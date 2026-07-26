# Codex Session Manager

`csm` is a Bubble Tea TUI for inspecting, backing up, and deleting Codex chats under `~/.codex/sessions`.

## Usage

```sh
go run ./cmd/csm
```

Flags:

- `--sessions-dir`: override Codex sessions root.
- `--backup-dir`: override backup base directory.
- `--state-db`: override the Codex state SQLite database used for chat names and metadata.
- `--dry-run`: show backup/delete actions without mutating files.
- `--include-subagents`: include subagent sessions (e.g. `guardian`) that are hidden by default.
- `--list`: print parsed sessions and exit.

Codex spawns subagent threads (such as the `guardian` approval reviewer) that each write their own rollout file, so a single interactive session can produce multiple session files. These subagent sessions are hidden by default; use `--include-subagents` to show them.

Deletion uses Codex's `thread/delete` app-server API (tested with Codex CLI 0.145). It hard-deletes the chat, its rollout files, associated SQLite state, and spawned descendant threads. Malformed rollout files without a thread ID are removed directly because they have no addressable Codex chat.

TUI keys:

- `space`: select session
- `/`: filter
- `b`: backup selected sessions
- `d`: hard-delete selected chats, with confirmation
- `r`: reload
- `q`: quit
