package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"simworkflow/internal/argo"
	"simworkflow/internal/worker"
)

func TestFailureDetailsFollowSelectedAttemptAndSummary(t *testing.T) {
	plan := detailsPlan(t)
	plan.Runs = plan.Runs[:1]
	run := plan.Runs[0]
	start := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	var snapshot argo.Snapshot
	snapshot.Metadata.Name = "failure-test"
	snapshot.Metadata.Annotations = map[string]string{"simworkflow.io/result-prefix": "experiments/test", "simworkflow.io/execution-id": "test"}
	snapshot.Status.Nodes = map[string]argo.Node{
		"old": {TemplateName: run.ID + "-simulation", Type: "Pod", DisplayName: "simulation(0)", Phase: "Failed", Message: "old Pod failure"},
		"new": {TemplateName: run.ID + "-simulation", Type: "Pod", DisplayName: "simulation(1)", Phase: "Error", StartedAt: start, Message: "agent-police-force: OOMKilled; see https://example.org/file?X-Amz-Signature=private"},
	}
	summary := worker.Summary{Run: run, ExecutionID: "test", Stage: worker.Simulation, Phase: "Failed", FinishedAt: start.Add(-time.Second), Agents: map[string]worker.State{
		"policeForce": {Error: "police worker reported a failure"},
	}}
	store := progressTestStore(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/summary.json") {
			return progressResponse(r, summary, 200), nil
		}
		return progressResponse(r, nil, 404), nil
	})
	model := testProgressModel()
	model.frame.plan, model.frame.runs = plan, plan.Runs
	model.frame.progressRows = readPlannedRunProgress(context.Background(), store, &snapshot, plan)
	model.frame.updateRows(start)
	model, _ = updateProgress(model, tea.KeyMsg{Type: tea.KeyEnter})
	body := strings.Join(model.bodyRows(), "\n")
	for _, want := range []string{"Stage: simulation", "OOMKilled", "[URL hidden]"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q: %s", want, body)
		}
	}
	for _, absent := range []string{"old Pod failure", "private", "police worker reported"} {
		if strings.Contains(body, absent) {
			t.Errorf("unexpected %q: %s", absent, body)
		}
	}

	// Open details receive a newer report on the next refresh.
	summary.FinishedAt = start.Add(time.Second)
	frame := model.frame
	frame.progressRows = readPlannedRunProgress(context.Background(), store, &snapshot, plan)
	model, _ = updateProgress(model, frame)
	if body := strings.Join(model.bodyRows(), "\n"); !strings.Contains(body, "agent-police-force: police worker reported a failure") {
		t.Fatal(body)
	}

	node := snapshot.Status.Nodes["new"]
	node.Phase = "Succeeded"
	snapshot.Status.Nodes["new"] = node
	frame.progressRows = readPlannedRunProgress(context.Background(), store, &snapshot, plan)
	model, _ = updateProgress(model, frame)
	if body := strings.Join(model.bodyRows(), "\n"); strings.Contains(body, "OOMKilled") {
		t.Fatal("a successful retry still shows the old failure")
	}
}

func TestFailureBeforePodCreationIsShown(t *testing.T) {
	plan := detailsPlan(t)
	plan.Runs = plan.Runs[:1]
	var snapshot argo.Snapshot
	snapshot.Metadata.Name = "failure-test"
	snapshot.Metadata.Annotations = map[string]string{"simworkflow.io/run-sequences": "v1"}
	snapshot.Status.Phase = "Error"
	attachSequenceNodes(&snapshot, plan)
	root := snapshot.RunNodeNames[plan.Runs[0].ID]
	snapshot.Status.Nodes = map[string]argo.Node{
		"stage": {Name: snapshot.Metadata.Name + "." + root + "[0].precompute", Type: "Retry", Phase: "Error", Message: "template evaluation failed"},
	}
	store := progressTestStore(func(r *http.Request) (*http.Response, error) {
		return progressResponse(r, nil, 404), nil
	})
	row := readPlannedRunProgress(context.Background(), store, &snapshot, plan)[0]
	body := strings.Join(failureDetails(row), "\n")
	if row.phase != "Error" || row.stage != worker.Precompute || !strings.Contains(body, "template evaluation failed") {
		t.Fatal(row, body)
	}
}
