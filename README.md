# Gomap

A small Go CLI to copy IMAP mailboxes and messages from a source account to a destination account.

Note: Test with non-production accounts first. Use at your own risk.

## Features

- Automatically creates missing folders on the destination
- Copies message content, flags and INTERNALDATE via APPEND
- Filters: include/exclude regex for folders
- Date filter: `--since YYYY-MM-DD`
- Resume: stores the highest copied UID per folder in a JSON state file
- Dry-run mode
- Configurable per-folder concurrency
- Bubble Tea TUI with a single overall progress bar by default, smoothed ETA, and quick cancel (q / Ctrl+C)

## Installation

Requires Go 1.22+

```
go build -o gomap ./cmd/gomap
```

### Releases

Tagged builds are published via GoReleaser for Linux, macOS, and Windows (amd64/arm64).
Artifacts and checksums are attached to the GitHub Release.

## Usage

Example (IMAP → IMAP):

```
./gomap copy \
  --src-host imap.source.example \
  --src-user user@source.example \
  --src-port 993 \
  --dst-host imap.dest.example \
  --dst-user user@dest.example \
  --dst-port 993 \
  --include '^(INBOX|Archive.*)$' \
  --exclude '^Trash|^Spam' \
  --since 2024-01-01 \
  --concurrency 3 \
  --state-file state.json
```

Passwords are provided via CLI flags only. Note: they can appear in the process list or shell history; use on trusted systems.

```
./gomap copy \
  --src-host imap.source.example --src-user user@source.example --src-pass 'app-password-src' \
  --dst-host imap.dest.example   --dst-user user@dest.example   --dst-pass 'app-password-dst' \
  --include '^(INBOX|Archive.*)$' --exclude '^Trash|^Spam' \
  --since 2024-01-01 --concurrency 3 --state-file state.json
```

MBOX → IMAP:

```
./gomap copy \
  --mbox ~/backup.mbox \
  --dst-host imap.dest.example --dst-user user@dest.example --dst-pass 'app-password-dst' \
  --dst-mailbox Archive/2024
```

You can also prompt for passwords (no echo):

```
./gomap copy \
  --src-host imap.source.example --src-user user@source.example --src-pass-prompt \
  --dst-host imap.dest.example   --dst-user user@dest.example   --dst-pass-prompt \
  --include '^(INBOX|Archive.*)$' --exclude '^Trash|^Spam' \
  --since 2024-01-01 --concurrency 3 --state-file state.json
```

Options:

- `--src-host`, `--src-port`, `--src-user`, `--src-pass`
- `--dst-host`, `--dst-port`, `--dst-user`, `--dst-pass`
  - or `--src-pass-prompt` / `--dst-pass-prompt` (prompt without echo)
- `--insecure` (disable TLS verify), `--starttls` (explicit STARTTLS)
- `--include`, `--exclude` (regex)
- `--since YYYY-MM-DD`
  - If omitted, defaults to `1970-01-01` (Unix epoch), effectively including all messages by date.
- `--dry-run`
- `--concurrency` (default 2)
- `--state-file` (default `gomap-state.json`)
- `--ignore-state` (start from UID 0 and ignore resume state)
Behavior notes:

- Date filter and resume state combine: messages must satisfy both (date >= since AND UID > stored max UID unless `--ignore-state`). To process everything regardless of previous runs, use `--ignore-state` (optionally with `--since`).
- `--skip-special`/`--skip-trash`/`--skip-junk`/`--skip-drafts`/`--skip-sent`
  (UI is quiet by default: single overall progress bar, no per-mail logging)
- `--verbose` (print detailed per-mailbox logs)

### Receive (IMAP → filesystem)

Download messages from a source IMAP account into the local filesystem. Two formats are supported:

- single-file: one .eml file per message under outputDir/<mailbox>/UID.eml (safe to resume; existing files are skipped)
- mbox: one mbox file per mailbox at outputDir/<mailbox>.mbox (appends messages)

Examples:

```
# Single-file mode (default)
./gomap receive \
  --src-host imap.source.example --src-user user@source.example --src-pass 'app-password-src' \
  --include '^(INBOX|Archive.*)$' --since 2024-01-01 \
  --output-dir backup

# Mbox mode
./gomap receive \
  --src-host imap.source.example --src-user user@source.example --src-pass 'app-password-src' \
  --format mbox \
  --output-dir backup
```

Flags (receive):

- `--src-host`, `--src-port`, `--src-user`, `--src-pass` (or `--src-pass-prompt`)
- `--insecure`, `--starttls`
- `--include`, `--exclude` (regex), `--since YYYY-MM-DD` (defaults to epoch)
- `--skip-special`/`--skip-trash`/`--skip-junk`/`--skip-drafts`/`--skip-sent`
- `--output-dir` (default `gomap-download`)
- `--format` single-file|mbox (default single-file)
- `--verbose`

Behavior:

- Single-file mode resumes by skipping existing files (UID.eml). Re-running is idempotent.
- Mbox mode appends raw messages; re-running may duplicate messages unless filtered with `--since` or external dedupe is used.

### Send (SMTP)

Send a message via SMTP (STARTTLS or implicit TLS).

Examples:

```
# Build message from fields
./gomap send \
  --smtp-host smtp.example --smtp-port 587 --smtp-user user@example --smtp-pass 'app-pass' \
  --from user@example --to rcpt1@example --to rcpt2@example \
  --subject "Hello" --body "This is the body"

# Send a raw RFC822 message from file
./gomap send \
  --smtp-host smtp.example --smtp-port 465 --ssl --smtp-user user@example --smtp-pass 'app-pass' \
  --from user@example --to rcpt@example \
  --raw-file message.eml
```

Flags (send):

- `--smtp-host`, `--smtp-port`, `--smtp-user`, `--smtp-pass` (or `--smtp-pass-prompt`)
- `--starttls` (default true), `--ssl` (implicit TLS), `--insecure`
- `--from`, `--to` (repeatable)
- Content options: `--subject`, `--body`, `--body-file`, or `--raw-file`

## Notes

- UID gaps: the tool stores only the highest UID per folder. Deleted or skipped UIDs may not be retried. Robust resume would require tracking a UID set.
- APPEND keeps flags and INTERNALDATE, but message IDs and UIDs on the destination will be new (different UIDVALIDITY/UIDs).
- Rate limits: some providers throttle parallel access. Reduce `--concurrency` if needed.
- STARTTLS vs TLS: use `--starttls` for port 143; use implicit TLS for 993.

Security:

- CLI passwords can show up in `ps` or shell history. Prefer `--src-pass-prompt`/`--dst-pass-prompt` on shared systems.

## License

MIT
