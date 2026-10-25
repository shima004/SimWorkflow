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

func TestFinishedTUIKeepsPollingUntilUnavailableResultsRecover(t *testing.T) {
	for _, phase := range []string{"Succeeded", "Failed", "Error"} {
		m := testProgressModel()
		frame := m.frame
		frame.phase, frame.retryProgress = phase, true
		m, cmd := updateProgress(m, frame)
		if !m.done || cmd == nil || !strings.Contains(m.View(), "retrying unavailable") {
			t.Fatal("completed workflow must keep refreshing unavailable results")
		}
		m, cmd = updateProgress(m, progressTick{})
		if cmd == nil {
			t.Fatal("retry tick must fetch again after completion")
		}
		frame.retryProgress = false
		m, cmd = updateProgress(m, frame)
		if cmd != nil || strings.Contains(m.View(), "retrying unavailable") {
			t.Fatal("polling must stop once result reads recover")
		}
		_, cmd = updateProgress(m, progressTick{})
		if cmd != nil {
			t.Fatal("recovered terminal workflow must not fetch again")
		}
	}
}

func TestScreenTextRemovesTerminalControls(t *testing.T) {
	if got := screenText("name\x1b\x07\r\t\ntext"); got != "name\ntext" {
		t.Fatalf("unexpected sanitized text: %q", got)
	}
}

func TestProgressElapsedClock(t *testing.T) {
	start := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	m := testProgressModel()
	m.width = 120
	m.frame.startedAt = start
	m, cmd := updateProgress(m, progressClockTick(start.Add(65*time.Second)))
	if cmd == nil || !strings.Contains(m.View(), "Elapsed: 1m5s") {
		t.Fatal("elapsed time must count from workflow start and schedule the next clock tick")
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyPgDown})
	m, _ = updateProgress(m, progressClockTick(start.Add(66*time.Second)))
	if m.y != 6 || !strings.Contains(m.View(), "Elapsed: 1m6s") {
		t.Fatal("clock updates must preserve scrolling")
	}
	frame := m.frame
	frame.phase, frame.finishedAt = "Succeeded", start.Add(70*time.Second)
	m, _ = updateProgress(m, frame)
	m, cmd = updateProgress(m, progressClockTick(start.Add(time.Hour)))
	if cmd != nil || !strings.Contains(m.View(), "Elapsed: 1m10s") {
		t.Fatal("completed elapsed time must stay fixed without more clock ticks")
	}
}

func TestWorkflowElapsed(t *testing.T) {
	start := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, phase, want      string
		started, finished, now time.Time
	}{
		{"unstarted", "Pending", "-", time.Time{}, time.Time{}, start},
		{"running", "Running", "1h2m3s", start, time.Time{}, start.Add(time.Hour + 2*time.Minute + 3*time.Second + 900*time.Millisecond)},
		{"clock skew", "Running", "0s", start, time.Time{}, start.Add(-time.Second)},
		{"succeeded", "Succeeded", "1m0s", start, start.Add(time.Minute), start.Add(time.Hour)},
		{"failed", "Failed", "1m0s", start, start.Add(time.Minute), start.Add(time.Hour)},
		{"error", "Error", "1m0s", start, start.Add(time.Minute), start.Add(time.Hour)},
		{"missing end", "Succeeded", "-", start, time.Time{}, start.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowElapsed(tc.started, tc.finished, tc.now, tc.phase); got != tc.want {
				t.Fatalf("elapsed = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIndividualRunElapsedTicksWithoutFetching(t *testing.T) {
	start := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	m := testProgressModel()
	m.width = 180
	m.fetch = func() tea.Msg { t.Fatal("clock must not fetch remote progress"); return nil }
	m.frame.progressRows = []runProgressRow{
		{runID: "run-a", stage: "simulation", phase: "Running", startedAt: start},
		{runID: "run-b", stage: "simulation", phase: "Succeeded", startedAt: start, finishedAt: start.Add(20 * time.Second)},
	}
	m.frame.updateRows(start.Add(time.Minute))
	m.selected = 1
	for _, seconds := range []int{61, 62} {
		var cmd tea.Cmd
		m, cmd = updateProgress(m, progressClockTick(start.Add(time.Duration(seconds)*time.Second)))
		if cmd == nil {
			t.Fatal("running workflow must keep updating the clock")
		}
		if _, ok := cmd().(progressClockTick); !ok {
			t.Fatal("must schedule a clock tick, not a remote fetch")
		}
		if m.selected != 1 || !strings.Contains(m.frame.rows[0], fmt.Sprintf("1m%ds", seconds-60)) || !strings.Contains(m.frame.rows[1], "20s") || !strings.Contains(m.View(), "ELAPSED") {
			t.Fatalf("individual elapsed times must tick independently and preserve selection: %s", m.View())
		}
	}
}
