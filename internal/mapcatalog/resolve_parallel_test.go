package mapcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

func TestResolveParallelLookupsPreserveOrderAndDeduplicateAliases(t *testing.T) {
	c, _ := catalogFixture(t)
	base, err := c.Register(context.Background(), inputDirectory(t), RegisterOptions{Name: "base", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]Metadata{}
	input, err := experiment.ReadFile("../../examples/experiments/python-module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	input.Spec.Conditions.Maps = nil
	for i := range 16 {
		name := fmt.Sprintf("map-%02d", i)
		m := base
		m.Name = name
		m.Source = "s3://results/maps/" + name + "/v1/map.zip"
		metadata["/results/maps/"+name+"/v1/metadata.json"] = m
		input.Spec.Conditions.Maps = append(input.Spec.Conditions.Maps, experiment.Map{Name: name, Versions: []string{"v1"}})
	}
	input.Spec.Conditions.Maps = append(input.Spec.Conditions.Maps, experiment.Map{Name: "alias", Ref: "map-00", Versions: []string{"v1"}})
	ready := make(chan struct{}, 32)
	release := make(chan struct{})
	var calls, active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		calls.Add(1)
		ready <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if m, ok := metadata[r.URL.Path]; ok {
			json.NewEncoder(w).Encode(m)
		} else {
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c.Store = storage.New(config.Config{S3: config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "test-key", SecretAccessKey: "test-secret", ForcePathStyle: true}, HTTPTimeout: 5 * time.Second})
	type result struct {
		e   *experiment.Experiment
		err error
	}
	done := make(chan result, 1)
	go func() { e, err := c.Resolve(context.Background(), input); done <- result{e, err} }()
	for range 8 {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("catalog lookups remained sequential")
		}
	}
	close(release)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if peak.Load() != 8 || calls.Load() != 16 {
		t.Fatal("lookups were not bounded or aliases were fetched twice", peak.Load(), calls.Load())
	}
	for i, m := range r.e.Spec.Conditions.Maps {
		if m.Name != input.Spec.Conditions.Maps[i].Name || m.Resolved == nil || !strings.HasSuffix(m.Location(), "/v1/map.zip") {
			t.Fatal("resolved order or references changed")
		}
		if input.Spec.Conditions.Maps[i].Resolved != nil {
			t.Fatal("caller input mutated")
		}
	}
}
