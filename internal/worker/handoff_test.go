package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

func TestSeparatePodsRestoreImmutablePrecompute(t *testing.T) {
	mapData := zipBytes(t, map[string]string{"map/scenario.xml": "<scenario><ambulanceteam/></scenario>", "config/kernel.cfg": "!include common.cfg\n", "config/common.cfg": "random.seed: 1\n"})
	var mu sync.Mutex
	objects := map[string][]byte{"/map": mapData}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			return
		}
		data, ok := objects[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	transfer := func(path string) storage.Transfer { return storage.Transfer{URL: server.URL + path} }
	put := transfer("/data")
	pre := Config{Stage: Precompute, ExecutionID: "execution-test", Run: experiment.Run{ID: "run-test", Seed: 7}, TimeoutSeconds: 60, Map: transfer("/map"), PrecomputeUpload: &put, Bundle: transfer("/pre-bundle"), Summary: transfer("/pre-summary")}
	root := t.TempDir()
	if err := Prepare(context.Background(), root, pre); err != nil {
		t.Fatal(err)
	}
	pre, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "precompute/ambulanceTeam/data.bin"), []byte("original-data"), 0600)
	writeJSON(statePath(root, "server"), State{Success: true})
	for _, role := range experiment.Roles() {
		writeJSON(statePath(root, AgentActor(role)), State{Success: true})
	}
	if err := collect(context.Background(), root, pre); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	var summary Summary
	json.Unmarshal(objects["/pre-summary"], &summary)
	original := append([]byte(nil), objects["/data"]...)
	mu.Unlock()
	if summary.Phase != "Succeeded" || summary.Stage != Precompute || len(original) == 0 {
		t.Fatal("precompute must publish data before succeeding")
	}
	get := transfer("/data")
	sim := Config{Stage: Simulation, ExecutionID: pre.ExecutionID, Run: pre.Run, TimeoutSeconds: 60, Map: transfer("/map"), PrecomputeDownload: &get}
	for i := 0; i < 2; i++ {
		next := t.TempDir()
		if err := Prepare(context.Background(), next, sim); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(next)
		if err != nil || !loaded.Deadline.Equal(pre.Deadline) || loaded.MapSHA256 != pre.MapSHA256 {
			t.Fatal("both pods must share the original budget and map identity")
		}
		data, err := os.ReadFile(filepath.Join(next, "precompute/ambulanceTeam/data.bin"))
		if err != nil || string(data) != "original-data" {
			t.Fatal("each retry must start from the original precompute data")
		}
		if _, ok := readState(next, "server"); ok {
			t.Fatal("phase states must not cross pod boundaries")
		}
		os.WriteFile(filepath.Join(next, "precompute/ambulanceTeam/data.bin"), []byte("simulation-mutated-copy"), 0600)
	}
	mu.Lock()
	objects["/map"] = append(mapData, 0)
	if string(objects["/data"]) != string(original) {
		t.Fatal("simulation must not modify the published data")
	}
	mu.Unlock()
	if err := Prepare(context.Background(), t.TempDir(), sim); err == nil || !strings.Contains(err.Error(), "map archive changed") {
		t.Fatalf("must reject a map changed between phases: %v", err)
	}
}

func TestRejectMismatchedExpiredAndUnexpectedHandoffs(t *testing.T) {
	for _, test := range []string{"execution", "run", "deadline", "path", "traversal"} {
		t.Run(test, func(t *testing.T) {
			manifest := handoffManifest{Version: 1, ExecutionID: "execution", RunID: "run", Deadline: time.Now().Add(time.Minute), MapSHA256: strings.Repeat("a", 64)}
			switch test {
			case "execution":
				manifest.ExecutionID = "other"
			case "run":
				manifest.RunID = "other"
			case "deadline":
				manifest.Deadline = time.Now().Add(-time.Minute)
			}
			encoded, _ := json.Marshal(manifest)
			files := map[string]string{"handoff.json": string(encoded), "precompute/ambulanceTeam/data.bin": "data"}
			if test == "path" {
				files["state/server.json"] = `{"success":true}`
			}
			if test == "traversal" {
				files["precompute/ambulanceTeam/../../run.json"] = "untrusted"
			}
			archive := zipBytes(t, files)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive) }))
			defer server.Close()
			c := Config{ExecutionID: "execution", Run: experiment.Run{ID: "run"}, PrecomputeDownload: &storage.Transfer{URL: server.URL}}
			if err := restoreHandoff(context.Background(), t.TempDir(), &c); err == nil {
				t.Fatal("invalid handoff must be rejected")
			}
		})
	}
}

func TestFailedHandoffUploadCannotSucceed(t *testing.T) {
	var summary Summary
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/data" {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/summary" {
			json.NewDecoder(r.Body).Decode(&summary)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "state"), 0700)
	os.Mkdir(filepath.Join(root, "precompute"), 0700)
	writeJSON(statePath(root, "server"), State{Success: true})
	for _, role := range experiment.Roles() {
		writeJSON(statePath(root, AgentActor(role)), State{Success: true})
	}
	put := storage.Transfer{URL: server.URL + "/data"}
	c := Config{Stage: Precompute, PrecomputeUpload: &put, Bundle: storage.Transfer{URL: server.URL + "/bundle"}, Summary: storage.Transfer{URL: server.URL + "/summary"}}
	if err := collect(context.Background(), root, c); err == nil || summary.Phase != "Failed" || summary.Error == "" {
		t.Fatal("publishing is part of precompute success; save the failure summary")
	}
}
