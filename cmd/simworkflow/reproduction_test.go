package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"go.yaml.in/yaml/v3"
	"simworkflow/internal/argo"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/maparchive"
	"simworkflow/internal/mapcatalog"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/reproduction/testfixture"
	"simworkflow/internal/worker"
)

type reproductionArgo struct {
	mu       sync.Mutex
	snapshot argo.Snapshot
	submits  int
}

func (s *reproductionArgo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodPost {
		var envelope struct {
			Workflow json.RawMessage `json:"workflow"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			w.WriteHeader(400)
			return
		}
		if err := json.Unmarshal(envelope.Workflow, &s.snapshot); err != nil {
			w.WriteHeader(400)
			return
		}
		s.snapshot.Metadata.Name = "fixture-workflow"
		s.snapshot.Status.Phase = "Succeeded"
		s.submits++
	}
	json.NewEncoder(w).Encode(s.snapshot)
}
func TestReproductionCommandsRoundTripAfterSourceDisappears(t *testing.T) {
	previous := prepareReproduction
	prepareReproduction = reproduction.Capture
	defer func() { prepareReproduction = previous }()
	source := testfixture.New(t)
	sourceArgo := new(reproductionArgo)
	argoServer := httptest.NewServer(sourceArgo)
	defer argoServer.Close()
	source.Config.Argo.Server = argoServer.URL
	source.Config.Argo.Namespace = "argo"
	mapFile := filepath.Join(t.TempDir(), "map.zip")
	if err := os.WriteFile(mapFile, source.Map, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := maparchive.Inspect(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	for i, version := range []string{"v1", "v2"} {
		key := "maps/fixture-map/" + version + "/"
		metadata := mapcatalog.Metadata{Schema: mapcatalog.Schema, Name: "fixture-map", Version: version, Source: "s3://fixture/" + key + "map.zip", Info: info, RegisteredAt: time.Unix(int64(100+i), 0).UTC()}
		source.Objects.Set("/fixture/"+key+"metadata.json", testfixture.JSON(t, metadata))
		source.Objects.Set("/fixture/"+key+"map.zip", source.Map)
	}
	input := source.Experiment()
	input.Spec.Conditions.Maps = []experiment.Map{{Name: "fixture-map", Versions: []string{"v1", "v2"}}}
	definition, err := yaml.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "experiment.yaml")
	if err := os.WriteFile(file, definition, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := runRemote([]string{"submit", "-f", file, "--helper-image", source.Helper}, source.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "16 runs") {
		t.Fatal("wrong number of grouped runs submitted")
	}
	sourceArgo.mu.Lock()
	snapshot := sourceArgo.snapshot
	sourceArgo.mu.Unlock()
	prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
	var original experiment.Plan
	if err := json.Unmarshal(source.Objects.Get("/fixture/"+prefix+"/plan.json"), &original); err != nil {
		t.Fatal(err)
	}
	versions := map[string]bool{}
	for _, run := range original.Runs {
		versions[run.Map.Version] = true
		if run.Map.Name != "fixture-map" {
			t.Fatal("version expansion changed the map name")
		}
	}
	if !versions["v1"] || !versions["v2"] || len(versions) != 2 {
		t.Fatal("submission lost requested map versions")
	}
	var capture reproduction.Metadata
	if err := json.Unmarshal(source.Objects.Get("/fixture/"+prefix+"/reproduction.json"), &capture); err != nil || capture.PlanSHA256 != reproduction.PlanSHA256(&original) {
		t.Fatal("submission did not save reproduction provenance")
	}
	if capture.ConfigStorage != "s3-sequence" || !capture.HelperConfigSequence || !capture.HelperConfigBundle {
		t.Fatal("compatible helper must automatically use stage bundles")
	}
	for _, stage := range []string{worker.Precompute, worker.Simulation} {
		key := "/fixture/" + prefix + "/configs/" + stage + ".zip"
		data := source.Objects.Get(key)
		if len(data) == 0 {
			t.Fatal("stage bundle not saved")
		}
		sha := sha256.Sum256(data)
		c, err := worker.ReadRunConfigSource(context.Background(), worker.RunConfigSource{BundleURL: source.S3.URL + key, BundleSHA256: hex.EncodeToString(sha[:]), Sequence: experiment.SequenceName(original.Runs[0]), Index: "0", Stage: stage, ExecutionID: snapshot.Metadata.Annotations["simworkflow.io/execution-id"]})
		if err != nil || c.Run.ID != original.Runs[0].ID || len(c.Run.Configuration.Groups) != 2 {
			t.Fatal("saved bundle was not usable by a worker", err)
		}
		if len(source.Objects.Get("/fixture/"+prefix+"/configs/"+original.Runs[0].ID+"/"+stage+".json")) != 0 {
			t.Fatal("bundled mode must not create per-run configuration objects")
		}
	}
	bundle := filepath.Join(t.TempDir(), "experiment.zip")
	if err := runRemote([]string{"export", "--output", bundle, "fixture-workflow"}, source.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	archive, err := reproduction.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := experiment.ReadFile(filepath.Join(archive.Directory, "experiment.yaml"))
	archive.Close()
	if err != nil {
		t.Fatal("exported versions YAML is invalid", err)
	}
	preview, err := experiment.BuildPlan(normalized)
	if err != nil || preview.TotalRuns != 16 {
		t.Fatal("exported YAML lost map versions", err)
	}
	// Reproduction has no dependency on any original service.
	source.Registry.Close()
	source.S3.Close()
	argoServer.Close()
	target := testfixture.NewTarget(t)
	targetArgo := new(reproductionArgo)
	targetServer := httptest.NewServer(targetArgo)
	defer targetServer.Close()
	target.Config.Argo.Server = targetServer.URL
	target.Config.Argo.Namespace = "argo"
	if err := runRemote([]string{"reproduce", "--bundle", bundle, "--name", "restored"}, target.Config, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	targetArgo.mu.Lock()
	after := targetArgo.snapshot
	submits := targetArgo.submits
	targetArgo.mu.Unlock()
	if submits != 1 {
		t.Fatal("reproduce must submit exactly one workflow")
	}
	var metadata reproduction.Metadata
	key := "/fixture/" + after.Metadata.Annotations["simworkflow.io/result-prefix"] + "/reproduction.json"
	if err := json.Unmarshal(target.Objects.Get(key), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Origin == nil || metadata.Origin.Workflow != "fixture-workflow" || len(metadata.Origin.Runs) != len(original.Runs) {
		t.Fatal("original/new run mapping was not recorded")
	}
	if metadata.ConfigStorage != "s3-sequence" {
		t.Fatal("reproduction lost configuration mode")
	}
	for _, template := range after.Spec.Templates {
		for _, container := range template.InitContainers {
			if !strings.Contains(container.Image, "/simworkflow/reproduced@sha256:") {
				t.Fatal("reproduction still depends on original helper registry")
			}
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(bad, []byte("corrupt"), 0600)
	target.Objects.Mu.Lock()
	requestsBefore := target.Objects.Requests
	target.Objects.Mu.Unlock()
	err = runRemote([]string{"reproduce", "--bundle", bad, "--name", "invalid"}, target.Config, io.Discard, io.Discard)
	target.Objects.Mu.Lock()
	requestsAfter := target.Objects.Requests
	target.Objects.Mu.Unlock()
	if err == nil || requestsAfter != requestsBefore {
		t.Fatal("invalid bundle must fail before service writes")
	}
}

func TestBundleModeRejectsLegacyHelperBeforeWriting(t *testing.T) {
	err := submitPlan(context.Background(), config.Config{}, nil, "old-helper:v1", "simworkflow", "s3-bundle", "submit", "", reproduction.Metadata{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "helper does not advertise") {
		t.Fatal("legacy helper must fail before configuration uploads", err)
	}
}
