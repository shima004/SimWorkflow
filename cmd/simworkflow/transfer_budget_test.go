package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"go.yaml.in/yaml/v3"

	"simworkflow/internal/experiment"
	"simworkflow/internal/reproduction/testfixture"
)

func TestTransferBudgetFailsBeforeCatalogOrSnapshotPreparation(t *testing.T) {
	env := testfixture.New(t)
	e := env.Experiment()
	e.Spec.Agents[0].Matrix = nil
	e.Spec.Conditions.Maps = []experiment.Map{{Name: "registered-map", Versions: []string{"v1"}}}
	e.Spec.Conditions.Seeds = make([]int64, 3000)
	for i := range e.Spec.Conditions.Seeds {
		e.Spec.Conditions.Seeds[i] = int64(i + 1)
	}
	e.Spec.Execution.MaxParallelRuns = 16
	e.Spec.Execution.Timeout = "60m"
	data, err := yaml.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "experiment.yaml")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	c := env.Config
	c.S3.Endpoint, c.Registry.URL, c.Argo.Server = server.URL, server.URL, server.URL
	for _, command := range []string{"submit", "render"} {
		args := []string{command, "-f", file}
		if command == "render" {
			args = append(args, "--output", filepath.Join(t.TempDir(), "workflow.yaml"))
		}
		var out, progress bytes.Buffer
		err := runRemote(args, c, &out, &progress)
		if err == nil {
			t.Fatal("over-budget experiment was accepted")
		}
		for _, want := range []string{"signed transfer validity", "3000 runs", "parallelism 16", "188 waves", "167h", "minimum parallelism is 20"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("missing budget explanation %q: %v", want, err)
			}
		}
		if requests.Load() != 0 || strings.Contains(progress.String(), "Verify and snapshot maps") {
			t.Fatal("duration check ran after network preparation", progress.String())
		}
	}
}
