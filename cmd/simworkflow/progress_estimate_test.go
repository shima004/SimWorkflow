package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

func observedRun(base time.Time) runProgressRow {
	return runProgressRow{runID: "active", stage: worker.Simulation, phase: "Running", startedAt: base, progressStartedAt: base, progressUpdatedAt: base.Add(10 * time.Second), completedSteps: 10, totalSteps: 100, precomputeStartedAt: base.Add(-20 * time.Second), precomputeFinishedAt: base.Add(-10 * time.Second)}
}

func TestEstimateUsesStepDeltasAndObservedConcurrency(t *testing.T) {
	base := time.Unix(1000, 0)
	e := newRunEstimator()
	rows := []runProgressRow{observedRun(base), {runID: "queued", phase: "Pending", totalSteps: 100}}
	plan := &experiment.Plan{MaxParallelRuns: 4}
	if first := e.observe(rows, plan, "Running", base.Add(10*time.Second)); first.known {
		t.Fatal("estimated before observing a step delta")
	}
	rows[0].completedSteps = 20
	rows[0].progressUpdatedAt = base.Add(20 * time.Second)
	got := e.observe(rows, plan, "Running", base.Add(20*time.Second))
	// 80s of active simulation + (10s precompute + 100s simulation) queued.
	if !got.known || got.remaining != 190*time.Second || got.parallel != 1 || got.samples != 1 {
		t.Fatalf("unexpected estimate: %+v", got)
	}
	text := strings.Join(progressOverview(rows, got, "Running", base, base.Add(25*time.Second), 0), "\n")
	if !strings.Contains(text, "remaining ~3m5s") || !strings.Contains(text, "total ~3m30s") {
		t.Fatal("countdown/total duration incorrect", text)
	}
	if stale := strings.Join(progressOverview(rows, got, "Running", base, base.Add(60*time.Second), 0), "\n"); !strings.Contains(stale, "status data is stale") {
		t.Fatal("stale frame kept counting down", stale)
	}
}

func TestEstimateDoesNotCountHeartbeatAsStepAdvance(t *testing.T) {
	base := time.Unix(1000, 0)
	e := newRunEstimator()
	plan := &experiment.Plan{MaxParallelRuns: 1}
	r := observedRun(base)
	r.completedSteps = 0
	r.progressUpdatedAt = base
	e.observe([]runProgressRow{r}, plan, "Running", base)
	r.progressUpdatedAt = base.Add(5 * time.Second)
	e.observe([]runProgressRow{r}, plan, "Running", r.progressUpdatedAt)
	r.completedSteps = 1
	r.progressUpdatedAt = base.Add(10 * time.Second)
	e.observe([]runProgressRow{r}, plan, "Running", r.progressUpdatedAt)
	if e.samples[r.runID].secondsPerStep != 10 {
		t.Fatal("heartbeat shortened the measured step duration")
	}
	r.progressUpdatedAt = base.Add(15 * time.Second)
	e.observe([]runProgressRow{r}, plan, "Running", r.progressUpdatedAt)
	r.completedSteps = 2
	r.progressUpdatedAt = base.Add(20 * time.Second)
	e.observe([]runProgressRow{r}, plan, "Running", r.progressUpdatedAt)
	if e.samples[r.runID].secondsPerStep != 10 {
		t.Fatal("plateau distorted step duration")
	}
	r.progressUpdatedAt = base.Add(80 * time.Second)
	got := e.observe([]runProgressRow{r}, plan, "Running", r.progressUpdatedAt)
	if got.known || !strings.Contains(got.reason, "stalled") {
		t.Fatal("stalled simulation retained its old rate", got)
	}
	// A new Argo Pod attempt must not inherit speed observations from the old one.
	r.startedAt = base.Add(81 * time.Second)
	r.completedSteps = 0
	r.progressUpdatedAt = r.startedAt
	got = e.observe([]runProgressRow{r}, plan, "Running", r.progressUpdatedAt)
	if got.known || e.samples[r.runID].secondsPerStep != 0 {
		t.Fatal("retry inherited previous attempt's measurements")
	}
}

