package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simworkflow/internal/argo"
	"simworkflow/internal/config"
	"simworkflow/internal/connections"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

func TestStatusElapsedFromArgoTimestamps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"metadata":{"name":"elapsed-test"},"status":{"phase":"Succeeded","startedAt":"2026-10-25T01:00:00Z","finishedAt":"2026-10-25T01:02:03Z"}}`)
	}))
	defer server.Close()
	c := config.Config{Argo: config.Argo{Server: server.URL, Namespace: "argo"}, HTTPTimeout: time.Second}
	frame := loadProgress(context.Background(), c, "elapsed-test")().(progressFrame)
	if frame.err != nil || frame.startedAt.IsZero() || frame.finishedAt.IsZero() {
		t.Fatal("TUI must retain Workflow timestamps from Argo")
	}
	for _, args := range [][]string{{"status", "elapsed-test"}, {"status", "--watch", "--plain", "elapsed-test"}} {
		var out, errOut bytes.Buffer
		if err := runRemote(args, c, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Elapsed: 2m3s") {
			t.Fatalf("missing Workflow duration: %s", out.String())
		}
	}
}

func TestIndividualRunElapsedUsesCurrentStageAndAttempt(t *testing.T) {
	plan := detailsPlan(t)
	run := plan.Runs[0]
	start := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	store := progressTestStore(func(r *http.Request) (*http.Response, error) {
		return progressResponse(r, nil, 404), nil
	})
	for _, tc := range []struct {
		name, wantStage, wantElapsed string
		nodes                        map[string]argo.Node
	}{
		{"not scheduled", "-", "-", nil},
		{"precompute", "precompute", "5m0s", map[string]argo.Node{
			"pre": {Type: "Pod", TemplateName: run.ID + "-precompute", Phase: "Running", StartedAt: start},
		}},
		{"simulation excludes precompute", "simulation", "1m0s", map[string]argo.Node{
			"pre": {Type: "Pod", TemplateName: run.ID + "-precompute", Phase: "Succeeded", StartedAt: start, FinishedAt: start.Add(3 * time.Minute)},
			"sim": {Type: "Pod", TemplateName: run.ID + "-simulation", Phase: "Running", StartedAt: start.Add(4 * time.Minute)},
		}},
		{"completed", "simulation", "30s", map[string]argo.Node{
			"sim": {Type: "Pod", TemplateName: run.ID + "-simulation", Phase: "Succeeded", StartedAt: start.Add(time.Minute), FinishedAt: start.Add(90 * time.Second)},
		}},
		{"failed", "simulation", "20s", map[string]argo.Node{
			"sim": {Type: "Pod", TemplateName: run.ID + "-simulation", Phase: "Failed", StartedAt: start.Add(time.Minute), FinishedAt: start.Add(80 * time.Second)},
		}},
		{"retry starts fresh", "simulation", "10s", map[string]argo.Node{
			"sim0": {Type: "Pod", TemplateName: run.ID + "-simulation", DisplayName: "simulation(0)", Phase: "Failed", StartedAt: start, FinishedAt: start.Add(time.Minute)},
			"sim1": {Type: "Pod", TemplateName: run.ID + "-simulation", DisplayName: "simulation(1)", Phase: "Running", StartedAt: start.Add(290 * time.Second)},
		}},
		{"pending retry", "simulation", "-", map[string]argo.Node{
			"sim0": {Type: "Pod", TemplateName: run.ID + "-simulation", DisplayName: "simulation(0)", Phase: "Failed", StartedAt: start, FinishedAt: start.Add(time.Minute)},
			"sim1": {Type: "Pod", TemplateName: run.ID + "-simulation", DisplayName: "simulation(1)", Phase: "Pending"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := progressSnapshot(plan)
			snapshot.Status.Phase, snapshot.Status.Nodes = "Running", tc.nodes
			row := readRunProgress(context.Background(), store, snapshot, run)
			var out bytes.Buffer
			if _, err := writeProgressRows(&out, []runProgressRow{row}, start.Add(5*time.Minute)); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			cells := strings.Fields(lines[1])
			if !strings.Contains(lines[0], "ELAPSED") || cells[3] != tc.wantStage || cells[6] != tc.wantElapsed {
				t.Fatalf("wrong per-stage elapsed time: %s", out.String())
			}
		})
	}
}

func TestSharedStageNodesKeepRunsAndRetryAttemptsSeparate(t *testing.T) {
	plan := detailsPlan(t)
	run := plan.Runs[0]
	start := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	snapshot := progressSnapshot(plan)
	snapshot.Metadata.Name = "shared-experiment"
	prefix := "shared-experiment." + run.ID
	snapshot.Status.Nodes = map[string]argo.Node{
		"pre":             {Name: prefix + "[0].precompute", Type: "Pod", TemplateName: "shared-precompute-abc", Phase: "Succeeded", StartedAt: start},
		"sim0":            {Name: prefix + "[1].simulation(0)", Type: "Pod", TemplateName: "shared-simulation-abc", DisplayName: "simulation(0)", Phase: "Failed", StartedAt: start},
		"sim1":            {Name: prefix + "[1].simulation(1)", Type: "Pod", TemplateName: "shared-simulation-abc", DisplayName: "simulation(1)", Phase: "Running", StartedAt: start.Add(4 * time.Minute)},
		"other":           {Name: "shared-experiment.other-run[1].simulation(9)", Type: "Pod", TemplateName: "shared-simulation-abc", DisplayName: "simulation(9)", Phase: "Succeeded"},
		"different-group": {Name: prefix + "[0].simulation(9)", Type: "Pod", TemplateName: "shared-simulation-abc", DisplayName: "simulation(9)", Phase: "Succeeded"},
		"malformed":       {Name: "(9)", Type: "Pod", TemplateName: "shared-simulation-abc", DisplayName: "simulation(9)", Phase: "Succeeded"},
	}
	store := progressTestStore(func(r *http.Request) (*http.Response, error) { return progressResponse(r, nil, 404), nil })
	row := readRunProgress(context.Background(), store, snapshot, run)
	if row.stage != "simulation" || row.phase != "Running" || workflowElapsed(row.startedAt, row.finishedAt, start.Add(5*time.Minute), row.phase) != "1m0s" {
		t.Fatal("shared templates must retain per-run, per-attempt elapsed time", row)
	}
	node := snapshot.Status.Nodes["sim1"]
	node.Phase, node.StartedAt = "Pending", time.Time{}
	snapshot.Status.Nodes["sim1"] = node
	latest, found := latestStageNode(snapshot, run.ID, "simulation")
	if !found || latest.Phase != "Pending" {
		t.Fatal("a pending retry must hide its failed predecessor")
	}
	for _, suffix := range []string{"", "(0)", "(1)"} {
		node.Name = prefix + "[1].simulation" + suffix
		if !matchesStageNode(snapshot, node, run.ID, "simulation") {
			t.Fatal("valid Argo step node not recognized", node.Name)
		}
	}
}

func TestSequenceProgressKeepsMembersAndRetriesSeparate(t *testing.T) {
	plan := detailsPlan(t)
	if len(plan.Runs) < 2 {
		t.Fatal("test requires multiple runs")
	}
	snapshot := progressSnapshot(plan)
	snapshot.Metadata.Name = "loop-test"
	snapshot.Metadata.Annotations["simworkflow.io/run-sequences"] = "v1"
	groups := experiment.RunSequences(plan.Runs)
	if len(groups[0].Runs) < 2 {
		t.Fatal("test requires multiple members")
	}
	start := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	root := "loop-test." + groups[0].Name + "(0:0)"
	snapshot.Status.Nodes = map[string]argo.Node{
		"pre":     {Type: "Pod", Name: root + "[0].precompute", TemplateName: "shared-precompute-abc", Phase: "Succeeded", StartedAt: start, FinishedAt: start.Add(time.Minute)},
		"sim0":    {Type: "Pod", Name: root + "[1].simulation(0)", TemplateName: "shared-simulation-abc", DisplayName: "simulation(0)", Phase: "Failed", StartedAt: start},
		"sim1":    {Type: "Pod", Name: root + "[1].simulation(1)", TemplateName: "shared-simulation-abc", DisplayName: "simulation(1)", Phase: "Running", StartedAt: start.Add(4 * time.Minute)},
		"other":   {Type: "Pod", Name: "loop-test." + groups[0].Name + "(1:1)[1].simulation(0)", TemplateName: "shared-simulation-abc", DisplayName: "simulation(0)", Phase: "Succeeded", StartedAt: start, FinishedAt: start.Add(2 * time.Minute)},
		"foreign": {Type: "Pod", Name: "other-workflow." + groups[0].Name + "(0:0)[1].simulation(9)", TemplateName: "shared-simulation-abc", DisplayName: "simulation(9)", Phase: "Failed"},
	}
	attachSequenceNodes(snapshot, plan)
	first, ok := latestStageNode(snapshot, groups[0].Runs[0], worker.Simulation)
	if !ok || first.Phase != "Running" || workflowElapsed(first.StartedAt, first.FinishedAt, start.Add(5*time.Minute), first.Phase) != "1m0s" {
		t.Fatal("loop retry elapsed time mixed across members", first)
	}
	second, ok := latestStageNode(snapshot, groups[0].Runs[1], worker.Simulation)
	if !ok || second.Phase != "Succeeded" || second.FinishedAt.Sub(second.StartedAt) != 2*time.Minute {
		t.Fatal("wrong loop member selected", second)
	}
	first.Phase = "Pending"
	first.StartedAt = time.Time{}
	snapshot.Status.Nodes["sim1"] = first
	attachSequenceNodes(snapshot, plan)
	latest, ok := latestStageNode(snapshot, groups[0].Runs[0], worker.Simulation)
	if !ok || latest.Phase != "Pending" {
		t.Fatal("pending loop retry must hide previous attempt")
	}
}

func TestRunProgressUsesCurrentAttemptAndHandlesOlderWorkers(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/precompute-baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experiment.BuildPlan(e)
	if err != nil {
		t.Fatal(err)
	}
	run := plan.Runs[0]
	for _, tc := range []struct {
		name                                  string
		activity                              string
		progressStatus                        int
		wrongIdentity, retryPending, terminal bool
		want, absent                          string
		wantRetry                             bool
	}{
		{name: "live", want: "45/300 (15%)"},
		{name: "collecting score", activity: "reading-score", want: "Reading score"},
		{name: "compressing", activity: "compressing", want: "Compressing"},
		{name: "uploading", activity: "uploading-results", want: "Uploading results"},
		{name: "unknown activity is hidden", activity: "\x1b[31mUntrusted", absent: "Untrusted"},
		{name: "terminal clears old activity", activity: "compressing", terminal: true, absent: "Compressing"},
		{name: "wrong execution ignored", wrongIdentity: true, absent: "45/300"},
		{name: "pending retry hides previous progress", retryPending: true, want: "Pending", absent: "45/300"},
		{name: "older worker uses final summary", progressStatus: 404, terminal: true, want: "6.5", absent: "45/300"},
		{name: "progress unavailable is nonfatal", progressStatus: 503, want: "unavailable", wantRetry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			score := 4.5
			progress := worker.Progress{Activity: tc.activity, ExecutionID: "execution", RunID: run.ID, Stage: worker.Simulation, Phase: "Running", CompletedSteps: 45, TotalSteps: 300, Score: &score, UpdatedAt: now}
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
			retry, err := writeRunProgress(context.Background(), store, &snapshot, &out)
			if err != nil {
				t.Fatal(err)
			}
			if retry != tc.wantRetry {
				t.Fatalf("retry=%v, want %v", retry, tc.wantRetry)
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

type progressTransport func(*http.Request) (*http.Response, error)

func (f progressTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func progressTestStore(transport progressTransport) *storage.Store {
	c := config.S3{Endpoint: "http://s3.invalid", Region: "us-east-1", Bucket: "results", AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true}
	return &storage.Store{Client: connections.NewS3(c, &http.Client{Transport: transport}), Bucket: c.Bucket}
}

func progressResponse(r *http.Request, value any, status int) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: r}
}

func progressSnapshot(plan *experiment.Plan) *argo.Snapshot {
	s := new(argo.Snapshot)
	s.Metadata.Annotations = map[string]string{"simworkflow.io/result-prefix": "experiments/test", "simworkflow.io/execution-id": "execution"}
	s.Status.Phase = "Succeeded"
	s.Status.Nodes = map[string]argo.Node{}
	for _, run := range plan.Runs {
		s.Status.Nodes[run.ID] = argo.Node{Type: "Pod", TemplateName: run.ID + "-simulation", Phase: "Succeeded", StartedAt: time.Now().Add(-time.Minute)}
	}
	return s
}

func TestProgressPlanAndObjectsHaveIndependentDeadlines(t *testing.T) {
	plan := detailsPlan(t)
	plan.Runs = plan.Runs[:1]
	run := plan.Runs[0]
	var deadlines []time.Time
	score := 7.4
	store := progressTestStore(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("each request must have a timeout")
		}
		deadlines = append(deadlines, deadline)
		// A little time spent on each object must not shorten the next deadline.
		time.Sleep(20 * time.Millisecond)
		switch {
		case strings.HasSuffix(r.URL.Path, "/plan.json"):
			return progressResponse(r, plan, 200), nil
		case strings.HasSuffix(r.URL.Path, "/progress.json"):
			return progressResponse(r, worker.Progress{ExecutionID: "execution", RunID: run.ID, Stage: "simulation", UpdatedAt: time.Now(), CompletedSteps: 250, TotalSteps: 250}, 200), nil
		default:
			return progressResponse(r, worker.Summary{ExecutionID: "execution", Run: run, Stage: "simulation", Score: &score}, 200), nil
		}
	})
	var out bytes.Buffer
	retry, err := writeRunProgress(context.Background(), store, progressSnapshot(plan), &out)
	if err != nil || retry || len(deadlines) != 3 {
		t.Fatalf("unexpected read result: retry=%v, err=%v, requests=%d", retry, err, len(deadlines))
	}
	if !deadlines[1].After(deadlines[0]) || !deadlines[2].After(deadlines[1]) {
		t.Fatal("plan, progress and summary must not share one deadline")
	}
	if !strings.Contains(out.String(), "250/250 (100%)") || !strings.Contains(out.String(), "7.4") {
		t.Fatal(out.String())
	}
}

func TestProgressReadsRowsConcurrentlyInPlanOrder(t *testing.T) {
	plan := detailsPlan(t)
	var active, peak atomic.Int32
	var blocked atomic.Bool
	released := make(chan struct{})
	first, last := plan.Runs[0].ID, plan.Runs[len(plan.Runs)-1].ID
	store := progressTestStore(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		parts := strings.Split(r.URL.Path, "/")
		id := parts[len(parts)-2]
		if strings.HasSuffix(r.URL.Path, "/progress.json") {
			if id == last {
				close(released)
			}
			if id == first {
				select {
				case <-released:
				case <-time.After(time.Second):
					blocked.Store(true)
				}
			}
			time.Sleep(5 * time.Millisecond)
			return progressResponse(r, worker.Progress{ExecutionID: "execution", RunID: id, Stage: "simulation", UpdatedAt: time.Now(), CompletedSteps: 250, TotalSteps: 250}, 200), nil
		}
		return progressResponse(r, nil, 404), nil
	})
	var out bytes.Buffer
	retry, err := writePlannedRunProgress(context.Background(), store, progressSnapshot(plan), plan, &out)
	if err != nil || retry {
		t.Fatalf("unexpected error: %v, retry=%v", err, retry)
	}
	if blocked.Load() || peak.Load() <= 1 || peak.Load() > 8 {
		t.Fatalf("row reads must be parallel and bounded: peak=%d, blocked=%v", peak.Load(), blocked.Load())
	}
	previous := -1
	for _, run := range plan.Runs {
		position := strings.Index(out.String(), run.ID)
		if position <= previous {
			t.Fatal("rows must retain plan order despite concurrent reads")
		}
		previous = position
	}
}

func TestFinishedProgressRetriesTransientReadsAndRecovers(t *testing.T) {
	for _, failedObject := range []string{"progress", "summary"} {
		t.Run(failedObject, func(t *testing.T) {
			plan := detailsPlan(t)
			plan.Runs = plan.Runs[:1]
			run := plan.Runs[0]
			score := 7.472686137386395
			var fail atomic.Bool
			fail.Store(true)
			store := progressTestStore(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/"+failedObject+".json") && fail.Swap(false) {
					return progressResponse(r, nil, 503), nil
				}
				if strings.HasSuffix(r.URL.Path, "/progress.json") {
					return progressResponse(r, worker.Progress{ExecutionID: "execution", RunID: run.ID, Stage: "simulation", UpdatedAt: time.Now(), CompletedSteps: 250, TotalSteps: 250}, 200), nil
				}
				return progressResponse(r, worker.Summary{ExecutionID: "execution", Run: run, Stage: "simulation", Score: &score}, 200), nil
			})
			var out bytes.Buffer
			retry, err := writePlannedRunProgress(context.Background(), store, progressSnapshot(plan), plan, &out)
			if err != nil || !retry {
				t.Fatalf("finished workflow must retry transient %s failures", failedObject)
			}
			out.Reset()
			retry, err = writePlannedRunProgress(context.Background(), store, progressSnapshot(plan), plan, &out)
			if err != nil || retry || strings.Contains(out.String(), "unavailable") || !strings.Contains(out.String(), "250/250 (100%)") || !strings.Contains(out.String(), "7.47269") {
				t.Fatalf("finished results did not recover: %s", out.String())
			}
		})
	}
}
