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
	"net/smtp"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
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

	// send command
	sendCmd := &cobra.Command{
		Use:   "send",
		Short: "Send an email via SMTP",
		RunE:  runSend,
	}
	addSendFlags(sendCmd)
	// receive command
	receiveCmd := &cobra.Command{
		Use:   "receive",
		Short: "Receive emails from IMAP and store locally",
		RunE:  runReceive,
	}
	addReceiveFlags(receiveCmd)
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

// ========================= RECEIVE =========================

type receiveOptions struct {
	// IMAP source
	srcHost       string
	srcPort       int
	srcUser       string
	srcPass       string
	srcPassPrompt bool
	insecure      bool
	startTLS      bool
	include       string
	exclude       string
	since         string
	skipSpecial   bool
	skipTrash     bool
	skipJunk      bool
	skipDrafts    bool
	skipSent      bool
	outputDir     string
	format        string // single-file | mbox
	verbose       bool
}

func addReceiveFlags(cmd *cobra.Command) {
	o := &receiveOptions{}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = false
	cmd.Flags().StringVar(&o.srcHost, "src-host", "", "Source IMAP host")
	cmd.Flags().IntVar(&o.srcPort, "src-port", 993, "Source IMAP port")
	cmd.Flags().StringVar(&o.srcUser, "src-user", "", "Source IMAP username")
	cmd.Flags().StringVar(&o.srcPass, "src-pass", "", "Source IMAP password")
	cmd.Flags().BoolVar(&o.srcPassPrompt, "src-pass-prompt", false, "Prompt for source IMAP password (no echo)")
	cmd.Flags().BoolVar(&o.insecure, "insecure", false, "Skip TLS verification")
	cmd.Flags().BoolVar(&o.startTLS, "starttls", false, "Use STARTTLS instead of implicit TLS")
	cmd.Flags().StringVar(&o.include, "include", "", "Regex of mailboxes to include")
	cmd.Flags().StringVar(&o.exclude, "exclude", "", "Regex of mailboxes to exclude")
	cmd.Flags().StringVar(&o.since, "since", "", "Only download messages with INTERNALDATE >= since (YYYY-MM-DD)")
	cmd.Flags().BoolVar(&o.skipSpecial, "skip-special", false, "Skip common special folders like Trash/Junk/Drafts/Sent")
	cmd.Flags().BoolVar(&o.skipTrash, "skip-trash", false, "Skip Trash folders")
	cmd.Flags().BoolVar(&o.skipJunk, "skip-junk", false, "Skip Junk/Spam folders")
	cmd.Flags().BoolVar(&o.skipDrafts, "skip-drafts", false, "Skip Drafts folders")
	cmd.Flags().BoolVar(&o.skipSent, "skip-sent", false, "Skip Sent folders")
	cmd.Flags().StringVar(&o.outputDir, "output-dir", "gomap-download", "Directory to store downloaded emails")
	cmd.Flags().StringVar(&o.format, "format", "single-file", "Storage format: single-file or mbox")
	cmd.Flags().BoolVar(&o.verbose, "verbose", false, "Enable detailed logs")
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		cmd.SetContext(context.WithValue(cmd.Context(), ctxKey{}, o))
		return nil
	}
}

func runReceive(cmd *cobra.Command, args []string) error {
	o := cmd.Context().Value(ctxKey{}).(*receiveOptions)

	if o.srcPassPrompt && o.srcPass == "" {
		fmt.Fprint(os.Stderr, "Source password: ")
		b, perr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if perr != nil {
			return fmt.Errorf("read source password: %w", perr)
		}
		o.srcPass = string(b)
	}
	if o.srcHost == "" || o.srcUser == "" || o.srcPass == "" {
		return fmt.Errorf("missing required flags: --src-host, --src-user, --src-pass")
	}
	if o.format != "single-file" && o.format != "mbox" {
		return fmt.Errorf("invalid --format: %s (must be 'single-file' or 'mbox')", o.format)
	}
	if err := os.MkdirAll(o.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output-dir: %w", err)
	}
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
	src, err := imaputil.DialAndLogin(ctx, o.srcHost, o.srcPort, o.srcUser, o.srcPass, o.startTLS, tlsConfig)
	if err != nil {
		return fmt.Errorf("connect source: %w", err)
	}
	defer src.Logout()

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
		fmt.Println("No mailboxes to download.")
		return nil
	}

	for _, box := range filtered {
		if err := downloadMailbox(src, box, sinceTime, o); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] error: %v\n", box, err)
		}
	}
	return nil
}