func TestEstimateIncludesPrecomputeAndExcludesFailedRuns(t *testing.T) {
	base := time.Unix(1000, 0)
	now := base.Add(100 * time.Second)
	rows := []runProgressRow{
		{runID: "finished", stage: worker.Simulation, phase: "Succeeded", startedAt: base, finishedAt: now, totalSteps: 100, completedSteps: 100, precomputeStartedAt: base.Add(-20 * time.Second), precomputeFinishedAt: base},
		{runID: "precompute", stage: worker.Precompute, phase: "Running", startedAt: now.Add(-5 * time.Second), totalSteps: 100},
		{runID: "awaiting-simulation", stage: worker.Precompute, phase: "Succeeded", totalSteps: 100, precomputeStartedAt: base.Add(-20 * time.Second), precomputeFinishedAt: base},
		{runID: "failed", phase: "Failed"}, {runID: "not-run", phase: "Not run"},
	}
	got := newRunEstimator().observe(rows, &experiment.Plan{MaxParallelRuns: 8}, "Running", now)
	// Successful precompute is not a completed experiment: its simulation remains.
	if !got.known || got.remaining != 215*time.Second {
		t.Fatalf("stage accounting is incorrect: %+v", got)
	}
	rows[1].totalSteps = 0
	if got := newRunEstimator().observe(rows, &experiment.Plan{MaxParallelRuns: 8}, "Running", now); got.known || got.reason != "total steps unavailable" {
		t.Fatal("missing total fabricated a duration", got)
	}
	if got := newRunEstimator().observe(rows, nil, "Failed", now); !got.known || got.remaining != 0 {
		t.Fatal("finished workflow retained a forecast")
	}
}

func TestEstimateDoesNotForecastResultUploadAfterLastStep(t *testing.T) {
	base := time.Unix(1000, 0)
	r := observedRun(base)
	r.completedSteps = r.totalSteps
	got := newRunEstimator().observe([]runProgressRow{r}, &experiment.Plan{MaxParallelRuns: 1}, "Running", base.Add(time.Minute))
	if got.known || got.reason != "finishing and saving results" {
		t.Fatal("completed steps fabricated an upload ETA", got)
	}
}

func TestStateCountsAndOverviewStayVisibleWhileScrolling(t *testing.T) {
	m := testProgressModel()
	m.width = 80
	m.height = 12
	m.now = time.Unix(1000, 0)
	for i, state := range []string{"Pending", "Pending", "Running", "Succeeded", "Succeeded", "Failed", "Error", "Not run"} {
		m.frame.progressRows = append(m.frame.progressRows, runProgressRow{runID: string(rune('a' + i)), phase: state})
	}
	m.frame.updateRows(m.now)
	text := m.View()
	for _, part := range []string{"STATE (total 8)", "Pending:2", "Running:1", "Succeeded:2", "Failed:1", "Error:1", "Not run:1", "Estimate: --"} {
		if !strings.Contains(text, part) {
			t.Fatal("missing overview field", part, text)
		}
	}
	firstRow := 3 + len(m.overviewLines())
	m, _ = updateProgress(m, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 4, Y: firstRow + 1})
	if m.selected != 1 {
		t.Fatal("new overview offset broke mouse selection")
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.y == 0 {
		t.Fatal("test did not scroll")
	}
	if !strings.Contains(m.View(), "STATE (total 8)") {
		t.Fatal("overview scrolled with the table")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 12}, {Width: 1, Height: 1}, {Width: 100, Height: 20}} {
		m, _ = updateProgress(m, size)
		lines := strings.Split(m.View(), "\n")
		if len(lines) > size.Height {
			t.Fatal("overview exceeded terminal height")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatal("overview exceeded terminal width")
			}
		}
	}
}
