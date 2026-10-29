package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"simworkflow/internal/argo"
	"simworkflow/internal/worker"
)

func TestMemoryColumnsAndLiveDetails(t *testing.T) {
	now := time.Now().UTC()
	limit := uint64(2 << 30)
	row := runProgressRow{runID: "run-one", phase: "Running", memory: map[string]worker.MemoryUsage{
		"server":             {UsageBytes: 1 << 30, PeakBytes: 1500 << 20, LimitBytes: &limit, UpdatedAt: now},
		"agent-fire-brigade": {UsageBytes: 500 << 20, UpdatedAt: now.Add(-time.Minute)},
		"agent-police-force": {UsageBytes: 100 << 20, UpdatedAt: now, Finished: true},
	}}
	var out bytes.Buffer
	if _, err := writeProgressRows(&out, []runProgressRow{row}, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MEM SERVER", "MEM FIRE", "MEM POLICE", "MEM AMB", "1.00GiB", "500.0MiB~", "100.0MiB*"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	if got := memoryCell(row, "agent-ambulance-team", now); got != "-" {
		t.Fatal(got)
	}
	if got := memoryCell(row, "server", now.Add(time.Minute)); got != "1.00GiB~" {
		t.Fatal(got)
	}
	m := testProgressModel()
	m.details = []string{"original settings"}
	m.detailRunID = row.runID
	m.now = now
	m.frame.progressRows = []runProgressRow{row}
	content := strings.Join(m.bodyRows(), "\n")
	for _, want := range []string{"1.00GiB / 2.00GiB", "peak 1.46GiB", "stale", "last", "unavailable", "original settings"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q: %s", want, content)
		}
	}
	row.memory["server"] = worker.MemoryUsage{UsageBytes: 1500 << 20, UpdatedAt: now}
	frame := m.frame
	frame.progressRows = []runProgressRow{row}
	m, _ = updateProgress(m, frame)
	if !strings.Contains(strings.Join(m.bodyRows(), "\n"), "1.46GiB /") {
		t.Fatal("open details did not refresh")
	}
	m, _ = updateProgress(m, tea.WindowSizeMsg{Width: 20, Height: 8})
	if len(strings.Split(m.View(), "\n")) > 8 {
		t.Fatal("memory details overflow")
	}
}

func TestReadProgressMemoryRejectsPreviousAttempt(t *testing.T) {
	plan := detailsPlan(t)
	run := plan.Runs[0]
	snapshot := progressSnapshot(plan)
	now := time.Now().UTC()
	snapshot.Status.Nodes = map[string]argo.Node{"sim": {Type: "Pod", TemplateName: run.ID + "-simulation", Phase: "Running", StartedAt: now}}
	progress := worker.Progress{ExecutionID: snapshot.Metadata.Annotations["simworkflow.io/execution-id"], RunID: run.ID, Stage: worker.Simulation, UpdatedAt: now, Memory: map[string]worker.MemoryUsage{"server": {UsageBytes: 123, UpdatedAt: now}}}
	store := progressTestStore(func(r *http.Request) (*http.Response, error) { return progressResponse(r, progress, 200), nil })
	row := readRunProgress(context.Background(), store, snapshot, run)
	if row.memory["server"].UsageBytes != 123 {
		t.Fatal("valid memory missing")
	}
	for _, mutate := range []func(){func() { progress.UpdatedAt = now.Add(-time.Second) }, func() { progress.UpdatedAt = now; progress.ExecutionID = "other" }, func() {
		progress.ExecutionID = snapshot.Metadata.Annotations["simworkflow.io/execution-id"]
		progress.Memory = nil
	}} {
		mutate()
		row = readRunProgress(context.Background(), store, snapshot, run)
		if len(row.memory) != 0 {
			t.Fatal("old or unavailable metrics accepted")
		}
	}
}
