package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"simworkflow/internal/mapcatalog"
)

func mapKey(m mapBrowserModel, key tea.KeyMsg) (mapBrowserModel, tea.Cmd) {
	next, cmd := m.Update(key)
	return next.(mapBrowserModel), cmd
}
func mapRune(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func TestMapBrowserLoadsVersionsOnlyAfterSelection(t *testing.T) {
	namesCalls, versionCalls := 0, 0
	m := mapBrowserModel{ctx: context.Background(), width: 80, height: 12, loading: true,
		loadNames: func(context.Context) ([]string, error) { namesCalls++; return []string{"aichi", "kobe", "vc"}, nil },
		loadVersions: func(_ context.Context, name string) ([]mapcatalog.Metadata, error) {
			versionCalls++
			if name != "kobe" {
				t.Fatal(name)
			}
			return []mapcatalog.Metadata{{Name: name, Version: "type0"}}, nil
		},
	}
	next, _ := m.Update(m.Init()())
	m = next.(mapBrowserModel)
	if namesCalls != 1 || versionCalls != 0 || m.loading {
		t.Fatal("versions fetched before selection")
	}
	m, _ = mapKey(m, tea.KeyMsg{Type: tea.KeyDown})
	var cmd tea.Cmd
	m, cmd = mapKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.loading || versionCalls != 0 || m.selectedMap != "kobe" {
		t.Fatal("selection did not start lazy load")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(mapBrowserModel)
	if versionCalls != 1 || !strings.Contains(m.View(), "type0") {
		t.Fatal(m.View())
	}
	m, _ = mapKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.selectedMap != "" || m.selected != 1 || namesCalls != 1 {
		t.Fatal("back lost name selection or fetched again")
	}
	next, _ = m.Update(msg)
	m = next.(mapBrowserModel)
	if m.selectedMap != "" || len(m.entries) != 0 {
		t.Fatal("late version response changed names screen")
	}
}
func TestMapBrowserFilterScrollRetryAndResize(t *testing.T) {
	m := mapBrowserModel{ctx: context.Background(), width: 70, height: 9, names: []string{"aichi", "kobe", "vc"}, loadNames: func(context.Context) ([]string, error) { return nil, errors.New("offline") }}
	m, _ = mapKey(m, mapRune("/"))
	m, _ = mapKey(m, mapRune("ko"))
	m, _ = mapKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.count() != 1 || m.visibleNames()[0] != "kobe" {
		t.Fatal("filter failed")
	}
	m, _ = mapKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.count() != 3 {
		t.Fatal("filter not cleared")
	}
	m, _ = mapKey(m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.selected != 2 {
		t.Fatal("end did not select last map")
	}
	m, cmd := mapKey(m, mapRune("r"))
	next, _ := m.Update(cmd())
	m = next.(mapBrowserModel)
	if m.err == nil || !strings.Contains(m.View(), "retry") {
		t.Fatal("load error not retryable")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 1, Height: 1}, {Width: 20, Height: 8}, {Width: 80, Height: 24}} {
		next, _ := m.Update(size)
		m = next.(mapBrowserModel)
		lines := strings.Split(m.View(), "\n")
		if len(lines) > size.Height {
			t.Fatal("height overflow")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatal("width overflow", line)
			}
		}
	}
	_, cmd = mapKey(m, mapRune("q"))
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
}
