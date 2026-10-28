package reproduction_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/operation"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/reproduction/testfixture"
)

func TestCaptureParallelSnapshotsDeduplicatesAliasesAndReusesVerifiedObjects(t *testing.T) {
	env := testfixture.New(t)
	input := env.Experiment()
	input.Spec.Conditions.Maps = nil
	for i := range 16 {
		name := fmt.Sprintf("map-%02d", i)
		env.Objects.Set("/fixture/"+name+".zip", env.Map)
		input.Spec.Conditions.Maps = append(input.Spec.Conditions.Maps, experiment.Map{Name: name, Source: "s3://fixture/" + name + ".zip"})
	}
	// One aliased location must be fetched once and retained in its own position.
	input.Spec.Conditions.Maps = append(input.Spec.Conditions.Maps, experiment.Map{Name: "alias", Source: input.Spec.Conditions.Maps[0].Source})
	ready := make(chan struct{}, 32)
	release := make(chan struct{})
	var active, peak, gets, puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/fixture/map-") {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			gets.Add(1)
			ready <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if r.Method == http.MethodPut {
			puts.Add(1)
		}
		env.Objects.ServeHTTP(w, r)
	}))
	defer server.Close()
	env.Config.S3.Endpoint = server.URL
	type result struct {
		e   *experiment.Experiment
		err error
	}
	done := make(chan result, 1)
	var completed, total int
	ctx := operation.WithReporter(context.Background(), func(e operation.Event) {
		if e.Stage == "Verify and snapshot maps" {
			completed, total = e.Completed, e.Total
		}
	})
	go func() {
		e, _, err := reproduction.Capture(ctx, env.Config, input, env.Helper, "test", "test")
		done <- result{e, err}
	}()
	for range 8 {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("snapshot downloads remained sequential")
		}
	}
	close(release)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if peak.Load() != 8 || gets.Load() != 16 || completed != 16 || total != 16 {
		t.Fatal("unexpected snapshot concurrency, deduplication or progress", peak.Load(), gets.Load(), completed, total)
	}
	sha := sha256.Sum256(env.Map)
	for i, m := range r.e.Spec.Conditions.Maps {
		if m.Name != input.Spec.Conditions.Maps[i].Name || m.ExpectedSHA256() != hex.EncodeToString(sha[:]) || !strings.Contains(m.Location(), "/reproduction/maps/") {
			t.Fatal("snapshot order, identity or checksum changed")
		}
		if strings.Contains(input.Spec.Conditions.Maps[i].Source, "reproduction") {
			t.Fatal("capture mutated caller's maps")
		}
	}
	before := puts.Load()
	if _, _, err := reproduction.Capture(context.Background(), env.Config, input, env.Helper, "test", "test"); err != nil {
		t.Fatal(err)
	}
	if puts.Load() != before {
		t.Fatal("existing snapshots were uploaded again")
	}
	// Existing metadata is insufficient: verify bytes and reject corruption.
	env.Objects.Set("/fixture/reproduction/maps/"+hex.EncodeToString(sha[:])+".zip", []byte("corrupted snapshot"))
	if _, _, err := reproduction.Capture(context.Background(), env.Config, input, env.Helper, "test", "test"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatal("stored bytes were trusted without verification", err)
	}
}

func TestCaptureRejectsConflictingAliasChecksumsBeforeMapReads(t *testing.T) {
	env := testfixture.New(t)
	input := env.Experiment()
	m := input.Spec.Conditions.Maps[0]
	m.SHA256 = strings.Repeat("a", 64)
	alias := m
	alias.Name = "alias"
	alias.SHA256 = strings.Repeat("b", 64)
	input.Spec.Conditions.Maps = []experiment.Map{m, alias}
	before := env.Objects.Requests
	if _, _, err := reproduction.Capture(context.Background(), env.Config, input, env.Helper, "test", "test"); err == nil || !strings.Contains(err.Error(), "aliases disagree") {
		t.Fatal("conflicting aliases accepted", err)
	}
	if env.Objects.Requests != before {
		t.Fatal("conflicting aliases started map transfers")
	}
}
