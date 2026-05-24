package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tombell/codex-session-manager/internal/sessionfmt"
	"github.com/tombell/codex-session-manager/internal/sessions"
	"github.com/tombell/codex-session-manager/internal/tui"
)

func main() {
	opts := sessions.Options{}
	listOnly := false
	flag.StringVar(&opts.SessionsDir, "sessions-dir", sessions.DefaultSessionsDir(), "Codex sessions directory")
	flag.StringVar(&opts.BackupDir, "backup-dir", sessions.DefaultBackupBaseDir(), "backup base directory")
	flag.StringVar(&opts.StateDB, "state-db", sessions.DefaultStateDBPath(), "Codex state SQLite database for session titles")
	flag.BoolVar(&opts.DryRun, "dry-run", false, "show actions without copying or deleting files")
	flag.BoolVar(&listOnly, "list", false, "list sessions and exit")
	flag.Parse()

	if opts.SessionsDir == "" {
		fmt.Fprintln(os.Stderr, "could not determine sessions directory")
		os.Exit(2)
	}
	root, err := filepath.Abs(opts.SessionsDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	opts.SessionsDir = root

	if listOnly {
		if err := printSessions(opts.SessionsDir, opts.StateDB); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := tui.Run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printSessions(root, stateDB string) error {
	found, err := sessions.ScanWithTitles(root, stateDB)
	if err != nil {
		return err
	}
	for _, session := range found {
		fmt.Printf("%s\t%s\t%s\t%s\n",
			session.Timestamp.Local().Format("2006-01-02 15:04"),
			sessionfmt.HumanSize(session.Size),
			sessionfmt.ShortPath(session.CWD),
			sessionfmt.DisplayTitle(session),
		)
	}
	return nil
}
