package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
)

func detailsPlan(t *testing.T) *experiment.Plan {
	t.Helper()
	e, err := experiment.ReadFile("../../examples/experiments/module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := experiment.BuildPlan(e, 100)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunSelectionDetailsAndReturn(t *testing.T) {
	p := detailsPlan(t)
	m := testProgressModel()
	m.frame.plan, m.frame.runs = p, p.Runs
	m.frame.rows = m.frame.rows[:len(p.Runs)]
	for i := 0; i < 7; i++ {
		m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.selected != 7 || m.y != 2 || !strings.Contains(m.View(), "> run-07") {
		t.Fatalf("selection must follow keys and remain visible: %s", m.View())
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyRight})
	x, y := m.x, m.y
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.details == nil || m.detailRunID != p.Runs[7].ID || m.x != 0 || m.y != 0 {
		t.Fatal("Enter must open selected run's settings")
	}
	content := strings.Join(m.details, "\n")
	if !strings.Contains(content, p.Runs[7].Configuration.ID) {
		t.Fatal("details must describe the selected configuration")
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.y == 0 || m.selected != 7 {
		t.Fatal("details scrolling must not change selected run")
	}
	detailY := m.y
	m, _ = updateProgress(m, m.frame)
	if m.details == nil || m.y != detailY {
		t.Fatal("refresh must retain the open details and scroll position")
	}
	m, cmd := updateProgress(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || m.details != nil || m.selected != 7 || m.x != x || m.y != y {
		t.Fatal("Esc must restore list selection and offsets")
	}
	m, _ = updateProgress(m, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 4, Y: 3})
	if m.selected != y {
		t.Fatal("click must select the row under the pointer")
	}
}

func TestRunDetailsIncludeResolvedSettingsAndEffectiveResources(t *testing.T) {
	p := detailsPlan(t)
	run := p.Runs[0]
	run.Map.Resolved = &experiment.ResolvedMap{Name: "registered-map", Version: "v2", Source: "s3://maps/pinned.zip", SHA256: strings.Repeat("a", 64)}
	run.Configuration.ResourcesByRole = map[string]experiment.Resources{"policeForce": {CPU: "3", Memory: "7Gi"}}
	steps := 123
	run.Simulator.Parameters = &experiment.SimulatorParameters{Timesteps: &steps}
	content := strings.Join(runDetails(p, run), "\n")
	for _, want := range []string{run.ID, run.Configuration.ID, run.Configuration.Image, run.Simulator.Image, "registered-map", "Resolved version: v2", "s3://maps/pinned.zip", strings.Repeat("a", 64), "Timesteps: 123", "policeForce: CPU 3 cores / Memory 7Gi", "Max parallel runs:", "Timeout per run:", "Collect:"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing detail %q", want)
		}
	}
	for key, value := range run.Configuration.Modules {
		if !strings.Contains(content, key+"\n    "+value) {
			t.Errorf("missing expanded module choice %s", key)
		}
	}
	r := run.Configuration.ResourcesForRole("fireBrigade")
	if !strings.Contains(content, "fireBrigade: CPU "+string(r.CPU)+" cores / Memory "+r.Memory) {
		t.Fatal("inherited per-role resources must be shown")
	}
}

func TestProgressSelectionSurvivesReorderedRuns(t *testing.T) {
	p := detailsPlan(t)
	m := testProgressModel()
	m.frame.plan, m.frame.runs = p, p.Runs
	m.frame.rows = m.frame.rows[:len(p.Runs)]
	f := m.frame
	f.runs = append([]experiment.Run(nil), f.runs...)
	f.rows = append([]string(nil), f.rows...)
	f.runs[0], f.runs[8] = f.runs[8], f.runs[0]
	f.rows[0], f.rows[8] = f.rows[8], f.rows[0]
	m, _ = updateProgress(m, f)
	if m.selected != 8 || m.selected < m.y || m.selected >= m.y+m.bodyHeight() {
		t.Fatal("selection must follow run ID across refresh")
	}
}

func TestLoadProgressUsesSamePlanForRowsAndDetails(t *testing.T) {
	p := detailsPlan(t)
	p.Runs = append([]experiment.Run{{ID: "invalid"}}, p.Runs...)
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/workflows/"):
			json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "test", "annotations": map[string]string{"simworkflow.io/result-prefix": "experiments/test"}}, "status": map[string]any{"phase": "Running"}})
		case strings.HasSuffix(r.URL.Path, "/plan.json"):
			reads.Add(1)
			json.NewEncoder(w).Encode(p)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := config.Config{Argo: config.Argo{Server: server.URL, Namespace: "argo"}, S3: config.S3{Endpoint: server.URL, Bucket: "results", Region: "us-east-1", AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true}, HTTPTimeout: time.Second}
	f := loadProgress(context.Background(), c, "test")().(progressFrame)
	if f.err != nil || f.plan == nil || reads.Load() != 1 || len(f.runs) != len(p.Runs)-1 || len(f.rows) != len(f.runs) {
		t.Fatalf("rows and details must use the same single plan read: %+v, reads %d", f, reads.Load())
	}
	for i, run := range f.runs {
		if !strings.HasPrefix(f.rows[i], run.ID) {
			t.Fatal("row/details mismatch")
		}
	}
}

func TestEnterWithoutAvailableSettingsIsSafe(t *testing.T) {
	m := testProgressModel()
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.details != nil {
		t.Fatal("missing plan must not open incorrect details")
	}
}
