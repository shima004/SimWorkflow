package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"simworkflow/internal/argo"
	"simworkflow/internal/artifacts"
	"simworkflow/internal/experiment"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/reproduction/testfixture"
	"simworkflow/internal/worker"
)

type retryBackend struct {
	mu          sync.Mutex
	env         *testfixture.Environment
	snapshots   map[string]*argo.Snapshot
	submissions []map[string]any
	writes      int
	failIndex   bool
}

func (b *retryBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/api/v1/workflows/argo") {
		if r.Method == http.MethodPost {
			var body struct {
				Workflow map[string]any `json:"workflow"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(400)
				return
			}
			b.submissions = append(b.submissions, body.Workflow)
			data, _ := json.Marshal(body.Workflow)
			var snapshot argo.Snapshot
			json.Unmarshal(data, &snapshot)
			snapshot.Metadata.Name = fmt.Sprintf("retry-%d", len(b.submissions))
			snapshot.Status.Phase = "Running"
			b.snapshots[snapshot.Metadata.Name] = &snapshot
			json.NewEncoder(w).Encode(snapshot)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/workflows/argo/")
		if s := b.snapshots[name]; s != nil {
			json.NewEncoder(w).Encode(s)
		} else {
			w.WriteHeader(404)
		}
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		prefix := "/fixture/" + r.URL.Query().Get("prefix")
		var result struct {
			XMLName  xml.Name `xml:"ListBucketResult"`
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
		}
		b.env.Objects.Mu.Lock()
		for key := range b.env.Objects.Data {
			if strings.HasPrefix(key, prefix) {
				result.Contents = append(result.Contents, struct {
					Key string `xml:"Key"`
				}{strings.TrimPrefix(key, "/fixture/")})
			}
		}
		b.env.Objects.Mu.Unlock()
		xml.NewEncoder(w).Encode(result)
		return
	}
	if r.Method == http.MethodPut {
		b.writes++
		if b.failIndex && strings.Contains(r.URL.Path, "/retries/") {
			w.WriteHeader(403)
			return
		}
	}
	b.env.Objects.ServeHTTP(w, r)
}

func newRetryBackend(t *testing.T) (*retryBackend, *experiment.Plan) {
	t.Helper()
	env := testfixture.New(t)
	b := &retryBackend{env: env, snapshots: map[string]*argo.Snapshot{}}
	server := httptest.NewServer(b)
	t.Cleanup(server.Close)
	env.Config.S3.Endpoint, env.Config.Argo.Server, env.Config.Argo.Namespace = server.URL, server.URL, "argo"
	locked, meta, err := reproduction.Capture(context.Background(), env.Config, env.Experiment(), env.Helper, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experiment.BuildPlan(locked)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Runs) != 8 {
		t.Fatal("fixture must contain eight runs")
	}
	meta.PlanSHA256 = reproduction.PlanSHA256(plan)
	meta.ConfigStorage = "s3-sequence"
	const prefix = "experiments/00000000000000000000000000000001"
	env.Objects.Set("/fixture/"+prefix+"/plan.json", testfixture.JSON(t, plan))
	env.Objects.Set("/fixture/"+prefix+"/reproduction.json", testfixture.JSON(t, meta))
	s := &argo.Snapshot{}
	s.Metadata.Name, s.Metadata.Namespace = "original", "argo"
	s.Metadata.Annotations = map[string]string{"simworkflow.io/result-prefix": prefix, "simworkflow.io/execution-id": strings.TrimPrefix(prefix, "experiments/")}
	s.Spec.ServiceAccountName = "original-executor"
	s.Status.Phase = "Failed"
	s.Status.Nodes = map[string]argo.Node{}
	for i, run := range plan.Runs {
		stage, phase := worker.Simulation, "Succeeded"
		switch i {
		case 1:
			phase = "Failed"
		case 2:
			phase = "Error"
		case 3:
			stage, phase = worker.Precompute, "Failed"
		case 4:
			stage = worker.Precompute
		case 5, 7:
			continue
		}
		s.Status.Nodes[run.ID] = argo.Node{Type: "Pod", TemplateName: run.ID + "-" + stage, Phase: phase}
	}
	// An earlier failed automatic retry must not hide its later successful Pod.
	s.Status.Nodes["old-attempt"] = argo.Node{Type: "Pod", TemplateName: plan.Runs[6].ID + "-simulation", DisplayName: "simulation(0)", Phase: "Failed"}
	n := s.Status.Nodes[plan.Runs[6].ID]
	n.DisplayName = "simulation(1)"
	s.Status.Nodes[plan.Runs[6].ID] = n
	b.snapshots["original"] = s
	return b, plan
}

func (b *retryBackend) plan(t *testing.T, name string) *experiment.Plan {
	t.Helper()
	prefix := b.snapshots[name].Metadata.Annotations["simworkflow.io/result-prefix"]
	var plan experiment.Plan
	if err := json.Unmarshal(b.env.Objects.Get("/fixture/"+prefix+"/plan.json"), &plan); err != nil {
		t.Fatal(err)
	}
	return &plan
}

func (b *retryBackend) finish(t *testing.T, name string, phases []string) {
	t.Helper()
	plan := b.plan(t, name)
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.snapshots[name]
	s.Status.Phase = "Failed"
	s.Status.Nodes = map[string]argo.Node{}
	for _, group := range experiment.RunSequences(plan.Runs) {
		for i, id := range group.Runs {
			phase := "Succeeded"
			for j, run := range plan.Runs {
				if run.ID == id && j < len(phases) {
					phase = phases[j]
				}
			}
			s.Status.Nodes[id] = argo.Node{Type: "Pod", Name: fmt.Sprintf("%s.%s(%d:%d)[1].simulation(1)", name, group.Name, i, i), TemplateName: "shared-simulation-test", DisplayName: "simulation(1)", Phase: phase}
		}
	}
}

func retryLog(t *testing.T, text string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, stage := range []string{worker.Precompute, worker.Simulation} {
		tw.WriteHeader(&tar.Header{Name: "logs/" + stage + "/rescue.log.7z", Mode: 0600, Size: int64(len(text))})
		tw.Write([]byte(text))
	}
	tw.Close()
	gz.Close()
	return out.Bytes()
}

func TestRetrySubmissionAndCombinedDownload(t *testing.T) {
	b, original := newRetryBackend(t)
	var out, errOut bytes.Buffer
	beforeWrites := b.writes
	for _, args := range [][]string{{"retry", "--dry-run", "original"}, {"retry", "--dry-run", "--include-unfinished", "original"}} {
		out.Reset()
		if err := runRemote(args, b.env.Config, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		count := "3"
		if len(args) == 4 {
			count = "6"
		}
		if !strings.Contains(out.String(), "Total runs:") || !strings.Contains(out.String(), count) {
			t.Fatal(out.String())
		}
	}
	if b.writes != beforeWrites || len(b.submissions) != 0 {
		t.Fatal("dry run performed writes")
	}
	if err := runRemote([]string{"retry", "--max-parallel-runs", "2", "--timeout", "2h", "original"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	first := b.plan(t, "retry-1")
	if len(first.Runs) != 3 || first.MaxParallelRuns != 2 || first.Timeout != "2h" {
		t.Fatal("wrong retry subset or overrides")
	}
	for i, run := range first.Runs {
		if run.ID != original.Runs[i+1].ID {
			t.Fatal("retry changed identity or selected a successful run")
		}
	}
	if b.snapshots["retry-1"].Spec.ServiceAccountName != "original-executor" {
		t.Fatal("executor account not inherited")
	}
	prefix := b.snapshots["retry-1"].Metadata.Annotations["simworkflow.io/result-prefix"]
	var meta reproduction.Metadata
	json.Unmarshal(b.env.Objects.Get("/fixture/"+prefix+"/reproduction.json"), &meta)
	if meta.Definition != nil || meta.Retry == nil || meta.Retry.RootWorkflow != "original" || meta.PlanSHA256 != reproduction.PlanSHA256(first) {
		t.Fatal("missing retry provenance")
	}
	// Both stages receive fresh scoped settings, rather than the old precompute data.
	for _, stage := range []string{worker.Precompute, worker.Simulation} {
		key := "/fixture/" + prefix + "/configs/" + stage + ".zip"
		data := b.env.Objects.Get(key)
		if len(data) == 0 {
			t.Fatal("missing new stage settings")
		}
		sha := sha256.Sum256(data)
		group := experiment.RunSequences(first.Runs)[0]
		config, err := worker.ReadRunConfigSource(context.Background(), worker.RunConfigSource{BundleURL: b.env.Config.S3.Endpoint + key, BundleSHA256: hex.EncodeToString(sha[:]), Sequence: group.Name, Index: "0", Stage: stage, ExecutionID: strings.TrimPrefix(prefix, "experiments/")})
		if err != nil || config.Run.ID != group.Runs[0] || config.TimeoutSeconds != 7200 {
			t.Fatal("retry worker settings did not preserve the selected run and timeout", err)
		}
		if (stage == worker.Precompute && (config.PrecomputeUpload == nil || config.PrecomputeDownload != nil)) || (stage == worker.Simulation && (config.PrecomputeUpload != nil || config.PrecomputeDownload == nil)) {
			t.Fatal("retry reused unscoped precompute settings")
		}
	}
	b.finish(t, "retry-1", []string{"Succeeded", "Failed", "Error"})
	if err := runRemote([]string{"retry", "retry-1"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	second := b.plan(t, "retry-2")
	if len(second.Runs) != 2 || second.Runs[0].ID != first.Runs[1].ID {
		t.Fatal("retry chain selected wrong runs")
	}
	b.finish(t, "retry-2", []string{"Succeeded", "Failed"})
	// A later retry of the original includes a newly failed repetition of a
	// previously successful retry. The successful attempt must remain selected.
	if err := runRemote([]string{"retry", "original"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	b.finish(t, "retry-3", []string{"Failed", "Failed", "Failed"})
	for name, snapshot := range b.snapshots {
		plan := b.plan(t, name)
		prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
		for _, run := range plan.Runs {
			score := float64(len(name))
			for _, stage := range []string{worker.Precompute, worker.Simulation} {
				key := "/fixture/" + prefix + "/" + run.ID
				if stage == worker.Precompute {
					key += "/precompute"
				}
				b.env.Objects.Set(key+"/summary.json", testfixture.JSON(t, struct {
					worker.Summary
					Attempt string `json:"attempt"`
				}{Summary: worker.Summary{Run: run, ExecutionID: strings.TrimPrefix(prefix, "experiments/"), Stage: stage, Score: &score}, Attempt: name}))
				b.env.Objects.Set(key+"/bundle.tar.gz", retryLog(t, name))
			}
			b.env.Objects.Set("/fixture/"+prefix+"/"+run.ID+"/precompute/data.zip", []byte(name))
		}
	}
	dest := filepath.Join(t.TempDir(), "combined")
	if err := runRemote([]string{"download", "--with-retries", "--include", "summary,precompute", "--output", dest, "retry-2"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var report artifacts.Report
	data, err := os.ReadFile(filepath.Join(dest, "download.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(data, &report)
	if report.Workflow != "original" || !report.WithRetries || len(report.Attempts) != 8 {
		t.Fatal("combined report has wrong root or coverage")
	}
	want := map[string]string{original.Runs[0].ID: "original", original.Runs[1].ID: "retry-1", original.Runs[2].ID: "retry-2", original.Runs[3].ID: "retry-3"}
	for id, name := range want {
		if report.Attempts[id].Workflow != name {
			t.Fatalf("%s selected %s, want %s", id, report.Attempts[id].Workflow, name)
		}
		for _, stage := range []string{worker.Precompute, worker.Simulation} {
			data, err := os.ReadFile(filepath.Join(dest, id, stage, "summary.json"))
			if err != nil || !strings.Contains(string(data), name) {
				t.Fatal("mixed summary attempts", err)
			}
		}
		pre, _ := os.ReadFile(filepath.Join(dest, id, "precompute/data.zip"))
		if string(pre) != name {
			t.Fatal("mixed precompute attempts")
		}
	}
	out.Reset()
	if err := runRemote([]string{"results", "--with-retries", "original"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var combined []struct {
		worker.Summary
		SourceWorkflow string `json:"sourceWorkflow"`
	}
	if err := json.Unmarshal(out.Bytes(), &combined); err != nil || len(combined) != 8 {
		t.Fatal("combined results unavailable", err)
	}
	for _, r := range combined {
		if r.SourceWorkflow != report.Attempts[r.Run.ID].Workflow || r.Run.Configuration.ID == "" {
			t.Fatal("results and download selected different attempts")
		}
		if r.Score == nil || *r.Score != float64(len(r.SourceWorkflow)) || len(r.Run.Configuration.Groups) != 2 {
			t.Fatal("combined results lost scores or grouped modules")
		}
	}
	if err := runRemote([]string{"download", "--with-retries", "--run", original.Runs[1].ID, "--file", "logs/simulation/rescue.log.7z", "--stage", "simulation", "--output", dest, "original"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(dest, original.Runs[1].ID, "simulation/logs/rescue.log.7z"))
	if err != nil || string(log) != "retry-1" {
		t.Fatal("file selector used wrong attempt", err)
	}
	data, _ = os.ReadFile(filepath.Join(dest, "download.json"))
	json.Unmarshal(data, &report)
	for _, file := range report.Downloaded {
		if file.RunID != "" && (file.Workflow != report.Attempts[file.RunID].Workflow || file.ExecutionID != report.Attempts[file.RunID].ExecutionID) {
			t.Fatal("missing per-file provenance")
		}
	}
	bundle := filepath.Join(t.TempDir(), "retry.zip")
	if err := runRemote([]string{"export", "--output", bundle, "retry-1"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal("retry could not be exported", err)
	}
	archive, err := reproduction.Open(bundle)
	if err != nil {
		t.Fatal("retry reproduction archive is invalid", err)
	}
	if archive.Plan.TotalRuns != 3 || archive.Manifest.Metadata.Definition != nil || archive.Manifest.Metadata.Retry == nil {
		archive.Close()
		t.Fatal("retry export lost the subset or its origin")
	}
	archive.Close()
	if strings.Contains(string(data), "X-Amz-") || strings.Contains(string(data), "fixture-secret") {
		t.Fatal("manifest leaked credentials")
	}
	// A newly successful attempt cannot be added to a directory containing an
	// older attempt's summary. The existing manifest and files stay untouched.
	b.finish(t, "retry-3", []string{"Succeeded", "Succeeded", "Succeeded"})
	before := append([]byte(nil), data...)
	if err := runRemote([]string{"download", "--with-retries", "--include", "summary", "--output", dest, "original"}, b.env.Config, &out, &errOut); err == nil || !strings.Contains(err.Error(), "choose a new --output") {
		t.Fatal("changed attempts were silently mixed", err)
	}
	after, _ := os.ReadFile(filepath.Join(dest, "download.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("rejected download replaced manifest")
	}
	// Ordinary download still retrieves just the specified Workflow.
	plain := filepath.Join(t.TempDir(), "plain")
	if err := runRemote([]string{"download", "--include", "summary", "--stage", "simulation", "--output", plain, "original"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(plain, original.Runs[1].ID, "simulation/summary.json"))
	if !strings.Contains(string(data), "original") {
		t.Fatal("ordinary download unexpectedly merged retries")
	}
}

func TestRetryRejectsUnsafeOrUnfinishedSources(t *testing.T) {
	b, _ := newRetryBackend(t)
	var out, errOut bytes.Buffer
	for _, args := range [][]string{{"retry", "--max-parallel-runs", "0", "original"}, {"retry", "--timeout", "0s", "original"}, {"retry", "--timeout", "168h", "original"}} {
		if err := runRemote(args, b.env.Config, &out, &errOut); err == nil {
			t.Fatal("invalid retry accepted", args)
		}
	}
	b.snapshots["original"].Status.Phase = "Running"
	if err := runRemote([]string{"retry", "original"}, b.env.Config, &out, &errOut); err == nil {
		t.Fatal("running workflow retried")
	}
	b.snapshots["original"].Status.Phase = "Succeeded"
	b.snapshots["original"].Status.Nodes = nil
	if err := runRemote([]string{"retry", "original"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if len(b.submissions) != 0 {
		t.Fatal("retry submitted an invalid or fully successful experiment")
	}
	if !strings.Contains(out.String(), "nothing submitted") {
		t.Fatal(out.String())
	}
}

func TestRetryRejectsChangedSavedPlanAndMissingRelatedWorkflow(t *testing.T) {
	b, plan := newRetryBackend(t)
	var out, errOut bytes.Buffer
	prefix := b.snapshots["original"].Metadata.Annotations["simworkflow.io/result-prefix"]
	original := b.env.Objects.Get("/fixture/" + prefix + "/plan.json")
	copy := *plan
	copy.Timeout = "2h"
	b.env.Objects.Set("/fixture/"+prefix+"/plan.json", testfixture.JSON(t, copy))
	if err := runRemote([]string{"retry", "original"}, b.env.Config, &out, &errOut); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("changed saved plan was retried", err)
	}
	if len(b.submissions) != 0 {
		t.Fatal("tampered plan reached Argo")
	}
	b.env.Objects.Set("/fixture/"+prefix+"/plan.json", original)
	if err := runRemote([]string{"retry", "original"}, b.env.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	delete(b.snapshots, "retry-1")
	b.mu.Unlock()
	dest := filepath.Join(t.TempDir(), "missing-related")
	if err := runRemote([]string{"download", "--with-retries", "--output", dest, "original"}, b.env.Config, &out, &errOut); err == nil {
		t.Fatal("missing retry was silently ignored")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("incomplete discovery created output")
	}
}

func TestCombinedSelectionOrderAndPhase(t *testing.T) {
	// Keep the aggregate state useful when a failed root is fully repaired.
	a := map[string]artifacts.Attempt{"one": {Phase: "Succeeded"}, "two": {Phase: "Succeeded"}}
	if combinedPhase(a) != "Succeeded" {
		t.Fatal("successful combined results still marked failed")
	}
	a["two"] = artifacts.Attempt{Phase: "Failed"}
	if combinedPhase(a) != "Failed" {
		t.Fatal("failed attempt hidden")
	}
	a["two"] = artifacts.Attempt{Phase: "Unfinished"}
	if combinedPhase(a) != "Incomplete" {
		t.Fatal("unfinished attempt hidden")
	}
}

func TestRetryFindsTemplateErrorsBeforePodCreation(t *testing.T) {
	for _, sequenced := range []bool{false, true} {
		t.Run(fmt.Sprint("sequence=", sequenced), func(t *testing.T) {
			b, plan := newRetryBackend(t)
			s := b.snapshots["original"]
			run := plan.Runs[5]
			root := run.ID
			if sequenced {
				s.Metadata.Annotations["simworkflow.io/run-sequences"] = "v1"
				attachSequenceNodes(s, plan)
				root = s.RunNodeNames[run.ID]
			}
			s.Status.Nodes["wrapper-error"] = argo.Node{Type: "Steps", Name: "original." + root, Phase: "Error"}
			e, err := loadSavedExecution(context.Background(), b.env.Config, "original")
			if err != nil {
				t.Fatal(err)
			}
			if executionRunPhase(e, run.ID) != "Error" {
				t.Fatal("pre-Pod template failure was omitted")
			}
		})
	}
}

func TestRetryReportsSubmittedWorkflowWhenIndexWriteFails(t *testing.T) {
	b, plan := newRetryBackend(t)
	b.failIndex = true
	var out, errOut bytes.Buffer
	err := runRemote([]string{"retry", "original"}, b.env.Config, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "was submitted") || !strings.Contains(out.String(), "Submitted retry-1") {
		t.Fatal("successful submission was hidden after index failure", err, out.String())
	}
	if len(b.submissions) != 1 {
		t.Fatal("retry duplicated a submission")
	}
	requested, err := loadSavedExecution(context.Background(), b.env.Config, "retry-1")
	if err != nil {
		t.Fatal(err)
	}
	family, err := retryFamily(context.Background(), b.env.Config, requested)
	if err != nil || len(family) != 2 {
		t.Fatal("explicit retry could not recover without index", err)
	}
	if combinedAttempts(family)[plan.Runs[1].ID].Workflow != "retry-1" {
		t.Fatal("unindexed explicit retry was omitted")
	}
}