func downloadMailbox(src *client.Client, box string, since time.Time, o *receiveOptions) error {
	// Select mailbox
	if _, err := imaputil.SelectMailbox(src, box, true); err != nil {
		return err
	}
	// Search UIDs
	uids, err := imaputil.SearchUIDsSince(src, since, 0)
	if err != nil {
		return err
	}
	if len(uids) == 0 {
		if o.verbose {
			log.Printf("[%s] no messages to download", box)
		}
		return nil
	}
	// Prepare output paths
	base := mailboxPath(o.outputDir, box)
	if o.format == "single-file" {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return err
		}
	} else {
		// ensure parent directory for mbox file exists
		parent := filepath.Dir(base)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}

	// Build seq set
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{section.FetchItem(), imap.FetchInternalDate, imap.FetchUid}
	seq := new(imap.SeqSet)
	for _, uid := range uids {
		seq.AddNum(uid)
	}
	msgs := make(chan *imap.Message, 64)
	go func() {
		_ = src.UidFetch(seq, items, msgs)
		close(msgs)
	}()

	var mboxFile *os.File
	var mboxPath string
	if o.format == "mbox" {
		// mbox file named after the mailbox, in its parent directory
		mboxPath = filepath.Join(filepath.Dir(base), filepath.Base(base)+".mbox")
		// Create or append
		f, err := os.OpenFile(mboxPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		mboxFile = f
		defer mboxFile.Close()
	}

	count := 0
	for msg := range msgs {
		if msg == nil {
			continue
		}
		uid := msg.Uid
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		// Read whole message
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			return err
		}
		raw := buf.Bytes()
		if o.format == "single-file" {
			outPath := filepath.Join(base, fmt.Sprintf("%d.eml", uid))
			// resume: skip if exists
			if _, err := os.Stat(outPath); err == nil {
				if o.verbose {
					log.Printf("[%s] skip existing %s", box, outPath)
				}
				continue
			}
			if err := os.WriteFile(outPath, raw, 0o644); err != nil {
				return err
			}
			if o.verbose {
				log.Printf("[%s] wrote %s", box, outPath)
			}
		} else {
			date := msg.InternalDate
			if date.IsZero() {
				date = time.Now()
			}
			if err := appendToMbox(mboxFile, raw, date); err != nil {
				return fmt.Errorf("append to mbox: %w", err)
			}
		}
		count++
	}
	if o.verbose {
		if o.format == "mbox" {
			log.Printf("[%s] appended %d messages to %s", box, count, mboxPath)
		} else {
			log.Printf("[%s] downloaded %d messages", box, count)
		}
	}
	return nil
}

func mailboxPath(outputDir, mailbox string) string {
	// Build a safe path under outputDir following mailbox hierarchy
	parts := strings.Split(mailbox, "/")
	safe := make([]string, 0, len(parts)+1)
	safe = append(safe, outputDir)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, ". ")
		p = strings.ReplaceAll(p, "..", "_")
		p = strings.ReplaceAll(p, string(os.PathSeparator), "_")
		if p == "" {
			p = "_"
		}
		safe = append(safe, p)
	}
	return filepath.Join(safe...)
}

