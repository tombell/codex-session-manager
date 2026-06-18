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
- `--include-subagents`: include subagent sessions (e.g. `guardian`) that are hidden by default.
- `--list`: print parsed sessions and exit.

Codex spawns subagent threads (such as the `guardian` approval reviewer) that each write their own rollout file, so a single interactive session can produce multiple session files. These subagent sessions are hidden by default; use `--include-subagents` to show them.

TUI keys:

- `space`: select session
- `/`: filter
- `b`: backup selected sessions
- `d`: delete selected sessions, with confirmation
- `r`: reload
- `q`: quit
