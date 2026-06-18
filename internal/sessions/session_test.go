package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanParsesMetadataAndFirstPrompt(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t, root, "2026/05/24/rollout-2026-05-24T10-00-00-id.jsonl", `{"type":"session_meta","payload":{"id":"abc","timestamp":"2026-05-24T10:00:00Z","cwd":"/tmp/project"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello\nworld"}]}}
`)

	sessions, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	got := sessions[0]
	if got.Path != path || got.ID != "abc" || got.CWD != "/tmp/project" {
		t.Fatalf("bad session: %#v", got)
	}
	if got.FirstPrompt != "hello world" {
		t.Fatalf("first prompt = %q", got.FirstPrompt)
	}
	if got.Timestamp.Format(time.RFC3339) != "2026-05-24T10:00:00Z" {
		t.Fatalf("timestamp = %s", got.Timestamp.Format(time.RFC3339))
	}
}

func TestScanSkipsCodexScaffoldingWhenFindingFirstPrompt(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "2026/05/24/session.jsonl", `{"type":"session_meta","payload":{"id":"abc"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /tmp/project"},{"type":"input_text","text":"<environment_context>...</environment_context>"}]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"real user request"}]}}
`)

	sessions, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].FirstPrompt != "real user request" {
		t.Fatalf("first prompt = %q", sessions[0].FirstPrompt)
	}
}

func TestScanDetectsSubagentSessions(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "2026/06/18/parent.jsonl", `{"type":"session_meta","payload":{"id":"parent","timestamp":"2026-06-18T10:00:00Z","source":"vscode","thread_source":"user"}}
`)
	writeSession(t, root, "2026/06/18/guardian.jsonl", `{"type":"session_meta","payload":{"id":"guardian","parent_thread_id":"parent","timestamp":"2026-06-18T10:00:01Z","source":{"subagent":{"other":"guardian"}},"thread_source":"subagent"}}
`)

	found, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("got %d sessions", len(found))
	}

	filtered := FilterSubagents(found)
	if len(filtered) != 1 {
		t.Fatalf("FilterSubagents kept %d sessions", len(filtered))
	}
	if filtered[0].ID != "parent" {
		t.Fatalf("kept wrong session: %q", filtered[0].ID)
	}

	for _, s := range found {
		switch s.ID {
		case "parent":
			if s.Subagent {
				t.Fatal("parent marked as subagent")
			}
		case "guardian":
			if !s.Subagent || s.ParentID != "parent" {
				t.Fatalf("guardian not detected: %#v", s)
			}
		}
	}
}

func TestScanMalformedFallsBackToFileInfo(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "2026/05/24/bad.jsonl", `not json
{"type":"event_msg","payload":{"type":"user_message","message":"first prompt"}}
`)

	sessions, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if !sessions[0].Malformed {
		t.Fatal("expected malformed session")
	}
	if sessions[0].FirstPrompt != "first prompt" {
		t.Fatalf("first prompt = %q", sessions[0].FirstPrompt)
	}
	if sessions[0].Timestamp.IsZero() {
		t.Fatal("expected modtime fallback")
	}
}

func TestScanWithMetadataEnrichesFromStateDB(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t, root, "2026/05/24/session.jsonl", `{"type":"session_meta","payload":{"id":"abc"}}
`)
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", stateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("create table threads (rollout_path text not null, title text not null, first_user_message text not null, cwd text not null)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into threads (rollout_path, title, first_user_message, cwd) values (?, ?, ?, ?)", path, "Session Title", "first from db", "/tmp/from-db"); err != nil {
		t.Fatal(err)
	}

	sessions, err := ScanWithTitles(root, stateDB)
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Title != "Session Title" || sessions[0].FirstPrompt != "first from db" || sessions[0].CWD != "/tmp/from-db" {
		t.Fatalf("bad metadata: %#v", sessions[0])
	}
}

func TestScanWithTitlesEnrichesFromOldStateDBSchema(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t, root, "2026/05/24/session.jsonl", `{"type":"session_meta","payload":{"id":"abc"}}
`)
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", stateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("create table threads (rollout_path text not null, title text not null)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into threads (rollout_path, title) values (?, ?)", path, "Session Title"); err != nil {
		t.Fatal(err)
	}

	sessions, err := ScanWithTitles(root, stateDB)
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Title != "Session Title" {
		t.Fatalf("title = %q", sessions[0].Title)
	}
}

func TestBackupPreservesRelativePath(t *testing.T) {
	root := t.TempDir()
	backupBase := t.TempDir()
	path := writeSession(t, root, "2026/05/24/session.jsonl", `{"type":"session_meta","payload":{}}
`)
	selected := []Session{{Path: path}}

	target, err := Backup(root, backupBase, selected, time.Date(2026, 5, 24, 12, 30, 0, 0, time.UTC), false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "2026/05/24/session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == "" {
		t.Fatal("backup is empty")
	}
}

func TestDeleteRemovesFilesPrunesEmptyDirsAndRefusesOutsideRoot(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t, root, "2026/05/24/session.jsonl", `{"type":"session_meta","payload":{}}
`)
	outside := writeSession(t, t.TempDir(), "outside.jsonl", "{}")

	if err := Delete(root, []Session{{Path: outside}}, false); err == nil {
		t.Fatal("expected outside-root refusal")
	}
	if err := Delete(root, []Session{{Path: path}}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "2026")); !os.IsNotExist(err) {
		t.Fatalf("empty dirs not pruned: %v", err)
	}
}

func TestDryRunDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	backupBase := t.TempDir()
	path := writeSession(t, root, "2026/05/24/session.jsonl", "{}")

	target, err := Backup(root, backupBase, []Session{{Path: path}}, time.Date(2026, 5, 24, 12, 30, 0, 0, time.UTC), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run backup created target: %v", err)
	}
	if err := Delete(root, []Session{{Path: path}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dry-run delete removed file: %v", err)
	}
}

func writeSession(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
