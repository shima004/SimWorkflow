package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/paginator"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"simworkflow/internal/argo"
)

type experimentListModel struct {
	items         []argo.Overview
	pager         paginator.Model
	width, height int
	selected      int
	openName      string
	notice        string
}

func newExperimentListModel(items []argo.Overview) experimentListModel {
	m := experimentListModel{items: items, pager: paginator.New(), width: 80, height: 24}
	m.resize(80, 24)
	return m
}

func browseExperiments(ctx context.Context, items []argo.Overview, in io.Reader, out io.Writer, openWatch func(string) error) error {
	m := newExperimentListModel(items)
	for {
		final, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen()).Run()
		if err != nil {
			return err
		}
		m = final.(experimentListModel)
		if m.openName == "" {
			return nil
		}
		name := m.openName
		m.openName, m.notice = "", ""
		// Fully stop the list program before opening watch. Each watch owns its
		// input loop and cancels its refresh tasks when it closes.
		if err := openWatch(name); err != nil {
			m.notice = experimentListCell(err.Error())
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (m experimentListModel) Init() tea.Cmd { return nil }

func (m *experimentListModel) resize(width, height int) {
	m.width, m.height = max(1, width), max(1, height)
	// Reserve one line each for the title, column headings and page controls.
	m.pager.PerPage = max(1, m.height-3)
	m.pager.SetTotalPages(len(m.items))
	m.moveSelection(0)
}

func (m *experimentListModel) moveSelection(delta int) {
	m.selected = min(max(0, m.selected+delta), max(0, len(m.items)-1))
	m.pager.Page = m.selected / m.pager.PerPage
}

func (m *experimentListModel) page(delta int) {
	offset := m.selected % m.pager.PerPage
	if delta < 0 {
		m.pager.PrevPage()
	} else {
		m.pager.NextPage()
	}
	m.selected = min(m.pager.Page*m.pager.PerPage+offset, max(0, len(m.items)-1))
}

func (m experimentListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "home", "g":
			m.moveSelection(-len(m.items))
		case "end", "G":
			m.moveSelection(len(m.items))
		case "up", "k":
			m.moveSelection(-1)
		case "down", "j":
			m.moveSelection(1)
		case "left", "h", "pgup", "b":
			m.page(-1)
		case "right", "l", "pgdown", " ":
			m.page(1)
		case "enter":
			if len(m.items) > 0 {
				m.openName = m.items[m.selected].Name
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m experimentListModel) View() string {
	start, end := m.pager.GetSliceBounds(len(m.items))
	footer := fmt.Sprintf("Page %s | %d-%d/%d | ↑↓ select  Enter status  ←→ page  q quit", m.pager.View(), min(start+1, len(m.items)), end, len(m.items))
	if m.height < 4 {
		return ansi.Truncate(footer, m.width, "…")
	}
	var table strings.Builder
	_ = writeExperimentList(&table, m.items[start:end])
	title := fmt.Sprintf("SimWorkflow experiments | %d total", len(m.items))
	if m.notice != "" {
		title = "Status: " + m.notice
	}
	lines := []string{title}
	for i, row := range strings.Split(strings.TrimSuffix(table.String(), "\n"), "\n") {
		marker := "  "
		if i > 0 && start+i-1 == m.selected {
			marker = "> "
		}
		lines = append(lines, marker+row)
	}
	lines = append(lines, footer)
	for i := range lines {
		// Prevent wrapping so each experiment always consumes one terminal row.
		lines[i] = ansi.Truncate(lines[i], m.width, "…")
	}
	return strings.Join(lines, "\n")
}
