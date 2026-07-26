package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tombell/codex-session-manager/internal/sessionfmt"
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
	return fmt.Sprintf("[%s] %s  %s  %s", marker, ts, sessionfmt.DisplayTitle(i.session), sessionfmt.HumanSize(i.session.Size))
}

func (i sessionItem) Description() string {
	if i.session.Title != "" && i.session.FirstPrompt != "" {
		return i.malformedPrefix() + i.session.FirstPrompt
	} else if i.session.FirstPrompt != "" {
		return i.malformedPrefix() + i.session.FirstPrompt
	}
	return i.malformedPrefix() + filepath.Base(i.session.Path)
}

func (i sessionItem) malformedPrefix() string {
	if i.session.Malformed {
		return "malformed jsonl; "
	}
	return ""
}

func (i sessionItem) FilterValue() string {
	return strings.Join([]string{i.session.ID, i.session.Title, i.session.CWD, i.session.Relative, i.session.FirstPrompt}, " ")
}

type itemDelegate struct {
	list.DefaultDelegate
}

func (d itemDelegate) Height() int               { return 3 }
func (d itemDelegate) Spacing() int              { return 1 }
func (d itemDelegate) ShortHelp() []key.Binding  { return nil }
func (d itemDelegate) FullHelp() [][]key.Binding { return nil }

func (d itemDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	session, ok := item.(sessionItem)
	if !ok {
		return
	}

	width := m.Width()
	if width <= 0 {
		return
	}

	styles := d.Styles
	padding := styles.NormalTitle.GetPaddingLeft() + styles.NormalTitle.GetPaddingRight()
	textWidth := max(width-padding, 0)

	cwd := ansi.Truncate("cwd: "+sessionfmt.ShortPath(displayCWD(session.session)), textWidth, "...")
	title := ansi.Truncate(session.Title(), textWidth, "...")
	desc := ansi.Truncate(session.Description(), textWidth, "...")

	isSelected := index == m.Index() && m.FilterState() != list.Filtering
	if isSelected {
		cwd = styles.SelectedDesc.Render(cwd)
		title = styles.SelectedTitle.Render(title)
		desc = styles.SelectedDesc.Render(desc)
	} else if m.FilterState() == list.Filtering && m.FilterValue() == "" {
		cwd = styles.DimmedDesc.Render(cwd)
		title = styles.DimmedTitle.Render(title)
		desc = styles.DimmedDesc.Render(desc)
	} else {
		cwd = styles.NormalDesc.Render(cwd)
		title = styles.NormalTitle.Render(title)
		desc = styles.NormalDesc.Render(desc)
	}

	fmt.Fprintf(w, "%s\n%s\n%s", cwd, title, desc) //nolint:errcheck
}

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
type loadedMsg struct {
	items []list.Item
}
type backedUpMsg struct{ target string }
type deletedMsg struct{ count int }

func Run(opts sessions.Options) error {
	program := tea.NewProgram(initialModel(opts), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func initialModel(opts sessions.Options) model {
	delegate := itemDelegate{DefaultDelegate: list.NewDefaultDelegate()}
	delegate.SetSpacing(1)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Codex Chats"
	l.SetShowStatusBar(false)
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
		status:   "loading chats...",
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
		m.status = fmt.Sprintf("loaded %d chats", len(msg.items))
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
			m.status = fmt.Sprintf("dry run: would delete %d chats", msg.count)
		} else {
			m.status = fmt.Sprintf("deleted %d chats", msg.count)
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
			m.status = fmt.Sprintf("hard-delete %d selected chats and their subagents? y/N", len(m.selectedSessions()))
			m.statusErr = true
			return m, nil
		case "r":
			m.status = "reloading chats..."
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
	includeSubagents := m.opts.IncludeSubagents
	selected := m.selected
	return func() tea.Msg {
		found, err := sessions.ScanWithTitles(root, stateDB)
		if err != nil {
			return errMsg{err: err}
		}
		if !includeSubagents {
			found = sessions.FilterSubagents(found)
		}
		return loadedMsg{items: sessionItems(found, selected)}
	}
}

func sessionItems(found []sessions.Session, selected map[string]bool) []list.Item {
	sorted := append([]sessions.Session(nil), found...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		leftCWD, rightCWD := displayCWD(left), displayCWD(right)
		if leftCWD != rightCWD {
			return leftCWD < rightCWD
		}
		if !left.Timestamp.Equal(right.Timestamp) {
			return left.Timestamp.After(right.Timestamp)
		}
		return left.Path > right.Path
	})

	items := make([]list.Item, 0, len(sorted))
	for _, session := range sorted {
		items = append(items, sessionItem{session: session, selected: selected[session.Path]})
	}
	return items
}

func displayCWD(session sessions.Session) string {
	if session.CWD != "" {
		return session.CWD
	}
	return filepath.Dir(session.Relative)
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
	root, stateDB, dryRun := m.root, m.opts.StateDB, m.opts.DryRun
	return func() tea.Msg {
		if err := sessions.Delete(root, stateDB, selected, dryRun); err != nil {
			return errMsg{err: err}
		}
		return deletedMsg{count: len(selected)}
	}
}
