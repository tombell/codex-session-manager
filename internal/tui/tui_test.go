package tui

import (
	"testing"
	"time"

	"github.com/tombell/codex-session-manager/internal/sessions"
)

func TestSessionItemsSortsByCWDThenNewestAndPreservesSelectedState(t *testing.T) {
	found := []sessions.Session{
		{Path: "/sessions/beta.jsonl", CWD: "/work/beta", Timestamp: time.Date(2026, 5, 24, 11, 0, 0, 0, time.UTC)},
		{Path: "/sessions/alpha-new.jsonl", CWD: "/work/alpha", Timestamp: time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)},
		{Path: "/sessions/alpha-old.jsonl", CWD: "/work/alpha", Timestamp: time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)},
	}

	items := sessionItems(found, map[string]bool{"/sessions/alpha-old.jsonl": true})

	if len(items) != 3 {
		t.Fatalf("got %d items", len(items))
	}

	firstSession, ok := items[0].(sessionItem)
	if !ok || firstSession.session.Path != "/sessions/alpha-new.jsonl" || firstSession.selected {
		t.Fatalf("bad first session: %#v", items[0])
	}
	betaSession, ok := items[2].(sessionItem)
	if !ok || betaSession.session.Path != "/sessions/beta.jsonl" || betaSession.selected {
		t.Fatalf("bad beta session: %#v", items[2])
	}
	secondAlphaSession, ok := items[1].(sessionItem)
	if !ok || secondAlphaSession.session.Path != "/sessions/alpha-old.jsonl" || !secondAlphaSession.selected {
		t.Fatalf("bad second alpha session: %#v", items[1])
	}
}

func TestDisplayCWDFallsBackToRelativeDirectory(t *testing.T) {
	got := displayCWD(sessions.Session{Relative: "2026/05/24/session.jsonl"})
	if got != "2026/05/24" {
		t.Fatalf("displayCWD = %q", got)
	}
}
