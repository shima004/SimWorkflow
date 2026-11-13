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
}

func newExperimentListModel(items []argo.Overview) experimentListModel {
	m := experimentListModel{items: items, pager: paginator.New(), width: 80, height: 24}
	m.resize(80, 24)
	return m
}

func browseExperiments(ctx context.Context, items []argo.Overview, in io.Reader, out io.Writer) error {
	_, err := tea.NewProgram(newExperimentListModel(items), tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen()).Run()
	return err
}

func (m experimentListModel) Init() tea.Cmd { return nil }

func (m *experimentListModel) resize(width, height int) {
	// Keep the first visible experiment on screen when the page size changes.
	first := m.pager.Page * m.pager.PerPage
	m.width, m.height = max(1, width), max(1, height)
	// Reserve one line each for the title, column headings and page controls.
	m.pager.PerPage = max(1, m.height-3)
	m.pager.SetTotalPages(len(m.items))
	m.pager.Page = min(first/m.pager.PerPage, m.pager.TotalPages-1)
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
			m.pager.Page = 0
		case "end", "G":
			m.pager.Page = m.pager.TotalPages - 1
		case "up", "k", "b":
			m.pager.PrevPage()
		case "down", "j", " ", "enter":
			m.pager.NextPage()
		default:
			var cmd tea.Cmd
			m.pager, cmd = m.pager.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m experimentListModel) View() string {
	start, end := m.pager.GetSliceBounds(len(m.items))
	footer := fmt.Sprintf("Page %s | %d-%d/%d | ←/→ PgUp/PgDn  Home/End  q quit", m.pager.View(), min(start+1, len(m.items)), end, len(m.items))
	if m.height < 4 {
		return ansi.Truncate(footer, m.width, "…")
	}
	var table strings.Builder
	_ = writeExperimentList(&table, m.items[start:end])
	lines := []string{fmt.Sprintf("SimWorkflow experiments | %d total", len(m.items))}
	lines = append(lines, strings.Split(strings.TrimSuffix(table.String(), "\n"), "\n")...)
	lines = append(lines, footer)
	for i := range lines {
		// Prevent wrapping so each experiment always consumes one terminal row.
		lines[i] = ansi.Truncate(lines[i], m.width, "…")
	}
	return strings.Join(lines, "\n")
}
