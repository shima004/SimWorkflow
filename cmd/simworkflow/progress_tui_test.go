package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func testProgressModel() progressModel {
	m := progressModel{width: 60, height: 10, fetch: func() tea.Msg { return nil }}
	m.frame = progressFrame{name: "test-workflow", phase: "Running", columns: "RUN     " + strings.Repeat("COLUMN ", 20)}
	for i := 0; i < 40; i++ {
		m.frame.rows = append(m.frame.rows, fmt.Sprintf("run-%02d  %s", i, strings.Repeat("日本語", 40)))
	}
	return m
}

func updateProgress(m progressModel, msg tea.Msg) (progressModel, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(progressModel), cmd
}

func TestProgressScrollPreservedAcrossRefreshAndResize(t *testing.T) {
	m := testProgressModel()
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyPgDown})
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.y != 6 || m.x != 8 {
		t.Fatalf("scroll offsets: %d, %d", m.y, m.x)
	}
	frame := m.frame
	frame.rows = append([]string(nil), frame.rows...)
	frame.rows[6] = "updated run-06 " + strings.Repeat("data ", 40)
	m, cmd := updateProgress(m, frame)
	if cmd == nil || m.y != 6 || m.x != 8 || !strings.Contains(m.View(), "run-06") || strings.Contains(m.View(), "run-00") {
		t.Fatalf("refresh lost scroll or failed to update: %s", m.View())
	}
	if !strings.Contains(m.View(), "Workflow: test-workflow") || !strings.Contains(m.View(), "COLUMN") {
		t.Fatal("workflow and table headings must remain visible")
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.y != 34 {
		t.Fatalf("end: %d", m.y)
	}
	m, _ = updateProgress(m, tea.WindowSizeMsg{Width: 2000, Height: 80})
	if m.x != 0 || m.y != 0 {
		t.Fatal("resize must clamp scroll offsets")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 20, Height: 8}, {Width: 1, Height: 1}, {Width: 80, Height: 24}} {
		m, _ = updateProgress(m, size)
		lines := strings.Split(m.View(), "\n")
		if len(lines) > size.Height {
			t.Fatalf("view exceeds terminal height: %d", len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("view exceeds terminal width: %q", line)
			}
		}
	}
}

func TestProgressMouseAndPageNavigation(t *testing.T) {
	m := testProgressModel()
	m, _ = updateProgress(m, tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.y != 3 {
		t.Fatal("wheel must scroll")
	}
	m, _ = updateProgress(m, tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.y != 0 {
		t.Fatal("wheel up must scroll back")
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyPgDown})
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.y != 0 {
		t.Fatal("page up must scroll back")
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyEnd})
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyHome})
	if m.y != 0 {
		t.Fatal("home must return to top")
	}
}

func TestProgressRefreshErrorsAndCompletion(t *testing.T) {
	for _, phase := range []string{"Succeeded", "Failed", "Error"} {
		m := testProgressModel()
		m, cmd := updateProgress(m, progressFrame{err: errors.New("connection failed")})
		if cmd == nil || m.frame.phase != "Running" || !strings.Contains(m.View(), "retrying") {
			t.Fatal("transient failures must retain data and schedule a retry")
		}
		frame := m.frame
		frame.phase = phase
		m, cmd = updateProgress(m, frame)
		if cmd != nil || !m.done || m.refreshErr != nil || !strings.Contains(m.View(), "Finished") {
			t.Fatal("completed workflow must stay open, without further refresh")
		}
		m, cmd = updateProgress(m, progressTick{})
		if cmd != nil {
			t.Fatal("completed workflow must not fetch again")
		}
		_, cmd = updateProgress(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("q must close the screen")
		}
	}
}

func TestProgressCanQuitDuringFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	m := testProgressModel()
	m.fetch = func() tea.Msg {
		close(started)
		<-ctx.Done()
		return progressFrame{err: ctx.Err()}
	}
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	finished := make(chan error, 1)
	go func() { _, err := p.Run(); finished <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("fetch did not start")
	}
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("network fetch blocked keyboard input")
	}
}

func TestScreenTextRemovesTerminalControls(t *testing.T) {
	if got := screenText("name\x1b\x07\r\t\ntext"); got != "name\ntext" {
		t.Fatalf("unexpected sanitized text: %q", got)
	}
}
