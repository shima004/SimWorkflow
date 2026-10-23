package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

func TestProgressTailAppendPartialAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel.log")
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("INFO kernel : Termination condition: Timestep >= 300\nINFO kernel : Timestep 1 complete\nDEBUG kernel : Score: 5.0\nINFO kernel : Timestep 2 comp")
	var tail progressTail
	tail.read(path)
	if tail.value.CompletedSteps != 1 || tail.value.TotalSteps != 300 || tail.value.Score == nil || *tail.value.Score != 5 {
		t.Fatal(tail.value)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("lete\nDEBUG kernel : Score: 4.25\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	tail.read(path)
	if tail.value.CompletedSteps != 2 || *tail.value.Score != 4.25 {
		t.Fatal(tail.value)
	}
	offset := tail.offset
	tail.read(path)
	if tail.offset != offset || tail.value.CompletedSteps != 2 {
		t.Fatal("duplicate counting")
	}
	write("INFO kernel : Timestep 1 complete\n")
	tail.read(path)
	if tail.value.CompletedSteps != 1 || tail.value.Score != nil {
		t.Fatal("truncation must reset observations")
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	write("INFO kernel : Termination condition: Timestep >= 10\nINFO kernel : Timestep 3 complete\nDEBUG kernel : Score: 2e-3\n")
	tail.read(path)
	if tail.value.CompletedSteps != 3 || tail.value.TotalSteps != 10 || *tail.value.Score != 0.002 {
		t.Fatal(tail.value)
	}
}

func TestProgressReporterPublishesFinalSnapshot(t *testing.T) {
	var mu sync.Mutex
	var records []Progress
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Progress
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		mu.Lock()
		records = append(records, p)
		mu.Unlock()
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs/simulation"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "logs/simulation/kernel.log"), []byte("INFO kernel : Timestep 5 complete\nDEBUG kernel : Score: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := Config{ExecutionID: "execution", Stage: Simulation, Run: experiment.Run{ID: "run-one"}, ProgressUpload: &storage.Transfer{URL: server.URL}}
	p := startProgress(context.Background(), root, c)
	p.finish("Succeeded")
	mu.Lock()
	defer mu.Unlock()
	if len(records) == 0 {
		t.Fatal("no progress published")
	}
	last := records[len(records)-1]
	if last.Phase != "Succeeded" || last.ExecutionID != "execution" || last.RunID != "run-one" || last.CompletedSteps != 5 || last.Score == nil || *last.Score != 7 || last.UpdatedAt.IsZero() {
		t.Fatal(last)
	}
}

func TestProgressUploadFailureDoesNotFailCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/progress" {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(statePath(root, "server"), State{Success: true}); err != nil {
		t.Fatal(err)
	}
	for _, role := range experiment.Roles() {
		if err := writeJSON(statePath(root, AgentActor(role)), State{Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	c := Config{Stage: Simulation, Run: experiment.Run{ID: "run-one"}, ProgressUpload: &storage.Transfer{URL: server.URL + "/progress"}, Bundle: storage.Transfer{URL: server.URL + "/bundle"}, Summary: storage.Transfer{URL: server.URL + "/summary"}}
	if err := collect(context.Background(), root, c); err != nil {
		t.Fatal(err)
	}
}
