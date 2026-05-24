package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tombell/codex-session-manager/internal/sessions"
)

type sessionItem struct {
	session  sessions.Session
	selected bool
}

func (i sessionItem) Title() string {
	ts := i.session.Timestamp.Local().Format("2006-01-02 15:04")
	marker := " "
	if i.selected {
		marker = "x"
	}
	return fmt.Sprintf("[%s] %s  %s  %s", marker, ts, displayTitle(i.session), humanSize(i.session.Size))
}

func (i sessionItem) Description() string {
	cwd := i.session.CWD
	if cwd == "" {
		cwd = filepath.Dir(i.session.Relative)
	}
	detail := shortPath(cwd)
	if i.session.Title != "" && i.session.FirstPrompt != "" {
		detail += " - " + i.session.FirstPrompt
	} else if i.session.FirstPrompt != "" {
		detail += " - " + i.session.FirstPrompt
	} else {
		detail += " - " + filepath.Base(i.session.Path)
	}
	if i.session.Malformed {
		detail = "malformed jsonl; " + detail
	}
	return detail
}

func (i sessionItem) FilterValue() string {
	return strings.Join([]string{i.session.ID, i.session.Title, i.session.CWD, i.session.Relative, i.session.FirstPrompt}, " ")
}

type itemDelegate struct {
	list.DefaultDelegate
}

func (d itemDelegate) ShortHelp() []key.Binding  { return nil }
func (d itemDelegate) FullHelp() [][]key.Binding { return nil }

type mode int

const (
	modeList mode = iota
	modeConfirmDelete
)

type model struct {
	list      list.Model
	opts      sessions.Options
	root      string
	mode      mode
	status    string
	statusErr bool
	selected  map[string]bool
}

type errMsg struct{ err error }
type loadedMsg struct{ items []list.Item }
type backedUpMsg struct{ target string }
type deletedMsg struct{ count int }

func initialModel(opts sessions.Options) model {
	delegate := itemDelegate{DefaultDelegate: list.NewDefaultDelegate()}
	delegate.SetSpacing(1)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Codex Sessions"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "select")),
			key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "backup")),
			key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")),
		}
	}
	return model{
		list:     l,
		opts:     opts,
		root:     opts.SessionsDir,
		selected: map[string]bool{},
		status:   "loading sessions...",
	}
}

func (m model) Init() tea.Cmd {
	return m.loadCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-2)
		return m, nil
	case loadedMsg:
		m.list.SetItems(msg.items)
		m.status = fmt.Sprintf("loaded %d sessions", len(msg.items))
		m.statusErr = false
		return m, nil
	case backedUpMsg:
		if m.opts.DryRun {
			m.status = "dry run: backup skipped"
		} else {
			m.status = "backed up to " + msg.target
		}
		m.statusErr = false
		return m, nil
	case deletedMsg:
		if m.opts.DryRun {
			m.status = fmt.Sprintf("dry run: would delete %d sessions", msg.count)
		} else {
			m.status = fmt.Sprintf("deleted %d sessions", msg.count)
			m.selected = map[string]bool{}
		}
		m.statusErr = false
		m.mode = modeList
		return m, m.loadCmd()
	case errMsg:
		m.status = msg.err.Error()
		m.statusErr = true
		m.mode = modeList
		return m, nil
	case tea.KeyMsg:
		if m.mode == modeConfirmDelete {
			switch msg.String() {
			case "y", "Y":
				selected := m.selectedSessions()
				return m, m.deleteCmd(selected)
			case "n", "N", "esc":
				m.mode = modeList
				m.status = "delete cancelled"
				m.statusErr = false
				return m, nil
			case "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case " ":
			return m.toggleSelected(), nil
		case "b":
			selected := m.selectedSessions()
			if len(selected) == 0 {
				m.status = "nothing selected"
				m.statusErr = true
				return m, nil
			}
			return m, m.backupCmd(selected)
		case "d":
			if len(m.selectedSessions()) == 0 {
				m.status = "nothing selected"
				m.statusErr = true
				return m, nil
			}
			m.mode = modeConfirmDelete
			m.status = fmt.Sprintf("delete %d selected sessions? y/N", len(m.selectedSessions()))
			m.statusErr = true
			return m, nil
		case "r":
			m.status = "reloading sessions..."
			m.statusErr = false
			return m, m.loadCmd()
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) View() string {
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	if m.statusErr {
		statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	}
	return m.list.View() + "\n" + statusStyle.Render(m.status)
}

func (m model) toggleSelected() model {
	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		return m
	}
	path := item.session.Path
	m.selected[path] = !m.selected[path]
	if !m.selected[path] {
		delete(m.selected, path)
	}
	items := m.list.Items()
	for idx, existing := range items {
		current, ok := existing.(sessionItem)
		if !ok || current.session.Path != path {
			continue
		}
		current.selected = m.selected[path]
		items[idx] = current
		break
	}
	m.list.SetItems(items)
	m.status = fmt.Sprintf("%d selected", len(m.selected))
	m.statusErr = false
	return m
}

func (m model) selectedSessions() []sessions.Session {
	var selected []sessions.Session
	for _, raw := range m.list.Items() {
		item, ok := raw.(sessionItem)
		if ok && m.selected[item.session.Path] {
			selected = append(selected, item.session)
		}
	}
	return selected
}

func (m model) loadCmd() tea.Cmd {
	root := m.root
	stateDB := m.opts.StateDB
	selected := m.selected
	return func() tea.Msg {
		found, err := sessions.ScanWithTitles(root, stateDB)
		if err != nil {
			return errMsg{err: err}
		}
		items := make([]list.Item, 0, len(found))
		for _, session := range found {
			items = append(items, sessionItem{session: session, selected: selected[session.Path]})
		}
		return loadedMsg{items: items}
	}
}

func (m model) backupCmd(selected []sessions.Session) tea.Cmd {
	root, backupDir, dryRun := m.root, m.opts.BackupDir, m.opts.DryRun
	return func() tea.Msg {
		target, err := sessions.Backup(root, backupDir, selected, time.Now(), dryRun)
		if err != nil {
			return errMsg{err: err}
		}
		return backedUpMsg{target: target}
	}
}

func (m model) deleteCmd(selected []sessions.Session) tea.Cmd {
	root, dryRun := m.root, m.opts.DryRun
	return func() tea.Msg {
		if err := sessions.Delete(root, selected, dryRun); err != nil {
			return errMsg{err: err}
		}
		return deletedMsg{count: len(selected)}
	}
}

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

	program := tea.NewProgram(initialModel(opts), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
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
			humanSize(session.Size),
			shortPath(session.CWD),
			displayTitle(session),
		)
	}
	return nil
}

func displayTitle(session sessions.Session) string {
	if session.Title != "" {
		return session.Title
	}
	if session.FirstPrompt != "" {
		return session.FirstPrompt
	}
	return filepath.Base(session.Path)
}

func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	if len(path) <= 58 {
		return path
	}
	return "..." + path[len(path)-55:]
}

func humanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
