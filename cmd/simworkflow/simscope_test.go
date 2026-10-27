package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"simworkflow/internal/argo"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

func TestSimScopeURLParameters(t *testing.T) {
	run := experiment.Run{Configuration: experiment.Configuration{Agent: "日本語 & team"}, Map: experiment.Map{Name: "vc", Version: "test-v1"}}
	if _, err := simScopeURL("", run, url.Values{}); err == nil || !strings.Contains(err.Error(), "simScope.url") || !strings.Contains(err.Error(), "SIMSCOPE_URL") {
		t.Fatal("missing viewer URL must explain how to configure it")
	}
	log := "http://s3.example/bucket/rescue.log.7z?X-Amz-Signature=test&X-Amz-Security-Token=a%2Bb%2Fc"
	raw, err := simScopeURL("http://viewer.example/prefix/", run, url.Values{"url": {log}})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Path != "/prefix/" || u.Query().Get("url") != log || u.Query().Get("team") != run.Configuration.Agent || u.Query().Get("map") != "vc@test-v1" || u.Query().Has("host") || u.Query().Has("autoload") {
		t.Fatal("URL parameters must round-trip without mixing live and replay modes")
	}
	for _, base := range []string{"javascript:alert(1)", "http://user:secret@viewer", "http://viewer?secret=value", "http://viewer/#secret"} {
		if _, err := simScopeURL(base, run, url.Values{}); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid viewer URL must be rejected safely")
		}
	}
}

func TestSimScopeChoosesCurrentSimulationAttempt(t *testing.T) {
	run := detailsPlan(t).Runs[0]
	for _, tc := range []struct {
		name, stage, phase, ip string
		retry                  bool
		shared                 bool
		wantLive               bool
	}{
		{name: "running simulation", stage: "simulation", phase: "Running", ip: "10.42.1.2", wantLive: true},
		{name: "running shared simulation", stage: "simulation", phase: "Running", ip: "10.42.1.2", wantLive: true, shared: true},
		{name: "pending shared retry", stage: "simulation", phase: "Running", ip: "10.42.1.2", retry: true, shared: true},
		{name: "precompute must not open", stage: "precompute", phase: "Running", ip: "10.42.1.2"},
		{name: "pending retry hides old running Pod", stage: "simulation", phase: "Running", ip: "10.42.1.2", retry: true},
		{name: "no Pod IP yet", stage: "simulation", phase: "Running"},
		{name: "IPv6 target", stage: "simulation", phase: "Running", ip: "fd00::12", wantLive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var snap argo.Snapshot
			snap.Status.Nodes = map[string]argo.Node{"first": {Type: "Pod", TemplateName: run.ID + "-" + tc.stage, DisplayName: "simulation(0)", Phase: tc.phase, PodIP: tc.ip, StartedAt: time.Now()}}
			if tc.retry {
				snap.Status.Nodes["retry"] = argo.Node{Type: "Pod", TemplateName: run.ID + "-simulation", DisplayName: "simulation(1)", Phase: "Pending"}
			}
			if tc.shared {
				snap.Metadata.Name = "test"
				for key, node := range snap.Status.Nodes {
					node.TemplateName = "shared-simulation-abc"
					node.Name = "test." + run.ID + "[1]." + node.DisplayName
					snap.Status.Nodes[key] = node
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/api/v1/workflows/") {
					if tc.ip != "" {
						t.Error("known Pod IP must not read or write S3")
					}
					w.WriteHeader(http.StatusNotFound)
					return
				}
				json.NewEncoder(w).Encode(snap)
			}))
			defer server.Close()
			c := config.Config{SimScopeURL: "http://viewer.example", Argo: config.Argo{Server: server.URL, Namespace: "argo"}, S3: config.S3{Endpoint: server.URL, Bucket: "results", Region: "us-east-1", AccessKeyID: "key", SecretAccessKey: "test", ForcePathStyle: true}, HTTPTimeout: time.Second}
			raw, err := prepareSimScope(context.Background(), c, "test", run)
			if !tc.wantLive {
				if err == nil || raw != "" {
					t.Fatal("unready simulations must not open a viewer")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(raw)
			if u.Query().Get("host") != tc.ip || u.Query().Get("port") != "27931" || u.Query().Has("url") {
				t.Fatal("wrong live parameters")
			}
		})
	}
}