func appendToMbox(f *os.File, raw []byte, date time.Time) error {
	// mboxrd style
	if date.IsZero() {
		date = time.Now()
	}
	// Standard mbox From_ line uses ctime format
	fromLine := fmt.Sprintf("From MAILER-DAEMON %s\n", date.Format(time.ANSIC))
	if _, err := f.WriteString(fromLine); err != nil {
		return err
	}
	// Escape any line beginning with 'From '
	br := bufio.NewReader(bytes.NewReader(raw))
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if strings.HasPrefix(line, "From ") {
				if _, werr := f.WriteString(">" + line); werr != nil {
					return werr
				}
			} else {
				if _, werr := f.WriteString(line); werr != nil {
					return werr
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	// Ensure trailing newline between messages
	if _, err := f.WriteString("\n"); err != nil {
		return err
	}
	return nil
}

// ========================= SEND =========================

type sendOptions struct {
	smtpHost       string
	smtpPort       int
	smtpUser       string
	smtpPass       string
	smtpPassPrompt bool
	startTLS       bool // use STARTTLS on plain connection (e.g., 587)
	ssl            bool // implicit TLS (e.g., 465)
	insecure       bool
	from           string
	to             []string
	subject        string
	body           string
	bodyFile       string
	rawFile        string
}

func addSendFlags(cmd *cobra.Command) {
	o := &sendOptions{}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = false
	cmd.Flags().StringVar(&o.smtpHost, "smtp-host", "", "SMTP server host")
	cmd.Flags().IntVar(&o.smtpPort, "smtp-port", 587, "SMTP server port")
	cmd.Flags().StringVar(&o.smtpUser, "smtp-user", "", "SMTP username")
	cmd.Flags().StringVar(&o.smtpPass, "smtp-pass", "", "SMTP password")
	cmd.Flags().BoolVar(&o.smtpPassPrompt, "smtp-pass-prompt", false, "Prompt for SMTP password (no echo)")
	cmd.Flags().BoolVar(&o.startTLS, "starttls", true, "Use STARTTLS (recommended for port 587)")
	cmd.Flags().BoolVar(&o.ssl, "ssl", false, "Use implicit TLS (recommended for port 465)")
	cmd.Flags().BoolVar(&o.insecure, "insecure", false, "Skip TLS verification")
	cmd.Flags().StringVar(&o.from, "from", "", "From email address")
	cmd.Flags().StringArrayVar(&o.to, "to", nil, "Recipient email address (repeatable)")
	cmd.Flags().StringVar(&o.subject, "subject", "", "Email subject")
	cmd.Flags().StringVar(&o.body, "body", "", "Email body (text/plain)")
	cmd.Flags().StringVar(&o.bodyFile, "body-file", "", "Read body from file")
	cmd.Flags().StringVar(&o.rawFile, "raw-file", "", "Send a raw RFC822 message from file (overrides other fields)")
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		cmd.SetContext(context.WithValue(cmd.Context(), ctxKey{}, o))
		return nil
	}
}

func runSend(cmd *cobra.Command, args []string) error {
	o := cmd.Context().Value(ctxKey{}).(*sendOptions)
	if o.smtpHost == "" || o.smtpPort == 0 {
		return fmt.Errorf("missing --smtp-host/--smtp-port")
	}
	if len(o.to) == 0 {
		return fmt.Errorf("at least one --to is required")
	}
	if o.from == "" {
		return fmt.Errorf("--from is required")
	}
	if o.smtpPassPrompt && o.smtpPass == "" {
		fmt.Fprint(os.Stderr, "SMTP password: ")
		b, perr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if perr != nil {
			return fmt.Errorf("read smtp password: %w", perr)
		}
		o.smtpPass = string(b)
	}
	// Build message
	var msg []byte
	if o.rawFile != "" {
		b, err := os.ReadFile(o.rawFile)
		if err != nil {
			return err
		}
		msg = b
	} else {
		var body string
		if o.bodyFile != "" {
			b, err := os.ReadFile(o.bodyFile)
			if err != nil {
				return err
			}
			body = string(b)
		} else {
			body = o.body
		}
		hdr := bytes.Buffer{}
		hdr.WriteString(fmt.Sprintf("From: %s\r\n", o.from))
		hdr.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(o.to, ", ")))
		if o.subject != "" {
			hdr.WriteString(fmt.Sprintf("Subject: %s\r\n", o.subject))
		}
		hdr.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
		hdr.WriteString("MIME-Version: 1.0\r\n")
		hdr.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		hdr.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		msg = append(hdr.Bytes(), []byte(body)...)
	}

	addr := fmt.Sprintf("%s:%d", o.smtpHost, o.smtpPort)
	tlsCfg := &tls.Config{ServerName: o.smtpHost, InsecureSkipVerify: o.insecure}

	// Helper to perform SMTP transaction using a client
	sendWithClient := func(c *smtp.Client) error {
		defer c.Close()
		// STARTTLS if requested and supported
		if !o.ssl && o.startTLS {
			if ok, _ := c.Extension("STARTTLS"); ok {
				if err := c.StartTLS(tlsCfg); err != nil {
					return err
				}
			}
		}
		// Auth if provided
		if o.smtpUser != "" {
			auth := smtp.PlainAuth("", o.smtpUser, o.smtpPass, o.smtpHost)
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
		if err := c.Mail(o.from); err != nil {
			return err
		}
		for _, rcpt := range o.to {
			if err := c.Rcpt(rcpt); err != nil {
				return err
			}
		}
		wc, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := wc.Write(msg); err != nil {
			_ = wc.Close()
			return err
		}
		if err := wc.Close(); err != nil {
			return err
		}
		return c.Quit()
	}

	if o.ssl {
		// Implicit TLS
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, o.smtpHost)
		if err != nil {
			return err
		}
		return sendWithClient(c)
	}
	// Plain TCP then optional STARTTLS
	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	return sendWithClient(c)
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

	// Load state to support resume by byte offset
	st, err := state.Load(o.stateFile)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	absPath, _ := filepath.Abs(o.mboxPath)
	stateKey := fmt.Sprintf("mbox:%s|dst:%s", absPath, o.dstMbox)
	var startOffset int64
	if !o.ignoreState {
		startOffset = st.GetMboxOffset(stateKey)
	}
	if startOffset > 0 {
		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			return fmt.Errorf("seek mbox to offset %d: %w", startOffset, err)
		}
	}

	// Count remaining messages quickly from current position
	total, err := countMboxMessages(f)
	if err != nil {
		return err
	}
	// reset file
	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
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
			curPos, _ := f.Seek(0, io.SeekCurrent)
			mr, err := r.NextMessage()
			if err == io.EOF {
				// reached end, save final offset
				if !o.dryRun {
					endPos, _ := f.Seek(0, io.SeekCurrent)
					st.SetMboxOffset(stateKey, endPos)
					_ = st.Save(o.stateFile)
				}
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
				// update state offset after successful append
				endPos, _ := f.Seek(0, io.SeekCurrent)
				// If NextMessage advanced file cursor from curPos to endPos, save endPos
				// In rare cases of reader buffering, prefer endPos when larger
				if endPos <= curPos {
					endPos = curPos
				}
				st.SetMboxOffset(stateKey, endPos)
				_ = st.Save(o.stateFile)
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
