package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/yourname/gomap/internal/imaputil"
	"github.com/yourname/gomap/internal/state"
	"github.com/yourname/gomap/internal/syncer"
)

var (
	// These are set via -ldflags at build time.
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	// Custom help/usage
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Gomap - Copy IMAP folders and emails between accounts\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  gomap [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	var (
		srcHost     string
		srcUser     string
		srcPass     string
		srcPort     int
		dstHost     string
		dstUser     string
		dstPass     string
		dstPort     int
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
	)

	flag.StringVar(&srcHost, "src-host", "", "Source IMAP host")
	flag.IntVar(&srcPort, "src-port", 993, "Source IMAP port")
	flag.StringVar(&srcUser, "src-user", "", "Source IMAP username")
	flag.StringVar(&srcPass, "src-pass", "", "Source IMAP password")

	flag.StringVar(&dstHost, "dst-host", "", "Destination IMAP host")
	flag.IntVar(&dstPort, "dst-port", 993, "Destination IMAP port")
	flag.StringVar(&dstUser, "dst-user", "", "Destination IMAP username")
	flag.StringVar(&dstPass, "dst-pass", "", "Destination IMAP password")

	flag.BoolVar(&insecure, "insecure", false, "Skip TLS verification")
	flag.BoolVar(&startTLS, "starttls", false, "Use STARTTLS instead of implicit TLS")
	flag.StringVar(&include, "include", "", "Regex of mailboxes to include")
	flag.StringVar(&exclude, "exclude", "", "Regex of mailboxes to exclude")
	flag.StringVar(&since, "since", "", "Only copy messages with INTERNALDATE >= since (YYYY-MM-DD)")
	flag.BoolVar(&dryRun, "dry-run", false, "Don't actually copy, just list actions")
	flag.IntVar(&concurrency, "concurrency", 2, "Number of concurrent mailboxes to copy")
	flag.StringVar(&stateFile, "state-file", "gomap-state.json", "Path to resume state JSON")
	flag.BoolVar(&ignoreState, "ignore-state", false, "Ignore resume state (start from UID 0)")

	flag.BoolVar(&skipSpecial, "skip-special", false, "Skip common special folders like Trash/Junk/Drafts/Sent")
	flag.BoolVar(&skipTrash, "skip-trash", false, "Skip Trash folders")
	flag.BoolVar(&skipJunk, "skip-junk", false, "Skip Junk/Spam folders")
	flag.BoolVar(&skipDrafts, "skip-drafts", false, "Skip Drafts folders")
	flag.BoolVar(&skipSent, "skip-sent", false, "Skip Sent folders")
	flag.StringArrayVar(&mapPairs, "map", nil, "Folder mapping src=dst (can be repeated)")
	flag.BoolVar(&verbose, "verbose", false, "Enable detailed per-mailbox logs")

	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.BoolVarP(&showVersion, "v", "v", false, "Print version and exit (shorthand)")

	flag.Parse()

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

	// No args: show help
	if len(os.Args) == 1 {
		flag.Usage()
		os.Exit(2)
	}

	// passwords must be provided via flags

	if srcHost == "" || srcUser == "" || srcPass == "" || dstHost == "" || dstUser == "" || dstPass == "" {
		fmt.Fprintln(os.Stderr, "Error: missing required flags: --src-host, --src-user, --src-pass, --dst-host, --dst-user, --dst-pass")
		flag.Usage()
		os.Exit(2)
	}

	var includeRe, excludeRe *regexp.Regexp
	var err error
	if include != "" {
		includeRe, err = regexp.Compile(include)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --include regex: %v\n", err)
			flag.Usage()
			os.Exit(2)
		}
	}
	if exclude != "" {
		excludeRe, err = regexp.Compile(exclude)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --exclude regex: %v\n", err)
			flag.Usage()
			os.Exit(2)
		}
	}

	var sinceTime time.Time
	if since != "" {
		sinceTime, err = time.Parse("2006-01-02", since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --since date: %v (expected YYYY-MM-DD)\n", err)
			flag.Usage()
			os.Exit(2)
		}
	} else {
		// Default to Unix epoch (1970-01-01) so the date filter is inclusive for all realistic messages
		sinceTime = time.Unix(0, 0).UTC()
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: insecure}

	ctx := context.Background()

	st, err := state.Load(stateFile)
	if err != nil {
		log.Fatalf("load state: %v", err)
	}

	src, err := imaputil.DialAndLogin(ctx, srcHost, srcPort, srcUser, srcPass, startTLS, tlsConfig)
	if err != nil {
		log.Fatalf("connect source: %v", err)
	}
	defer src.Logout()

	dst, err := imaputil.DialAndLogin(ctx, dstHost, dstPort, dstUser, dstPass, startTLS, tlsConfig)
	if err != nil {
		log.Fatalf("connect destination: %v", err)
	}
	defer dst.Logout()

	// Discover mailboxes
	boxes, err := imaputil.ListMailboxes(ctx, src)
	if err != nil {
		log.Fatalf("list mailboxes: %v", err)
	}

	// Build special folders patterns if requested
	specialPatterns := []string{}
	if skipSpecial || skipTrash {
		specialPatterns = append(specialPatterns, `(?i)^(Trash|Gelöscht.*|Deleted Items|Papierkorb)$`)
	}
	if skipSpecial || skipJunk {
		specialPatterns = append(specialPatterns, `(?i)^(Junk|Spam|Bulk Mail|Unerw.*)$`)
	}
	if skipSpecial || skipDrafts {
		specialPatterns = append(specialPatterns, `(?i)^(Drafts|Entwürfe)$`)
	}
	if skipSpecial || skipSent {
		specialPatterns = append(specialPatterns, `(?i)^(Sent( Items)?|Gesendet.*)$`)
	}
	var specialRe *regexp.Regexp
	if len(specialPatterns) > 0 {
		specialRe = regexp.MustCompile(strings.Join(specialPatterns, "|"))
	}

	// Filter
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
		return
	}

	// Quiet mode is default; no extra banner output

	// Build mapping map
	folderMap := parseMappings(mapPairs)

	worker := syncer.NewMailboxSyncer(src, dst, st, syncer.Options{
		DryRun:      dryRun,
		Since:       sinceTime,
		Concurrency: concurrency,
		Quiet:       !verbose,
		Map:         folderMap,
		IgnoreState: ignoreState,
	})

	if verbose {
		// Count how many mailboxes have a stored resume UID
		resumeBoxes := 0
		for _, b := range filtered {
			if st.GetMaxUID(b) > 0 {
				resumeBoxes++
			}
		}
		fmt.Printf("Starting sync: %d mailbox(es), concurrency=%d, dry-run=%v\n", len(filtered), concurrency, dryRun)
		fmt.Printf("  since=%s  ignore-state=%v  state-file=%s\n", sinceTime.Format("2006-01-02"), ignoreState, stateFile)
		fmt.Printf("  resume status: %d/%d mailbox(es) have prior progress\n", resumeBoxes, len(filtered))
		if !ignoreState && resumeBoxes > 0 {
			fmt.Println("  tip: use --ignore-state or a fresh --state-file to process everything again")
		}
	}

	// Start TUI progress
	errs := runTUI(ctx, worker, filtered)
	if len(errs) > 0 {
		fmt.Println("Finished with errors:")
		for _, e := range errs {
			fmt.Println(" -", e)
		}
	}

	if err := st.Save(stateFile); err != nil {
		log.Fatalf("save state: %v", err)
	}

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
