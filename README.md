# Gomap

![Gomap mascot](./docs/mascot.svg)

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

Resume for MBOX imports:

- The copy command stores a byte offset for each MBOX file and destination mailbox in the state file. Re-running continues from that offset (no re-reading of already appended messages).
- Dry-run does not advance the offset.
- Use `--ignore-state` or a fresh `--state-file` to restart from the beginning of the MBOX.
- If the MBOX file changed (truncated/rotated) after a run, the stored offset may be invalid; restart with `--ignore-state` or a new state file.

Date handling for MBOX imports:

- The tool uses the message's `Date:` header if it can be parsed.
- If `Date:` is missing/unparseable, it falls back to (in order): `Resent-Date`, `Delivery-date`, then the earliest timestamp parsed from `Received:` headers. As a last resort, it uses the current time.
- This determines the INTERNALDATE on the destination server during APPEND.

Only import messages missing Date headers:

If you need to re-import only messages that had no parseable `Date:` (for example to correct dates), you can filter with:

```
./gomap copy \
  --mbox /path/to/mail.mbox \
  --dst-host imap.dest.example --dst-user user@dest.example --dst-pass 'app-password-dst' \
  --dst-mailbox INBOX \
  --mbox-only-missing-date
```

Note: `--mbox-only-missing-date` scans the whole file and ignores the resume offset so earlier messages without `Date:` aren't skipped.

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
- Include/Exclude filters and skip-special options apply to IMAP source mode. When using `--mbox`, the filters are not used.
- In MBOX → IMAP mode, the progress total reflects messages remaining from the current resume offset.

### Backup (IMAP → filesystem)

Download messages from a source IMAP account into the local filesystem. Two formats are supported:

- single-file: one .eml file per message under outputDir/<mailbox>/UID.eml (safe to resume; existing files are skipped)
- mbox: one mbox file per mailbox at outputDir/<mailbox>.mbox (appends messages)

Examples:

```
# Single-file mode (default)
./gomap backup \
  --src-host imap.source.example --src-user user@source.example --src-pass 'app-password-src' \
  --include '^(INBOX|Archive.*)$' --since 2024-01-01 \
  --output-dir backup

# Mbox mode
./gomap backup \
  --src-host imap.source.example --src-user user@source.example --src-pass 'app-password-src' \
  --format mbox \
  --output-dir backup
```

Flags (backup):

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
- Mailbox-to-path mapping: remote folder names become directories under `--output-dir` (single-file) or `.mbox` file names (mbox mode). Unsafe characters are sanitized to safe path segments.

### Mark-read (set \Seen)

Mark all messages as read in one or multiple mailboxes. Supports date range filters.

Examples:

```
# INBOX: all messages
./gomap mark-read \
  --dst-host imap.example --dst-user user@example --dst-pass 'app-pass' \
  --mailbox INBOX

# All mailboxes, filtered, date range inclusive
./gomap mark-read \
  --dst-host imap.example --dst-user user@example --dst-pass 'app-pass' \
  --all --include '^(INBOX|Archive.*)$' --exclude '^Spam|^Trash' \
  --start-date 2024-01-01 --end-date 2024-12-31
```

Flags:

- `--mailbox` NAME or `--all` with `--include/--exclude` (regex)
- `--start-date YYYY-MM-DD` (INTERNALDATE >=)
- `--end-date YYYY-MM-DD` (inclusive; internally BEFORE end+1d)
- IMAP connection flags: `--dst-host`, `--dst-port`, `--dst-user`, `--dst-pass`, `--dst-pass-prompt`, `--insecure`, `--starttls`

### Delete (with confirmation)

Delete messages in one or multiple mailboxes, optionally restricted by date range. A Bubble Tea confirmation prompt summarizes the action before applying. By default, messages are expunged after marking as `\\Deleted`.

Examples:

```
# Dry-run preview
./gomap delete \
  --dst-host imap.example --dst-user user@example --dst-pass 'app-pass' \
  --mailbox INBOX --end-date 2023-12-31 --dry-run

# Delete and expunge across mailboxes
./gomap delete \
  --dst-host imap.example --dst-user user@example --dst-pass 'app-pass' \
  --all --exclude '^Spam|^Trash' \
  --start-date 2020-01-01 --end-date 2022-12-31 \
  --expunge true
```

Flags:

- `--mailbox` NAME or `--all` with `--include/--exclude` (regex)
- `--start-date YYYY-MM-DD`, `--end-date YYYY-MM-DD` (inclusive end)
- `--dry-run` to preview without changes
- `--expunge` (default true) to permanently remove after marking `\\Deleted`
- IMAP connection flags: `--dst-host`, `--dst-port`, `--dst-user`, `--dst-pass`, `--dst-pass-prompt`, `--insecure`, `--starttls`

Safety:

- A TUI confirmation dialog summarizes mailbox, range and options. Confirm with `y`, cancel with `n`.

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

Security (SMTP):

- CLI SMTP passwords have the same caveats as IMAP. Prefer `--smtp-pass-prompt` on shared systems.

## Notes

- UID gaps: the tool stores only the highest UID per folder. Deleted or skipped UIDs may not be retried. Robust resume would require tracking a UID set.
- APPEND keeps flags and INTERNALDATE, but message IDs and UIDs on the destination will be new (different UIDVALIDITY/UIDs).
- Rate limits: some providers throttle parallel access. Reduce `--concurrency` if needed.
- STARTTLS vs TLS: use `--starttls` for port 143; use implicit TLS for 993.

Security:

- CLI passwords can show up in `ps` or shell history. Prefer `--src-pass-prompt`/`--dst-pass-prompt` on shared systems.

## State file format

By default, state is saved to `gomap-state.json` (override with `--state-file`).

Current keys:

- `mail_max_uid`: highest copied UID per IMAP mailbox (used by IMAP → IMAP copy resume)
- `mbox_offsets`: processed byte offsets for MBOX sources, keyed by `mbox:<abs-path>|dst:<Mailbox>` (used by MBOX → IMAP copy resume)

Example:

```
{
  "mail_max_uid": {
    "INBOX": 12345,
    "Archive/2024": 67890
  },
  "mbox_offsets": {
    "mbox:/home/user/backup.mbox|dst:Archive/2024": 10485760
  }
}
```

Notes:

- `mail_max_uid`: A message is considered for copy if it matches the date filter and its UID is greater than the stored value (unless `--ignore-state`).
- `mbox_offsets`: Offset is in bytes from the start of the MBOX file. Re-runs continue from that position. Use `--ignore-state` or a fresh `--state-file` to start from the beginning.
- If an MBOX file was truncated or rotated after a run, the stored offset may be invalid—restart with `--ignore-state` or delete the entry.
- Backup command does not use the state file: single-file mode resumes by skipping existing `UID.eml`; backup mbox mode appends and may duplicate on re-runs unless you constrain with `--since`.

## License

MIT
