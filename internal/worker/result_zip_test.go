package worker

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

func TestResultZIPCompressionAndSelection(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs/simulation"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"logs/simulation/score.log": "score: 123\n", "logs/simulation/rescue.log.7z": "compressed bytes", "logs/simulation/agent-fire-brigade.log": "not selected", "run.json": "private signed URL"}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "bundle.zip")
	if err := bundleResult(root, path, []string{"simulatorLogs"}, "zip"); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if len(z.File) != 2 {
		t.Fatal("signed settings and unselected logs must not be archived", z.File)
	}
	for _, file := range z.File {
		method := uint16(zip.Deflate)
		if file.Name == "logs/simulation/rescue.log.7z" {
			method = zip.Store
		}
		if file.Method != method {
			t.Fatal("already-compressed files must not be recompressed", file.Name, file.Method)
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil || string(data) != files[file.Name] {
			t.Fatal(file.Name, err)
		}
	}
}

func TestCollectorUploadsZIPResult(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, actor := range append([]string{"server"}, AgentActor(experiment.Roles()[0]), AgentActor(experiment.Roles()[1]), AgentActor(experiment.Roles()[2])) {
		if err := writeJSON(statePath(root, actor), State{Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	var uploaded atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundle.zip" {
			data, err := io.ReadAll(r.Body)
			if err != nil || len(data) < 4 || string(data[:2]) != "PK" {
				t.Error("collector did not upload ZIP bytes", err)
			}
			uploaded.Store(true)
		}
	}))
	defer server.Close()
	c := Config{Stage: Simulation, Run: experiment.Run{ID: "run-test"}, BundleFormat: "zip", Bundle: storage.Transfer{URL: server.URL + "/bundle.zip"}, Summary: storage.Transfer{URL: server.URL + "/summary"}}
	if err := collect(context.Background(), root, c); err != nil || !uploaded.Load() {
		t.Fatal(err, uploaded.Load())
	}
}
