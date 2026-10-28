package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simworkflow/internal/argo"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

func TestWatchEstimatesFromStoredProgressInTUIAndPlainOutput(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/precompute-baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experiment.BuildPlan(e)
	if err != nil {
		t.Fatal(err)
	}
	plan.Runs = plan.Runs[:1]
	plan.TotalRuns = 1
	plan.MaxParallelRuns = 1
	run := plan.Runs[0]
	base := time.Now().UTC().Add(-20 * time.Second)
	execution := strings.Repeat("1", 32)
	snapshot := &argo.Snapshot{}
	snapshot.Metadata.Name = "workflow"
	snapshot.Metadata.Annotations = map[string]string{"simworkflow.io/result-prefix": "experiments/" + execution, "simworkflow.io/execution-id": execution}
	snapshot.Status.Phase = "Running"
	snapshot.Status.StartedAt = base.Add(-20 * time.Second)
	snapshot.Status.Nodes = map[string]argo.Node{
		"pre": {Type: "Pod", TemplateName: run.ID + "-precompute", Phase: "Succeeded", StartedAt: base.Add(-20 * time.Second), FinishedAt: base.Add(-10 * time.Second)},
		"sim": {Type: "Pod", TemplateName: run.ID + "-simulation", Phase: "Running", StartedAt: base},
	}
	initial := worker.Progress{ExecutionID: execution, RunID: run.ID, Stage: worker.Simulation, Phase: "Running", StartedAt: base, UpdatedAt: base.Add(10 * time.Second), CompletedSteps: 10, TotalSteps: 100}
	latest := initial
	latest.UpdatedAt = base.Add(20 * time.Second)
	latest.CompletedSteps = 20
	var progress atomic.Value
	progress.Store(initial)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/workflows/"):
			json.NewEncoder(w).Encode(snapshot)
		case strings.HasSuffix(r.URL.Path, "/plan.json"):
			json.NewEncoder(w).Encode(plan)
		case strings.HasSuffix(r.URL.Path, "/progress.json"):
			json.NewEncoder(w).Encode(progress.Load())
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := config.Config{Argo: config.Argo{Server: server.URL, Namespace: "argo"}, S3: config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "test", SecretAccessKey: "test", ForcePathStyle: true}, HTTPTimeout: time.Second}
	fetch := loadProgress(context.Background(), c, "workflow")
	first := fetch().(progressFrame)
	if first.err != nil || first.estimate.known {
		t.Fatal("first observation should gather samples", first.err)
	}
	progress.Store(latest)
	second := fetch().(progressFrame)
	if second.err != nil || !second.estimate.known || second.estimate.remaining != 80*time.Second {
		t.Fatal("TUI did not estimate from stored step deltas", second.err, second.estimate)
	}
	if second.progressRows[0].completedSteps != 20 || !second.progressRows[0].precomputeFinishedAt.Equal(base.Add(-10*time.Second)) {
		t.Fatal("raw progress/stage observations were lost")
	}
	m := progressModel{frame: second, width: 140, height: 20, now: second.estimate.at}
	before := requests.Load()
	m, _ = updateProgress(m, progressClockTick(second.estimate.at.Add(time.Second)))
	if requests.Load() != before || !strings.Contains(m.View(), "remaining ~1m19s") {
		t.Fatal("clock tick fetched remotely or did not update estimate")
	}
	store := storage.New(c)
	estimator := newRunEstimator()
	var out bytes.Buffer
	progress.Store(initial)
	if _, err := writeRunProgressEstimate(context.Background(), store, snapshot, &out, estimator); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	progress.Store(latest)
	if _, err := writeRunProgressEstimate(context.Background(), store, snapshot, &out, estimator); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "STATE (total 1)") || !strings.Contains(out.String(), "Running:1") || !strings.Contains(out.String(), "remaining ~1m20s") {
		t.Fatal("plain output omitted counts or estimate", out.String())
	}
}
