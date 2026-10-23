package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

func TestRemoteCommands(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	var annotations map[string]string
	submitted := false
	precomputeOnly := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodHead {
			return
		}
		if r.URL.Path == "/api/v1/workflows/argo" {
			if r.Header.Get("Authorization") != "Bearer secret-argo-token" {
				t.Error("missing Argo token")
			}
			var envelope struct {
				Workflow struct {
					Metadata struct {
						Annotations map[string]string `json:"annotations"`
					} `json:"metadata"`
				} `json:"workflow"`
			}
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				t.Error(err)
			}
			annotations = envelope.Workflow.Metadata.Annotations
			submitted = true
			json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "smoke-run"}})
			return
		}
		if r.URL.Path == "/api/v1/workflows/argo/smoke-run" {
			json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "smoke-run", "annotations": annotations}, "status": map[string]string{"phase": "Succeeded"}})
			return
		}
		if r.Method == http.MethodPut {
			data, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = data
			return
		}
		if data, ok := objects[r.URL.Path]; ok {
			w.Write(data)
			return
		}
		if precomputeOnly && strings.HasSuffix(r.URL.Path, "/summary.json") && !strings.HasSuffix(r.URL.Path, "/precompute/summary.json") {
			w.WriteHeader(404)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/summary.json") {
			json.NewEncoder(w).Encode(worker.Summary{Stage: worker.Precompute, Phase: "Succeeded"})
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	values := map[string]string{"ARGO_WORKFLOW_SERVER": server.URL, "ARGO_WORKFLOW_TOKEN": "secret-argo-token", "S3_ENDPOINT": server.URL, "S3_BUCKET": "results", "S3_ACCESS_KEY_ID": "test-access-key", "S3_SECRET_ACCESS_KEY": "secret-s3-key", "REGISTRY_URL": server.URL}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	file := "../../examples/experiments/module-comparison.yaml"
	var out, errOut bytes.Buffer
	output := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := run([]string{"experiment", "render", "-f", file, "--helper-image", "registry/worker:test", "--output", output}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("rendered capabilities must be in a private file")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("X-Amz-Signature")) || bytes.Contains(data, []byte("secret-s3-key")) || strings.Contains(out.String(), "X-Amz-Signature") {
		t.Fatal("render must include temporary capabilities only in the private file")
	}
	mu.Lock()
	wasSubmitted := submitted
	mu.Unlock()
	if wasSubmitted {
		t.Fatal("render must not submit a workflow")
	}
	out.Reset()
	if err := run([]string{"experiment", "submit", "-f", file, "--helper-image", "registry/worker:test"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Submitted smoke-run (9 runs)") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := run([]string{"experiment", "status", "--watch", "smoke-run"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Succeeded") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := run([]string{"experiment", "results", "smoke-run"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var summaries []worker.Summary
	if err := json.Unmarshal(out.Bytes(), &summaries); err != nil || len(summaries) != 9 {
		t.Fatalf("expected nine JSON summaries: %v", err)
	}
	mu.Lock()
	precomputeOnly = true
	mu.Unlock()
	out.Reset()
	if err := run([]string{"experiment", "results", "smoke-run"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &summaries); err != nil {
		t.Fatal(err)
	}
	if summaries[0].Stage != worker.Precompute || summaries[0].WorkflowPhase != "Succeeded" {
		t.Fatal("results must expose which stage the available summary describes")
	}
	mu.Lock()
	defer mu.Unlock()
	var plan experiment.Plan
	if err := json.Unmarshal(objects["/results/"+annotations["simworkflow.io/result-prefix"]+"/plan.json"], &plan); err != nil || plan.TotalRuns != 9 {
		t.Fatal(fmt.Sprintf("persisted plan mismatch: %v", err))
	}
}
