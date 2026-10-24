package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

func TestGroupedModuleOutputsAndDetails(t *testing.T) {
	file := "../../examples/experiments/grouped-modules.yaml"
	e, err := experiment.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experiment.BuildPlan(e, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	lookup := func(string) (string, bool) { return "", false }
	if err := run([]string{"experiment", "plan", "-f", file}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "group A = astar") || !strings.Contains(out.String(), "group B = kmeanspp") {
		t.Fatal("human-readable plan must identify selected module sets")
	}
	destination := filepath.Join(t.TempDir(), "modules")
	if err := run([]string{"experiment", "modules", "-f", file, "--agent", "grouped-python", "--base", "../../examples/module-configs/grouped-python.yaml", "--format", "python", "--output-dir", destination}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest moduleManifest
	if json.Unmarshal(data, &manifest) != nil || len(manifest.Files) != 4 {
		t.Fatal("expected one generated file per grouped configuration")
	}
	for i, item := range manifest.Files {
		if !maps.Equal(item.Configuration.Groups, plan.Runs[i].Configuration.Groups) || item.Configuration.ID != plan.Runs[i].Configuration.ID {
			t.Fatal("manifest and plan must share choices and IDs")
		}
		contents, err := os.ReadFile(filepath.Join(destination, item.File))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range item.Configuration.Modules {
			if !strings.Contains(string(contents), value) {
				t.Fatal("module file must contain every flattened choice")
			}
		}
		details := strings.Join(runDetails(plan, plan.Runs[i]), "\n")
		for group, name := range item.Configuration.Groups {
			if !strings.Contains(details, group+" = "+name) {
				t.Fatal("TUI details must show selected module set names")
			}
		}
	}
}

func TestGroupedResultsRecoverLabelsFromPlanForOlderHelpers(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/grouped-modules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experiment.BuildPlan(e, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	var mismatch atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workflows/argo/grouped-run":
			json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "grouped-run", "annotations": map[string]string{"simworkflow.io/result-prefix": "experiments/grouped"}}, "status": map[string]string{"phase": "Succeeded"}})
			return
		case "/results/experiments/grouped/plan.json":
			json.NewEncoder(w).Encode(plan)
			return
		}
		for _, run := range plan.Runs {
			if r.URL.Path != "/results/experiments/grouped/"+run.ID+"/summary.json" {
				continue
			}
			run.Configuration.Groups = nil // Simulate a helper built before groups existed.
			if mismatch.Load() {
				run.ID = "different-run"
			}
			json.NewEncoder(w).Encode(worker.Summary{Run: run, Stage: worker.Simulation, Phase: "Succeeded"})
			return
		}
		t.Error("unexpected result request")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	values := map[string]string{"ARGO_WORKFLOW_SERVER": server.URL, "S3_ENDPOINT": server.URL, "S3_BUCKET": "results", "S3_ACCESS_KEY_ID": "test-key", "S3_SECRET_ACCESS_KEY": "test-secret", "REGISTRY_URL": server.URL}
	lookup := func(key string) (string, bool) { v, ok := values[key]; return v, ok }
	var out, errOut bytes.Buffer
	args := []string{"experiment", "results", "grouped-run"}
	if err := run(args, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var summaries []worker.Summary
	if json.Unmarshal(out.Bytes(), &summaries) != nil || len(summaries) != 4 {
		t.Fatal("grouped results must be returned")
	}
	for i, summary := range summaries {
		if !maps.Equal(summary.Run.Configuration.Groups, plan.Runs[i].Configuration.Groups) {
			t.Fatal("old helper results must be annotated from their exact saved plan")
		}
	}
	mismatch.Store(true)
	out.Reset()
	if err := run(args, lookup, &out, &errOut); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("must not attach labels to another run's result")
	}
}
