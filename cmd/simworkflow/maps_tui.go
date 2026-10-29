package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"simworkflow/internal/mapcatalog"
)

type mapNamesMsg struct {
	request int
	names   []string
	err     error
}
type mapVersionsMsg struct {
	request int
	name    string
	entries []mapcatalog.Metadata
	err     error
}
type mapBrowserModel struct {
	ctx                             context.Context
	cancelLoad                      context.CancelFunc
	loadNames                       func(context.Context) ([]string, error)
	loadVersions                    func(context.Context, string) ([]mapcatalog.Metadata, error)
	names                           []string
	entries                         []mapcatalog.Metadata
	selectedMap, query              string
	selected, top, x, width, height int
	nameSelected, nameTop           int
	editing, loading                bool
	request                         int
	err                             error
}

func browseMaps(ctx context.Context, catalog *mapcatalog.Catalog, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := mapBrowserModel{ctx: ctx, loadNames: catalog.Names, loadVersions: catalog.List, width: 80, height: 24, loading: true}
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}
func (m mapBrowserModel) Init() tea.Cmd { return m.fetchNames(m.ctx, m.request) }
func (m mapBrowserModel) fetchNames(ctx context.Context, request int) tea.Cmd {
	return func() tea.Msg { names, err := m.loadNames(ctx); return mapNamesMsg{request, names, err} }
}
func (m *mapBrowserModel) fetch() tea.Cmd {
	if m.cancelLoad != nil {
		m.cancelLoad()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelLoad = cancel
	m.request++
	m.loading = true
	m.err = nil
	request, name := m.request, m.selectedMap
	if name == "" {
		return m.fetchNames(ctx, request)
	}
	return func() tea.Msg {
		entries, err := m.loadVersions(ctx, name)
		return mapVersionsMsg{request, name, entries, err}
	}
}
func (m mapBrowserModel) visibleNames() []string {
	if m.query == "" {
		return m.names
	}
	names := []string{}
	for _, name := range m.names {
		if strings.Contains(name, strings.ToLower(m.query)) {
			names = append(names, name)
		}
	}
	return names
}
func (m mapBrowserModel) count() int {
	if m.selectedMap != "" {
		return len(m.entries)
	}
	return len(m.visibleNames())
}
func (m mapBrowserModel) bodyHeight() int { return max(0, m.height-5) }
func (m *mapBrowserModel) clamp() {
	m.selected = max(0, min(m.selected, m.count()-1))
	n := m.bodyHeight()
	if m.selected < m.top {
		m.top = m.selected
	}
	if n > 0 && m.selected >= m.top+n {
		m.top = m.selected - n + 1
	}
	m.top = max(0, min(m.top, max(0, m.count()-n)))
	m.x = max(0, m.x)
}
func (m mapBrowserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case mapNamesMsg:
		if msg.request != m.request || m.selectedMap != "" {
			return m, nil
		}
		m.names, m.err, m.loading = msg.names, msg.err, false
	case mapVersionsMsg:
		if msg.request != m.request || msg.name != m.selectedMap {
			return m, nil
		}
		m.entries, m.err, m.loading = msg.entries, msg.err, false
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" || key == "q" && !m.editing {
			return m, tea.Quit
		}
		if m.editing {
			switch key {
			case "esc":
				m.editing = false
				m.query = ""
			case "enter":
				m.editing = false
			case "backspace", "ctrl+h":
				r := []rune(m.query)
				if len(r) > 0 {
					m.query = string(r[:len(r)-1])
				}
			case "ctrl+u":
				m.query = ""
			default:
				if msg.Type == tea.KeyRunes {
					m.query += string(msg.Runes)
				}
			}
			m.selected, m.top = 0, 0
			m.clamp()
			return m, nil
		}
		switch key {
		case "/":
			if m.selectedMap == "" {
				m.editing = true
			}
		case "esc", "backspace":
			if m.selectedMap != "" {
				if m.cancelLoad != nil {
					m.cancelLoad()
				}
				m.request++
				m.selectedMap = ""
				m.entries = nil
				m.err = nil
				m.loading = false
				m.selected, m.top, m.x = m.nameSelected, m.nameTop, 0
			} else {
				m.query = ""
				m.selected, m.top = 0, 0
			}
		case "enter":
			if m.selectedMap == "" && !m.loading && m.count() > 0 {
				m.nameSelected, m.nameTop = m.selected, m.top
				m.selectedMap = m.visibleNames()[m.selected]
				m.selected, m.top, m.x = 0, 0, 0
				cmd = m.fetch()
			}
		case "r":
			if !m.loading {
				cmd = m.fetch()
			}
		case "up", "k":
			m.selected--
		case "down", "j":
			m.selected++
		case "pgup":
			m.selected -= max(1, m.bodyHeight())
		case "pgdown":
			m.selected += max(1, m.bodyHeight())
		case "home", "g":
			m.selected = 0
		case "end", "G":
			m.selected = m.count() - 1
		case "left", "h":
			m.x -= 8
		case "right", "l":
			m.x += 8
		}
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.selected -= 3
			case tea.MouseButtonWheelDown:
				m.selected += 3
			case tea.MouseButtonLeft:
				if msg.Y >= 3 && msg.Y < 3+m.bodyHeight() && m.top+msg.Y-3 < m.count() {
					m.selected = m.top + msg.Y - 3
				}
			}
		}
	}
	m.clamp()
	return m, cmd
}
func (m mapBrowserModel) View() string {
	title := "Maps"
	columns := "NAME"
	help := "↑/↓ select · Enter versions · / filter · r refresh · q quit"
	if m.selectedMap != "" {
		title = "Map: " + m.selectedMap
		columns = "VERSION           SIZE (bytes)  REGISTERED AT         SHA256"
		help = "↑/↓ scroll · ←/→ pan · Esc maps · r refresh · q quit"
	}
	status := fmt.Sprintf("%d entries", m.count())
	if m.selectedMap == "" && (m.query != "" || m.editing) {
		status += " | Filter: " + m.query
	}
	if m.editing {
		status += " | Enter apply / Esc clear"
	}
	if m.loading {
		status = "Loading..."
	}
	if m.err != nil {
		status = "Error: " + m.err.Error() + " (r retry)"
	}
	lines := []string{title, status, "  " + columns}
	names := m.visibleNames()
	for i := 0; i < m.bodyHeight(); i++ {
		index := m.top + i
		line := ""
		if index < m.count() && !m.loading && m.err == nil {
			if m.selectedMap == "" {
				line = names[index]
			} else {
				entry := m.entries[index]
				line = fmt.Sprintf("%-16s %12d  %-20s  %s", entry.Version, entry.SizeBytes, entry.RegisteredAt.Format("2006-01-02T15:04:05Z"), entry.SHA256)
			}
			marker := "  "
			if index == m.selected {
				marker = "> "
			}
			line = marker + ansi.Cut(screenText(line), m.x, m.x+max(0, m.width-2))
		}
		if i == 0 && m.count() == 0 && !m.loading && m.err == nil {
			line = "No published maps or versions found."
		}
		lines = append(lines, line)
	}
	lines = append(lines, fmt.Sprintf("%d/%d", min(m.count(), m.selected+1), m.count()), help)
	if m.height <= 0 || m.width <= 0 {
		return ""
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(screenText(line), m.width, "")
	}
	return strings.Join(lines, "\n")
}
