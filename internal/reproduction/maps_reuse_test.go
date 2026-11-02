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

	"simworkflow/internal/experiment"
	"simworkflow/internal/operation"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/reproduction/testfixture"
)

func TestCaptureReusesPinnedSnapshotOnceAcrossLocations(t *testing.T) {
	env := testfixture.New(t)
	digest := sha256.Sum256(env.Map)
	sha := hex.EncodeToString(digest[:])
	key := "/fixture/reproduction/maps/" + sha + ".zip"
	env.Objects.Set(key, env.Map)
	input := env.Experiment()
	input.Spec.Conditions.Maps = []experiment.Map{
		{Name: "first", Source: "s3://fixture/deleted-original.zip", SHA256: sha},
		{Name: "second", Source: "s3://fixture/another-original.zip", SHA256: sha},
		{Name: "alias", Source: "s3://fixture/deleted-original.zip"},
	}
	var gets, otherRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == key {
			gets.Add(1)
		} else {
			otherRequests.Add(1)
		}
		env.Objects.ServeHTTP(w, r)
	}))
	defer server.Close()
	env.Config.S3.Endpoint = server.URL
	var completed, total int
	ctx := operation.WithReporter(context.Background(), func(e operation.Event) {
		if e.Stage == "Verify and snapshot maps" {
			completed, total = e.Completed, e.Total
		}
	})
	locked, _, err := reproduction.Capture(ctx, env.Config, input, env.Helper, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 1 || otherRequests.Load() != 0 || completed != 1 || total != 1 {
		t.Fatalf("expected one snapshot GET only: gets=%d other=%d progress=%d/%d", gets.Load(), otherRequests.Load(), completed, total)
	}
	for i, m := range locked.Spec.Conditions.Maps {
		if m.Name != input.Spec.Conditions.Maps[i].Name || m.ExpectedSHA256() != sha || m.Location() != "s3://fixture"+strings.TrimPrefix(key, "/fixture") {
			t.Fatal("map alias identity, order or pinned contents changed", m)
		}
	}
	if input.Spec.Conditions.Maps[2].SHA256 != "" {
		t.Fatal("capture mutated original input")
	}
	// Neither matching object names nor successful HEADs may hide corrupt bytes.
	env.Objects.Set(key, []byte("corrupted snapshot"))
	if _, _, err := reproduction.Capture(ctx, env.Config, input, env.Helper, "test", "test"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatal("corrupt snapshot accepted or silently replaced", err)
	}
}

func TestCapturePinnedSnapshotFallbackAndValidation(t *testing.T) {
	for _, mode := range []string{"missing", "forbidden", "invalid-archive"} {
		t.Run(mode, func(t *testing.T) {
			env := testfixture.New(t)
			data := env.Map
			if mode == "invalid-archive" {
				data = []byte("hash matches, but this is not a ZIP")
			}
			digest := sha256.Sum256(data)
			sha := hex.EncodeToString(digest[:])
			key := "/fixture/reproduction/maps/" + sha + ".zip"
			if mode != "missing" {
				env.Objects.Set(key, data)
			}
			var origins atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "forbidden" && r.URL.Path == key {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if r.URL.Path == "/fixture/original.zip" {
					origins.Add(1)
				}
				env.Objects.ServeHTTP(w, r)
			}))
			defer server.Close()
			env.Config.S3.Endpoint = server.URL
			input := env.Experiment()
			input.Spec.Conditions.Maps[0].SHA256 = sha
			_, _, err := reproduction.Capture(context.Background(), env.Config, input, env.Helper, "test", "test")
			if mode == "missing" {
				if err != nil || origins.Load() == 0 || string(env.Objects.Get(key)) != string(data) {
					t.Fatal("missing snapshot was not created and verified", err)
				}
			} else if err == nil || origins.Load() != 0 {
				t.Fatal(fmt.Sprintf("%s snapshot bypassed using original source", mode), err)
			}
		})
	}
}
