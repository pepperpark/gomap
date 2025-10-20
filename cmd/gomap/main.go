package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/emersion/go-mbox"
	"github.com/yourname/gomap/internal/imaputil"
	"github.com/yourname/gomap/internal/state"
	"github.com/yourname/gomap/internal/syncer"
)

var (
	// Set via -ldflags at build time.
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "gomap",
		Short: "Gomap - IMAP copy and utilities",
		RunE: func(cmd *cobra.Command, args []string) error {
			// default to help
			return cmd.Help()
		},
	}

	var showVersion bool
	rootCmd.PersistentFlags().BoolVarP(&showVersion, "version", "v", false, "Print version and exit")
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if showVersion {
			fmt.Printf("gomap %s", version)
			if commit != "" {
				fmt.Printf(" (%s)", commit)
			}
			if date != "" {
				fmt.Printf(" built %s", date)
			}
			fmt.Println()
			os.Exit(0)
		}
	}

	// copy subcommand
	copyCmd := &cobra.Command{
		Use:   "copy",
		Short: "Copy emails from IMAP or MBOX to destination IMAP",
		RunE:  runCopy,
	}
	addCopyFlags(copyCmd)
	rootCmd.AddCommand(copyCmd)

	// scaffolding for future commands
	sendCmd := &cobra.Command{
		Use:   "send",
		Short: "Send email (placeholder)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("'send' is not implemented yet.")
			return nil
		},
	}
	receiveCmd := &cobra.Command{
		Use:   "receive",
		Short: "Receive emails (placeholder)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("'receive' is not implemented yet.")
			return nil
		},
	}
	rootCmd.AddCommand(sendCmd, receiveCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// copy command options
type copyOptions struct {
	// IMAP source
	srcHost       string
	srcPort       int
	srcUser       string
	srcPass       string
	srcPassPrompt bool
	// MBOX source
	mboxPath string
	dstMbox  string // destination mailbox name when using mbox

	// Destination IMAP
	dstHost       string
	dstPort       int
	dstUser       string
	dstPass       string
	dstPassPrompt bool

	insecure    bool
	startTLS    bool
	include     string
	exclude     string
	since       string
	dryRun      bool
	concurrency int
	stateFile   string
	ignoreState bool
	skipSpecial bool
	skipTrash   bool
	skipJunk    bool
	skipDrafts  bool
	skipSent    bool
	mapPairs    []string
	verbose     bool
}

func addCopyFlags(cmd *cobra.Command) {
	o := &copyOptions{}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = false
	cmd.Flags().StringVar(&o.srcHost, "src-host", "", "Source IMAP host")
	cmd.Flags().IntVar(&o.srcPort, "src-port", 993, "Source IMAP port")
	cmd.Flags().StringVar(&o.srcUser, "src-user", "", "Source IMAP username")
	cmd.Flags().StringVar(&o.srcPass, "src-pass", "", "Source IMAP password")
	cmd.Flags().BoolVar(&o.srcPassPrompt, "src-pass-prompt", false, "Prompt for source IMAP password (no echo)")
	// MBOX
	cmd.Flags().StringVar(&o.mboxPath, "mbox", "", "Read from local MBOX file instead of source IMAP")
	cmd.Flags().StringVar(&o.dstMbox, "dst-mailbox", "INBOX", "Destination mailbox name when using --mbox")

	cmd.Flags().StringVar(&o.dstHost, "dst-host", "", "Destination IMAP host")
	cmd.Flags().IntVar(&o.dstPort, "dst-port", 993, "Destination IMAP port")
	cmd.Flags().StringVar(&o.dstUser, "dst-user", "", "Destination IMAP username")
	cmd.Flags().StringVar(&o.dstPass, "dst-pass", "", "Destination IMAP password")
	cmd.Flags().BoolVar(&o.dstPassPrompt, "dst-pass-prompt", false, "Prompt for destination IMAP password (no echo)")

	cmd.Flags().BoolVar(&o.insecure, "insecure", false, "Skip TLS verification")
	cmd.Flags().BoolVar(&o.startTLS, "starttls", false, "Use STARTTLS instead of implicit TLS")
	cmd.Flags().StringVar(&o.include, "include", "", "Regex of mailboxes to include (IMAP source)")
	cmd.Flags().StringVar(&o.exclude, "exclude", "", "Regex of mailboxes to exclude (IMAP source)")
	cmd.Flags().StringVar(&o.since, "since", "", "Only copy messages with INTERNALDATE >= since (YYYY-MM-DD)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Don't actually copy, just list actions")
	cmd.Flags().IntVar(&o.concurrency, "concurrency", 2, "Number of concurrent mailboxes to copy (IMAP source)")
	cmd.Flags().StringVar(&o.stateFile, "state-file", "gomap-state.json", "Path to resume state JSON")
	cmd.Flags().BoolVar(&o.ignoreState, "ignore-state", false, "Ignore resume state (start from UID 0)")

	cmd.Flags().BoolVar(&o.skipSpecial, "skip-special", false, "Skip common special folders like Trash/Junk/Drafts/Sent")
	cmd.Flags().BoolVar(&o.skipTrash, "skip-trash", false, "Skip Trash folders")
	cmd.Flags().BoolVar(&o.skipJunk, "skip-junk", false, "Skip Junk/Spam folders")
	cmd.Flags().BoolVar(&o.skipDrafts, "skip-drafts", false, "Skip Drafts folders")
	cmd.Flags().BoolVar(&o.skipSent, "skip-sent", false, "Skip Sent folders")
	cmd.Flags().StringArrayVar(&o.mapPairs, "map", nil, "Folder mapping src=dst (can be repeated)")
	cmd.Flags().BoolVar(&o.verbose, "verbose", false, "Enable detailed per-mailbox logs")

	// Bind into context
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		cmd.SetContext(context.WithValue(cmd.Context(), ctxKey{}, o))
		return nil
	}
}

