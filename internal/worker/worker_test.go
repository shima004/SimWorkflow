package worker

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, data := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestPrepareAndSeedIsolation(t *testing.T) {
	archive := zipBytes(t, map[string]string{"map/scenario.xml": "<scenario><ambulanceteam/><firebrigade/><policeforce/></scenario>", "config/kernel.cfg": "!include common.cfg\n", "config/common.cfg": "random.seed: 1023\nkernel.host: localhost\n", "config/other.cfg": "random.seed: 999\n"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive) }))
	defer server.Close()
	for _, seed := range []int64{1, 2} {
		root := filepath.Join(t.TempDir(), "run")
		c := Config{TimeoutSeconds: 60, Run: experiment.Run{ID: "run-test", Seed: seed}, Map: storage.Transfer{URL: server.URL}}
		if err := Prepare(context.Background(), root, c); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"common.cfg", "other.cfg"} {
			data, err := os.ReadFile(filepath.Join(root, "scenario/config", name))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "1023") || strings.Contains(string(data), "999") {
				t.Fatal("all component seeds must be overwritten in run-local configs")
			}
		}
		if got, err := Load(root); err != nil || got.Run.Seed != seed {
			t.Fatal("resolved run configuration must be preserved")
		}
	}
}

func TestUnsafeArchivesRejected(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "map/../../escape", `map\escape`} {
		archive := filepath.Join(t.TempDir(), "map.zip")
		os.WriteFile(archive, zipBytes(t, map[string]string{name: "data"}), 0600)
		if err := extractZIP(archive, filepath.Join(t.TempDir(), "map")); err == nil {
			t.Fatalf("unsafe path accepted: %s", name)
		}
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "map/link"}
	header.SetMode(os.ModeSymlink | 0777)
	f, _ := writer.CreateHeader(header)
	io.WriteString(f, "/etc/passwd")
	writer.Close()
	archive := filepath.Join(t.TempDir(), "map.zip")
	os.WriteFile(archive, buffer.Bytes(), 0600)
	if err := extractZIP(archive, t.TempDir()); err == nil {
		t.Fatal("archive symlinks must be rejected")
	}
}

func TestAgentPhaseCommands(t *testing.T) {
	counts := []int{4, 1, 3, 1, 2, 1}
	for _, runtime := range []string{"java", "python"} {
		c := Config{Run: experiment.Run{Configuration: experiment.Configuration{Runtime: runtime, Agent: "sample"}}}
		pre := strings.Join(agentCommand(c, "precompute", "/work/module.cfg", counts, "/work", "fireBrigade"), " ")
		run := strings.Join(agentCommand(c, "simulation", "/work/module.cfg", counts, "/work", "fireBrigade"), " ")
		if runtime == "java" {
			if !strings.Contains(pre, "-t 1,0,1,0,1,0") || !strings.Contains(pre, "-pre true") || !strings.Contains(run, "-pre false") || !strings.Contains(run, "-t 4,1,3,1,2,1") {
				t.Fatal("Java must precompute once per platoon role and disable precompute on the next launch")
			}
		}
		if runtime == "python" {
			if !strings.Contains(pre, "--precompute") || strings.Contains(run, "--precompute") || !strings.Contains(run, "-a 2 -f 4 -p 3") {
				t.Fatal("Python phase flags and counts must be independent")
			}
		}
	}
	if counts[0] != 4 || counts[1] != 1 {
		t.Fatal("phase command generation must not mutate scenario counts")
	}
}

func TestReadinessAcrossOutputChunks(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "state"), 0700)
	var output bytes.Buffer
	writer := readyWriter{writer: &output, root: root, phase: "precompute"}
	writer.Write([]byte("Start your "))
	writer.Write([]byte("agents\n"))
	if state, ok := readState(root, "precompute.ready"); !ok || !state.Success {
		t.Fatal("split readiness message must release agent startup")
	}
}