func TestSimScopeResolvesSequenceFromSavedPlan(t *testing.T) {
	plan := detailsPlan(t)
	group := experiment.RunSequences(plan.Runs)[0]
	run := plan.Runs[0]
	for _, pending := range []bool{false, true} {
		snapshot := progressSnapshot(plan)
		snapshot.Metadata.Name = "test"
		snapshot.Metadata.Annotations["simworkflow.io/run-sequences"] = "v1"
		snapshot.Metadata.Annotations["simworkflow.io/result-prefix"] = "experiments/" + strings.Repeat("a", 32)
		root := "test." + group.Name + "(0:0)[1].simulation"
		snapshot.Status.Nodes = map[string]argo.Node{"sim": {Type: "Pod", Name: root + "(0)", TemplateName: "shared-simulation-abc", DisplayName: "simulation(0)", Phase: "Running", PodIP: "10.42.1.2", StartedAt: time.Now()}}
		if pending {
			snapshot.Status.Nodes["retry"] = argo.Node{Type: "Pod", Name: root + "(1)", TemplateName: "shared-simulation-abc", DisplayName: "simulation(1)", Phase: "Pending"}
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/v1/workflows/") {
				json.NewEncoder(w).Encode(snapshot)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/plan.json") {
				json.NewEncoder(w).Encode(plan)
				return
			}
			t.Error("sequence resolution must not read unrelated artifacts")
			w.WriteHeader(404)
		}))
		c := config.Config{SimScopeURL: "http://viewer.example", Argo: config.Argo{Server: server.URL, Namespace: "argo"}, S3: config.S3{Endpoint: server.URL, Bucket: "results", Region: "us-east-1", AccessKeyID: "key", SecretAccessKey: "test", ForcePathStyle: true}, HTTPTimeout: time.Second}
		raw, err := prepareSimScope(context.Background(), c, "test", run)
		server.Close()
		if pending {
			if err == nil || raw != "" {
				t.Fatal("pending loop retry must not open old Pod")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(raw)
		if u.Query().Get("host") != "10.42.1.2" || u.Query().Get("port") != "27931" {
			t.Fatal("wrong loop live endpoint")
		}
	}
}

func TestSimScopePublishesOnlyRescueLogAndReusesCache(t *testing.T) {
	run := detailsPlan(t).Runs[0]
	const prefix = "experiments/0123456789abcdef0123456789abcdef"
	const execution = "execution-test"
	finished := time.Now().UTC()
	summary := worker.Summary{Run: run, Stage: worker.Simulation, ExecutionID: execution, FinishedAt: finished}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	log := []byte("7z-test-selected-run")
	for name, data := range map[string][]byte{"logs/simulation/rescue.log.7z": log, "logs/simulation/kernel.log": []byte("not a replay"), "modules/agent.cfg": []byte("private settings")} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))})
		tw.Write(data)
	}
	tw.Close()
	gz.Close()
	var mu sync.Mutex
	var cached []byte
	reads, writes := 0, 0
	key := prefix + "/" + run.ID + "/viewer/"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/workflows/"):
			json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "test", "annotations": map[string]string{"simworkflow.io/result-prefix": prefix, "simworkflow.io/execution-id": execution}}, "status": map[string]any{"nodes": map[string]argo.Node{"sim": {Type: "Pod", TemplateName: run.ID + "-simulation", Phase: "Succeeded", StartedAt: finished.Add(-time.Minute)}}}})
		case strings.HasSuffix(r.URL.Path, "/summary.json"):
			json.NewEncoder(w).Encode(summary)
		case strings.Contains(r.URL.Path, key):
			if r.Method == http.MethodPut {
				writes++
				cached, _ = io.ReadAll(r.Body)
			} else if cached == nil {
				w.WriteHeader(http.StatusNotFound)
			}
		case strings.HasSuffix(r.URL.Path, "/bundle.tar.gz"):
			reads++
			w.Write(archive.Bytes())
		default:
			t.Errorf("unexpected object: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := config.Config{SimScopeURL: "http://viewer.example", Argo: config.Argo{Server: server.URL, Namespace: "argo"}, S3: config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "key", SecretAccessKey: "test-secret", ForcePathStyle: true}, HTTPTimeout: time.Second}
	for i := 0; i < 2; i++ {
		raw, err := prepareSimScope(context.Background(), c, "test", run)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(raw)
		logURL, _ := url.Parse(u.Query().Get("url"))
		if !strings.HasPrefix(logURL.Path, "/results/"+key) || !strings.HasSuffix(logURL.Path, "/rescue.log.7z") || logURL.Query().Get("X-Amz-Signature") == "" || logURL.Query().Get("X-Amz-Expires") != "86400" || strings.Contains(raw, "test-secret") {
			t.Fatal("replay must use a single-object signed URL without a raw credential")
		}
	}
	mu.Lock()
	if reads != 1 || writes != 1 || !bytes.Equal(cached, log) {
		t.Fatal("must publish only the selected replay once and then reuse it")
	}
	// A stale result must not reuse the previous attempt's cache.
	summary.ExecutionID = "other-execution"
	mu.Unlock()
	if raw, err := prepareSimScope(context.Background(), c, "test", run); err == nil || raw != "" {
		t.Fatal("mismatched results must be rejected")
	}
}

