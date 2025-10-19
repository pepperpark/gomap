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

Example:

```
./gomap \
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
./gomap \
  --src-host imap.source.example --src-user user@source.example --src-pass 'app-password-src' \
  --dst-host imap.dest.example   --dst-user user@dest.example   --dst-pass 'app-password-dst' \
  --include '^(INBOX|Archive.*)$' --exclude '^Trash|^Spam' \
  --since 2024-01-01 --concurrency 3 --state-file state.json
```

Options:

- `--src-host`, `--src-port`, `--src-user`, `--src-pass`
- `--dst-host`, `--dst-port`, `--dst-user`, `--dst-pass`
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

## Notes

- UID gaps: the tool stores only the highest UID per folder. Deleted or skipped UIDs may not be retried. Robust resume would require tracking a UID set.
- APPEND keeps flags and INTERNALDATE, but message IDs and UIDs on the destination will be new (different UIDVALIDITY/UIDs).
- Rate limits: some providers throttle parallel access. Reduce `--concurrency` if needed.
- STARTTLS vs TLS: use `--starttls` for port 143; use implicit TLS for 993.

Security:

- CLI passwords can show up in `ps` or shell history. If preferred, you can use interactive prompts (`--src-pass-prompt`/`--dst-pass-prompt`).
  Note: interactive password prompts are not implemented in this version; use flags only.

## License

MIT