func TestCollectPublishesSummaryAndExcludesCapabilities(t *testing.T) {
	var mu sync.Mutex
	received := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		received[r.URL.Path] = data
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	root := t.TempDir()
	for _, name := range []string{"state", "precompute", "modules", "logs/simulation"} {
		os.MkdirAll(filepath.Join(root, name), 0700)
	}
	os.WriteFile(filepath.Join(root, "precompute/data.bin"), []byte("computed-data"), 0600)
	os.WriteFile(filepath.Join(root, "logs/simulation/kernel.log"), []byte("Score: 5\nScore: 9.5\n"), 0600)
	os.WriteFile(filepath.Join(root, "logs/simulation/agent.log"), []byte("PRECOMPUTED\n"), 0600)
	writeJSON(statePath(root, "server"), State{Success: true})
	for _, role := range experiment.Roles() {
		writeJSON(statePath(root, AgentActor(role)), State{Success: true})
	}
	c := Config{ExecutionID: "execution-test", Run: experiment.Run{ID: "run-test"}, Bundle: storage.Transfer{URL: server.URL + "/bundle?secret-url-token"}, Summary: storage.Transfer{URL: server.URL + "/summary?secret-url-token"}, Collect: []string{"score", "simulatorLogs"}}
	writeJSON(filepath.Join(root, "run.json"), c)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := collect(ctx, root, c); err != nil {
		t.Fatal(err)
	}
	var summary Summary
	if err := json.Unmarshal(received["/summary"], &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Phase != "Succeeded" || summary.Score == nil || *summary.Score != 9.5 || len(summary.PrecomputeFiles) != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	gz, err := gzip.NewReader(bytes.NewReader(received["/bundle"]))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "secret-url-token") || header.Name == "run.json" || strings.HasSuffix(header.Name, "agent.log") {
			t.Fatalf("bundle must exclude signed URLs and unrequested agent logs: %s", header.Name)
		}
	}
}

func TestCanceledRunStillStoresFailure(t *testing.T) {
	var summary Summary
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/summary" {
			json.NewDecoder(r.Body).Decode(&summary)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "precompute"), 0700)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := collect(ctx, root, Config{Run: experiment.Run{ID: "run-test"}, Bundle: storage.Transfer{URL: server.URL + "/bundle"}, Summary: storage.Transfer{URL: server.URL + "/summary"}})
	if err == nil || summary.Phase != "Failed" || summary.Server.Success || summary.Agent.Success {
		t.Fatal("canceled runs must remain failed while saving a failure report")
	}
}

func TestResumeModeVerification(t *testing.T) {
	for _, runtime := range []string{"java", "python"} {
		root := t.TempDir()
		dir := filepath.Join(root, "logs/simulation")
		os.MkdirAll(dir, 0700)
		name := AgentActor("fireBrigade") + ".log"
		mode := "PRECOMPUTED"
		if runtime == "python" {
			name = AgentActor("fireBrigade") + "-internal.log"
			mode = "1"
		}
		makeLog := func(mode string, count int) string {
			var output strings.Builder
			for _, role := range []string{"Police", "Fire", "Ambulance"}[:count] {
				if runtime == "python" {
					entry, _ := json.Marshal(map[string]string{"event": "Connected to kernel: Platoon" + role + " (request_id: 1, agent_id: 2, mode: " + mode + ")"})
					output.Write(entry)
					output.WriteByte('\n')
				} else {
					output.WriteString("[INFO] Connected - adf.core.agent.platoon.Platoon" + role + "@123 (" + mode + ")\n")
				}
			}
			return output.String()
		}
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte(makeLog(mode, 3)), 0600)
		counts := []int{1, 0, 1, 0, 1, 0}
		if err := verifyResumed(root, runtime, counts, "fireBrigade"); err != nil {
			t.Fatalf("%s: %v", runtime, err)
		}
		os.WriteFile(path, []byte(makeLog("0", 3)), 0600)
		if err := verifyResumed(root, runtime, counts, "fireBrigade"); err == nil {
			t.Fatal("non-precomputed modes must fail")
		}
		os.WriteFile(path, []byte(makeLog(mode, 2)), 0600)
		if err := verifyResumed(root, runtime, counts, "fireBrigade"); err == nil {
			t.Fatal("missing platoon connections must fail")
		}
	}
}

