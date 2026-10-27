package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"
	"simworkflow/internal/argo"
	"simworkflow/internal/experiment"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/reproduction/testfixture"
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
	definition, err := yaml.Marshal(source.Experiment())
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
	if !strings.Contains(out.String(), "8 runs") {
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
	var capture reproduction.Metadata
	if err := json.Unmarshal(source.Objects.Get("/fixture/"+prefix+"/reproduction.json"), &capture); err != nil || capture.PlanSHA256 != reproduction.PlanSHA256(&original) {
		t.Fatal("submission did not save reproduction provenance")
	}
	bundle := filepath.Join(t.TempDir(), "experiment.zip")
	if err := runRemote([]string{"export", "--output", bundle, "fixture-workflow"}, source.Config, &out, &errOut); err != nil {
		t.Fatal(err)
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