type ctxKey struct{}

func runCopy(cmd *cobra.Command, args []string) error {
	o := cmd.Context().Value(ctxKey{}).(*copyOptions)

	// Prompt passwords if requested
	if o.srcPassPrompt && o.srcPass == "" {
		fmt.Fprint(os.Stderr, "Source password: ")
		b, perr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if perr != nil {
			return fmt.Errorf("read source password: %w", perr)
		}
		o.srcPass = string(b)
	}
	if o.dstPassPrompt && o.dstPass == "" {
		fmt.Fprint(os.Stderr, "Destination password: ")
		b, perr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if perr != nil {
			return fmt.Errorf("read destination password: %w", perr)
		}
		o.dstPass = string(b)
	}

	// Validate required flags depending on mode
	if o.mboxPath == "" {
		// IMAP source mode
		if o.srcHost == "" || o.srcUser == "" || o.srcPass == "" || o.dstHost == "" || o.dstUser == "" || o.dstPass == "" {
			return fmt.Errorf("missing required flags: --src-host, --src-user, --src-pass, --dst-host, --dst-user, --dst-pass")
		}
		return runCopyIMAP(cmd, o)
	}
	// MBOX source mode
	if o.dstHost == "" || o.dstUser == "" || o.dstPass == "" {
		return fmt.Errorf("missing required flags: --dst-host, --dst-user, --dst-pass (required with --mbox)")
	}
	return runCopyMBOX(cmd, o)
}