func TestLiveViewerUsesOnlyCurrentProgress(t *testing.T) {
	run := detailsPlan(t).Runs[0]
	started := time.Now().UTC().Add(-time.Minute)
	for _, field := range []string{"valid", "execution", "run", "stage", "started", "updated", "phase", "ip"} {
		t.Run(field, func(t *testing.T) {
			p := worker.Progress{ExecutionID: "current", RunID: run.ID, Stage: worker.Simulation, Phase: "Running", PodIP: "10.42.3.4", StartedAt: started.Add(time.Second), UpdatedAt: time.Now().UTC()}
			switch field {
			case "execution":
				p.ExecutionID = "old"
			case "run":
				p.RunID = "other"
			case "stage":
				p.Stage = worker.Precompute
			case "started":
				p.StartedAt = started.Add(-time.Minute)
			case "updated":
				p.UpdatedAt = started.Add(-time.Minute)
			case "phase":
				p.Phase = "Succeeded"
			case "ip":
				p.PodIP = "invalid-host"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/v1/workflows/") {
					json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"annotations": map[string]string{"simworkflow.io/result-prefix": "experiments/0123456789abcdef0123456789abcdef", "simworkflow.io/execution-id": "current"}}, "status": map[string]any{"nodes": map[string]argo.Node{"sim": {Type: "Pod", TemplateName: run.ID + "-simulation", Phase: "Running", StartedAt: started}}}})
				} else if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/progress.json") {
					json.NewEncoder(w).Encode(p)
				} else {
					t.Error("live fallback must only read current progress")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			c := config.Config{SimScopeURL: "http://viewer.example", Argo: config.Argo{Server: server.URL, Namespace: "argo"}, S3: config.S3{Endpoint: server.URL, Bucket: "results", Region: "us-east-1", AccessKeyID: "key", SecretAccessKey: "test", ForcePathStyle: true}, HTTPTimeout: time.Second}
			raw, err := prepareSimScope(context.Background(), c, "test", run)
			if field != "valid" {
				if err == nil || raw != "" {
					t.Fatal("stale or mismatched progress must not open a connection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(raw)
			if u.Query().Get("host") != p.PodIP || u.Query().Get("port") != "27931" {
				t.Fatal("missing live connection parameters")
			}
		})
	}
}

func TestViewerKeyIsAsyncAndOnlyUsesOpenDetails(t *testing.T) {
	p := detailsPlan(t)
	m := testProgressModel()
	m.frame.plan, m.frame.runs = p, p.Runs
	calls := 0
	m.openViewer = func(workflow string, run experiment.Run) tea.Cmd {
		calls++
		if run.ID != p.Runs[0].ID {
			t.Fatal("must open the run currently shown in details")
		}
		return func() tea.Msg { return viewerOpened{} }
	}
	w := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}
	m, cmd := updateProgress(m, w)
	if cmd != nil || calls != 0 {
		t.Fatal("w outside details must do nothing")
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd = updateProgress(m, w)
	if cmd == nil || !m.viewerBusy || calls != 1 {
		t.Fatal("w must dispatch an asynchronous viewer command")
	}
	m, duplicate := updateProgress(m, w)
	if duplicate != nil || calls != 1 {
		t.Fatal("repeated key presses must not start duplicate transfers")
	}
	m, _ = updateProgress(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = updateProgress(m, cmd())
	if m.viewerBusy || !strings.Contains(m.View(), "Opened SimScope") {
		t.Fatal("viewer completion must release busy state")
	}
	m, _ = updateProgress(m, viewerOpened{err: errors.New("browser unavailable")})
	if !strings.Contains(m.View(), "browser unavailable") || m.details == nil {
		t.Fatal("opening errors must leave details usable")
	}
}

func TestBrowserOpenerPassesURLAsOneArgument(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux opener test")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "url")
	if err := os.WriteFile(filepath.Join(dir, "xdg-open"), []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$BROWSER_CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("BROWSER_CAPTURE", capture)
	target := "http://viewer.example/?url=a%26b&team=$(invalid)"
	if err := openBrowser(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil || string(data) != target {
		t.Fatal("URL must be passed literally without shell interpolation")
	}
	t.Setenv("PATH", t.TempDir())
	if err := openBrowser(context.Background(), target); err == nil || strings.Contains(err.Error(), target) {
		t.Fatal("missing browser command must produce a safe error")
	}
}

func TestWSLBrowserOpenerAvoidsCommandShell(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("WSL opener selection runs on Linux")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "args")
	if err := os.WriteFile(filepath.Join(dir, "rundll32.exe"), []byte("#!/bin/sh\nprintf '%s\\n' \"$#\" \"$1\" \"$2\" > \"$BROWSER_CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("BROWSER_CAPTURE", capture)
	target := "http://viewer.example/?url=a%26b&team=$(invalid)"
	if err := openBrowser(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil || string(data) != "2\nurl.dll,FileProtocolHandler\n"+target+"\n" {
		t.Fatal("Windows browser handler must receive the complete literal URL")
	}
}
