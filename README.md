# Codex Session Manager

`csm` is a Bubble Tea TUI for inspecting, backing up, and deleting Codex session files under `~/.codex/sessions`.

## Usage

```sh
go run ./cmd/csm
```

Flags:

- `--sessions-dir`: override Codex sessions root.
- `--backup-dir`: override backup base directory.
- `--state-db`: override Codex state SQLite database used for session titles.
- `--dry-run`: show backup/delete actions without mutating files.
- `--list`: print parsed sessions and exit.

TUI keys:

- `space`: select session
- `/`: filter
- `b`: backup selected sessions
- `d`: delete selected sessions, with confirmation
- `r`: reload
- `q`: quit