func runCopyIMAP(cmd *cobra.Command, o *copyOptions) error {
	var includeRe, excludeRe *regexp.Regexp
	var err error
	if o.include != "" {
		includeRe, err = regexp.Compile(o.include)
		if err != nil {
			return fmt.Errorf("invalid --include regex: %w", err)
		}
	}
	if o.exclude != "" {
		excludeRe, err = regexp.Compile(o.exclude)
		if err != nil {
			return fmt.Errorf("invalid --exclude regex: %w", err)
		}
	}

	var sinceTime time.Time
	if o.since != "" {
		sinceTime, err = time.Parse("2006-01-02", o.since)
		if err != nil {
			return fmt.Errorf("invalid --since date: %w (expected YYYY-MM-DD)", err)
		}
	} else {
		sinceTime = time.Unix(0, 0).UTC()
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: o.insecure}
	ctx := cmd.Context()

	st, err := state.Load(o.stateFile)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	src, err := imaputil.DialAndLogin(ctx, o.srcHost, o.srcPort, o.srcUser, o.srcPass, o.startTLS, tlsConfig)
	if err != nil {
		return fmt.Errorf("connect source: %w", err)
	}
	defer src.Logout()

	dst, err := imaputil.DialAndLogin(ctx, o.dstHost, o.dstPort, o.dstUser, o.dstPass, o.startTLS, tlsConfig)
	if err != nil {
		return fmt.Errorf("connect destination: %w", err)
	}
	defer dst.Logout()

	boxes, err := imaputil.ListMailboxes(ctx, src)
	if err != nil {
		return fmt.Errorf("list mailboxes: %w", err)
	}

	specialPatterns := []string{}
	if o.skipSpecial || o.skipTrash {
		specialPatterns = append(specialPatterns, `(?i)^(Trash|Gelöscht.*|Deleted Items|Papierkorb)$`)
	}
	if o.skipSpecial || o.skipJunk {
		specialPatterns = append(specialPatterns, `(?i)^(Junk|Spam|Bulk Mail|Unerw.*)$`)
	}
	if o.skipSpecial || o.skipDrafts {
		specialPatterns = append(specialPatterns, `(?i)^(Drafts|Entwürfe)$`)
	}
	if o.skipSpecial || o.skipSent {
		specialPatterns = append(specialPatterns, `(?i)^(Sent( Items)?|Gesendet.*)$`)
	}
	var specialRe *regexp.Regexp
	if len(specialPatterns) > 0 {
		specialRe = regexp.MustCompile(strings.Join(specialPatterns, "|"))
	}

	filtered := make([]string, 0, len(boxes))
	for _, b := range boxes {
		name := b
		if includeRe != nil && !includeRe.MatchString(name) {
			continue
		}
		if excludeRe != nil && excludeRe.MatchString(name) {
			continue
		}
		if specialRe != nil && specialRe.MatchString(name) {
			continue
		}
		filtered = append(filtered, name)
	}
	if len(filtered) == 0 {
		fmt.Println("No mailboxes to process.")
		return nil
	}

	folderMap := parseMappings(o.mapPairs)
	worker := syncer.NewMailboxSyncer(src, dst, st, syncer.Options{
		DryRun:      o.dryRun,
		Since:       sinceTime,
		Concurrency: o.concurrency,
		Quiet:       !o.verbose,
		Map:         folderMap,
		IgnoreState: o.ignoreState,
	})

	if o.verbose {
		resumeBoxes := 0
		for _, b := range filtered {
			if st.GetMaxUID(b) > 0 {
				resumeBoxes++
			}
		}
		fmt.Printf("Starting sync: %d mailbox(es), concurrency=%d, dry-run=%v\n", len(filtered), o.concurrency, o.dryRun)
		fmt.Printf("  since=%s  ignore-state=%v  state-file=%s\n", sinceTime.Format("2006-01-02"), o.ignoreState, o.stateFile)
		fmt.Printf("  resume status: %d/%d mailbox(es) have prior progress\n", resumeBoxes, len(filtered))
		if !o.ignoreState && resumeBoxes > 0 {
			fmt.Println("  tip: use --ignore-state or a fresh --state-file to process everything again")
		}
	}

	errs := runTUI(ctx, worker, filtered)
	if len(errs) > 0 {
		fmt.Println("Finished with errors:")
		for _, e := range errs {
			fmt.Println(" -", e)
		}
	}
	if err := st.Save(o.stateFile); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

func runCopyMBOX(cmd *cobra.Command, o *copyOptions) error {
	// Open mbox
	f, err := os.Open(o.mboxPath)
	if err != nil {
		return fmt.Errorf("open mbox: %w", err)
	}
	defer f.Close()

	// Count messages quickly (scan From lines)
	total, err := countMboxMessages(f)
	if err != nil {
		return err
	}
	// reset file
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: o.insecure}
	ctx := cmd.Context()
	dst, err := imaputil.DialAndLogin(ctx, o.dstHost, o.dstPort, o.dstUser, o.dstPass, o.startTLS, tlsConfig)
	if err != nil {
		return fmt.Errorf("connect destination: %w", err)
	}
	defer dst.Logout()

	// Ensure destination mailbox exists
	if err := imaputil.EnsureMailbox(dst, o.dstMbox); err != nil {
		return fmt.Errorf("ensure mailbox: %w", err)
	}

	progress := make(chan int, 128)
	errc := make(chan error, 1)

	go func() {
		defer close(progress)
		defer close(errc)
		r := mbox.NewReader(f)
		for {
			mr, err := r.NextMessage()
			if err == io.EOF {
				errc <- nil
				return
			}
			if err != nil {
				errc <- fmt.Errorf("read mbox: %w", err)
				return
			}
			var bldr strings.Builder
			if _, err := io.Copy(&bldr, mr); err != nil {
				errc <- fmt.Errorf("read message: %w", err)
				return
			}
			raw := bldr.String()
			var date time.Time
			if msg, perr := mail.ReadMessage(strings.NewReader(raw)); perr == nil {
				if dh := msg.Header.Get("Date"); dh != "" {
					if t, per := mail.ParseDate(dh); per == nil {
						date = t
					}
				}
			}
			if date.IsZero() {
				date = time.Now()
			}
			if o.dryRun {
				if o.verbose {
					log.Printf("[dry-run] append %s date=%s", o.dstMbox, date.Format(time.RFC3339))
				}
			} else {
				if _, err := imaputil.SelectMailbox(dst, o.dstMbox, false); err != nil {
					errc <- err
					return
				}
				lit := bytes.NewReader([]byte(raw))
				if err := dst.Append(o.dstMbox, nil, date, lit); err != nil {
					errc <- fmt.Errorf("append: %w", err)
					return
				}
			}
			progress <- 1
		}
	}()

	_ = runMboxTUI(total, progress, errc)
	return nil
}

func countMboxMessages(r io.Reader) (int, error) {
	// Count lines beginning with 'From ' which separate messages.
	// Using a buffered scanner for speed.
	br := bufio.NewReader(r)
	count := 0
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if strings.HasPrefix(line, "From ") {
			count++
		}
	}
	return count, nil
}

// parseMappings converts `src=dst` pairs into a map

// parseMappings converts `src=dst` pairs into a map
func parseMappings(pairs []string) map[string]string {
	m := make(map[string]string)
	for _, p := range pairs {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Invalid --map value (expected src=dst): %s\n", p)
			continue
		}
		m[parts[0]] = parts[1]
	}
	return m
}

// TUI implemented in tui.go
