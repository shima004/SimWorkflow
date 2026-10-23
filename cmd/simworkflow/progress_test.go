package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"simworkflow/internal/argo"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

func TestRunProgressUsesCurrentAttemptAndHandlesOlderWorkers(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/precompute-baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experiment.BuildPlan(e, 100)
	if err != nil {
		t.Fatal(err)
	}
	run := plan.Runs[0]
	for _, tc := range []struct {
		name                                  string
		progressStatus                        int
		wrongIdentity, retryPending, terminal bool
		want, absent                          string
	}{
		{name: "live", want: "45/300 (15%)"},
		{name: "wrong execution ignored", wrongIdentity: true, absent: "45/300"},
		{name: "pending retry hides previous progress", retryPending: true, want: "Pending", absent: "45/300"},
		{name: "older worker uses final summary", progressStatus: 404, terminal: true, want: "6.5", absent: "45/300"},
		{name: "progress unavailable is nonfatal", progressStatus: 503, want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			score := 4.5
			progress := worker.Progress{ExecutionID: "execution", RunID: run.ID, Stage: worker.Simulation, Phase: "Running", CompletedSteps: 45, TotalSteps: 300, Score: &score, UpdatedAt: now}
			if tc.wrongIdentity {
				progress.ExecutionID = "other-execution"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/plan.json"):
					json.NewEncoder(w).Encode(plan)
				case strings.HasSuffix(r.URL.Path, "/progress.json"):
					if tc.progressStatus != 0 {
						w.WriteHeader(tc.progressStatus)
						return
					}
					json.NewEncoder(w).Encode(progress)
				case strings.HasSuffix(r.URL.Path, "/summary.json"):
					score := 6.5
					json.NewEncoder(w).Encode(worker.Summary{ExecutionID: "execution", Run: run, Stage: worker.Simulation, Phase: "Succeeded", Score: &score})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			store := storage.New(config.Config{S3: config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true}, HTTPTimeout: time.Second})
			phase := "Running"
			if tc.terminal {
				phase = "Succeeded"
			}
			nodes := map[string]any{"sim0": map[string]any{"type": "Pod", "templateName": run.ID + "-simulation", "displayName": "simulation(0)", "phase": phase, "startedAt": now.Add(-time.Minute)}}
			if tc.retryPending {
				nodes["sim0"].(map[string]any)["phase"] = "Failed"
				nodes["sim1"] = map[string]any{"type": "Pod", "templateName": run.ID + "-simulation", "displayName": "simulation(1)", "phase": "Pending"}
			}
			var snapshot argo.Snapshot
			data, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{"simworkflow.io/result-prefix": "experiments/test", "simworkflow.io/execution-id": "execution"}}, "status": map[string]any{"phase": phase, "nodes": nodes}})
			if err := json.Unmarshal(data, &snapshot); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := writeRunProgress(context.Background(), store, &snapshot, &out); err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q: %s", tc.want, out.String())
			}
			if tc.absent != "" && strings.Contains(out.String(), tc.absent) {
				t.Fatalf("unexpected %q: %s", tc.absent, out.String())
			}
		})
	}
}
