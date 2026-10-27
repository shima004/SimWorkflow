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
					Spec struct {
						Templates []struct {
							InitContainers []struct {
								Image string `json:"image"`
								Env   []struct {
									Name  string `json:"name"`
									Value string `json:"value"`
								} `json:"env"`
							} `json:"initContainers"`
							ContainerSet struct {
								Containers []struct {
									Name  string `json:"name"`
									Image string `json:"image"`
								} `json:"containers"`
							} `json:"containerSet"`
						} `json:"templates"`
					} `json:"spec"`
				} `json:"workflow"`
			}
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				t.Error(err)
			}
			annotations = envelope.Workflow.Metadata.Annotations
			helperCount := 0
			for _, template := range envelope.Workflow.Spec.Templates {
				for _, init := range template.InitContainers {
					helperCount++
					if init.Image != defaultHelperImage {
						t.Error("prepare must use the pinned default helper")
					}
					if len(init.Env) != 1 || init.Env[0].Name != "SIMWORKFLOW_RUN_URL" || init.Env[0].Value != "{{inputs.parameters.config-url}}" {
						t.Error("submit without config-storage must use S3 configuration URLs")
					}
				}
				for _, c := range template.ContainerSet.Containers {
					if c.Name == "main" {
						helperCount++
						if c.Image != defaultHelperImage {
							t.Error("collector must use the pinned default helper")
						}
					}
				}
			}
			if helperCount == 0 {
				t.Error("submitted workflow has no helper containers")
			}
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
	if err := run([]string{"experiment", "render", "-f", file, "--helper-image", "registry/worker:test", "--config-storage", "inline", "--output", output}, lookup, &out, &errOut); err != nil {
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
	if !bytes.Contains(data, []byte("registry/worker:test")) || bytes.Contains(data, []byte(defaultHelperImage)) {
		t.Fatal("explicit helper-image must override the default")
	}
	if bytes.Count(data, []byte("name: SIMWORKFLOW_RUN\n")) != 18 || bytes.Contains(data, []byte("SIMWORKFLOW_RUN_URL")) {
		t.Fatal("explicit inline storage must embed each stage configuration")
	}
	mu.Lock()
	wasSubmitted := submitted
	mu.Unlock()
	if wasSubmitted {
		t.Fatal("render must not submit a workflow")
	}
	remoteOutput := filepath.Join(t.TempDir(), "remote-config.yaml")
	if err := run([]string{"experiment", "render", "-f", file, "--output", remoteOutput}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	remoteYAML, err := os.ReadFile(remoteOutput)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	configCount := 0
	for key, raw := range objects {
		if !strings.Contains(key, "/configs/") {
			continue
		}
		var c worker.Config
		if json.Unmarshal(raw, &c) != nil || !strings.Contains(key, "/"+c.Run.ID+"/"+c.Stage+".json") || c.ExecutionID == "" || c.Map.URL == "" {
			t.Error("stored configuration must match run and stage")
		}
		if bytes.Contains(remoteYAML, []byte(c.Map.URL)) {
			t.Error("external configuration contents must not be embedded in Workflow")
		}
		configCount++
	}
	mu.Unlock()
	if configCount != 18 || bytes.Count(remoteYAML, []byte("SIMWORKFLOW_RUN_URL")) != 2 || bytes.Contains(remoteYAML, []byte("SIMWORKFLOW_RUN:")) {
		t.Fatal("all nine runs must reference both stage configurations in S3")
	}
	out.Reset()
	if err := run([]string{"experiment", "submit", "-f", file}, lookup, &out, &errOut); err != nil {
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
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("non-terminal output must remain plain text")
	}
	out.Reset()
	if err := run([]string{"experiment", "status", "--watch", "--plain", "smoke-run"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Succeeded") || strings.Contains(out.String(), "\x1b") {
		t.Fatal("--plain must retain the existing text watch output")
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
