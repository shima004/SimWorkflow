package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"simworkflow/internal/argo"
)

func experimentPageKey(m experimentListModel, key tea.KeyType) experimentListModel {
	next, _ := m.Update(tea.KeyMsg{Type: key})
	return next.(experimentListModel)
}

func TestExperimentListPagesFollowTerminalHeight(t *testing.T) {
	items := make([]argo.Overview, 60)
	for i := range items {
		items[i] = argo.Overview{Name: fmt.Sprintf("experiment-%03d", i), Phase: "Succeeded"}
	}
	m := newExperimentListModel(items)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	m = next.(experimentListModel)
	if m.pager.PerPage != 9 || m.pager.TotalPages != 7 {
		t.Fatal(m.pager)
	}
	first := m.View()
	if len(strings.Split(first, "\n")) != 12 || !strings.Contains(first, "experiment-008") || strings.Contains(first, "experiment-009") {
		t.Fatal(first)
	}
	m = experimentPageKey(m, tea.KeyPgDown)
	if view := m.View(); !strings.Contains(view, "Page 2/7") || !strings.Contains(view, "experiment-009") || strings.Contains(view, "experiment-008") {
		t.Fatal(view)
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 8})
	m = next.(experimentListModel)
	start, end := m.pager.GetSliceBounds(len(items))
	if m.pager.PerPage != 5 || start > 9 || end <= 9 || len(strings.Split(m.View(), "\n")) > 8 {
		t.Fatal(m.pager, m.View())
	}
	m = experimentPageKey(m, tea.KeyEnd)
	m = experimentPageKey(m, tea.KeyRight)
	if view := m.View(); !strings.Contains(view, "experiment-059") || !strings.Contains(view, "Page 12/12") {
		t.Fatal(view)
	}
	m = experimentPageKey(m, tea.KeyHome)
	m = experimentPageKey(m, tea.KeyPgUp)
	if m.pager.Page != 0 {
		t.Fatal("paged before first page")
	}
	_, quit := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if quit == nil {
		t.Fatal("quit key did not exit")
	}
}

func TestExperimentListDoesNotWrapOrRenderRemoteControlCharacters(t *testing.T) {
	m := newExperimentListModel([]argo.Overview{{Name: "map\n\x1b[31m" + strings.Repeat("日本語", 50), Phase: "Running"}})
	for _, height := range []int{1, 2, 3, 4, 5, 24} {
		m.resize(20, height)
		view := m.View()
		if strings.Contains(view, "\x1b") || len(strings.Split(view, "\n")) > height {
			t.Fatal(height, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > 20 {
				t.Fatal("line wraps", line)
			}
		}
	}
}