func TestRoleCountsIncludeCorrespondingCentres(t *testing.T) {
	counts := []int{4, 1, 3, 2, 5, 6}
	for i, role := range experiment.Roles() {
		selected := countsForRole(counts, role)
		for j, n := range selected {
			want := 0
			if j == 2*i || j == 2*i+1 {
				want = counts[j]
			}
			if n != want {
				t.Fatalf("%s selected unrelated role: %v", role, selected)
			}
		}
		for _, runtime := range []string{"java", "python"} {
			c := Config{Run: experiment.Run{Configuration: experiment.Configuration{Runtime: runtime}}}
			pre := strings.Join(agentCommand(c, "precompute", "module", selected, "/work", role), " ")
			run := strings.Join(agentCommand(c, "simulation", "module", selected, "/work", role), " ")
			if runtime == "java" {
				values := []string{"0", "0", "0", "0", "0", "0"}
				values[2*i] = "1"
				if !strings.Contains(pre, "-t "+strings.Join(values, ",")) {
					t.Fatal(pre)
				}
				values[2*i] = strconv.Itoa(counts[2*i])
				values[2*i+1] = strconv.Itoa(counts[2*i+1])
				if !strings.Contains(run, "-t "+strings.Join(values, ",")) {
					t.Fatal(run)
				}
			} else {
				flags := []string{"-f", "-fs", "-p", "-po", "-a", "-ac"}
				for j, flag := range flags {
					if !strings.Contains(run, flag+" "+strconv.Itoa(selected[j])+" ") {
						t.Fatal(run)
					}
				}
				if !strings.Contains(pre, "-ac 0 -fs 0 -po 0") {
					t.Fatal(pre)
				}
			}
		}
	}
}

func TestRoleBarrierWaitsForAllAndReportsFailuresEarly(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "state"), 0700)
	roles := experiment.Roles()
	for _, role := range roles[:2] {
		writeJSON(statePath(root, "precompute."+AgentActor(role)), State{Success: true})
	}
	state, _, done := agentStates(root, "precompute")
	if done || state.Success {
		t.Fatal("must wait for the last precompute role")
	}
	writeJSON(statePath(root, "precompute."+AgentActor(roles[2])), State{Success: true})
	state, _, done = agentStates(root, "precompute")
	if !done || !state.Success {
		t.Fatal("all roles should release the barrier")
	}
	writeJSON(statePath(root, AgentActor(roles[0])), State{Error: "startup failed"})
	state, _, done = agentStates(root, "")
	if done || state.Success || !strings.Contains(state.Error, "fireBrigade") {
		t.Fatal("report a failure before other roles finish")
	}
	if _, err := waitServerPhase(context.Background(), root, "precompute"); err == nil {
		t.Fatal("sibling failure must abort the server restart wait")
	}
	if err := waitAgents(context.Background(), root, "simulation"); err == nil {
		t.Fatal("failure must abort barrier wait")
	}
}

func TestAbsentRoleSkipsStartupAndCompletesBarriers(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "scenario/map"), 0700)
	os.Mkdir(filepath.Join(root, "state"), 0700)
	os.WriteFile(filepath.Join(root, "scenario/map/scenario.xml"), []byte("<scenario><ambulanceteam/></scenario>"), 0600)
	if err := runAgent(context.Background(), root, Config{}, "fireBrigade"); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"precompute", "simulation"} {
		state, ok := readState(root, phase+"."+AgentActor("fireBrigade"))
		if !ok || !state.Success {
			t.Fatal("absent roles must not block either phase")
		}
	}
}
