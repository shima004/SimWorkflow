package mapcatalog

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeObjects struct {
	mu       sync.Mutex
	data     map[string][]byte
	metadata map[string]map[string]string
}

func catalogFixture(t *testing.T) (*Catalog, *fakeObjects) {
	t.Helper()
	objects := &fakeObjects{data: map[string][]byte{}, metadata: map[string]map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		objects.mu.Lock()
		defer objects.mu.Unlock()
		if r.URL.Query().Get("list-type") == "2" {
			type entry struct {
				Key string `xml:"Key"`
			}
			listing := struct {
				XMLName   xml.Name `xml:"ListBucketResult"`
				Truncated bool     `xml:"IsTruncated"`
				Contents  []entry  `xml:"Contents"`
			}{}
			prefix := "/results/" + r.URL.Query().Get("prefix")
			for key := range objects.data {
				if strings.HasPrefix(key, prefix) {
					listing.Contents = append(listing.Contents, entry{strings.TrimPrefix(key, "/results/")})
				}
			}
			xml.NewEncoder(w).Encode(listing)
			return
		}
		if r.Method == http.MethodPut {
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("map objects must use atomic conditional writes")
			}
			if _, ok := objects.data[r.URL.Path]; ok {
				w.WriteHeader(412)
				return
			}
			objects.data[r.URL.Path], _ = io.ReadAll(r.Body)
			objects.metadata[r.URL.Path] = map[string]string{"sha256": r.Header.Get("X-Amz-Meta-Sha256")}
			return
		}
		data, ok := objects.data[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			for key, value := range objects.metadata[r.URL.Path] {
				w.Header().Set("X-Amz-Meta-"+key, value)
			}
			return
		}
		w.Write(data)
	}))
	t.Cleanup(server.Close)
	store := storage.New(config.Config{S3: config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "test-key", SecretAccessKey: "test-secret", ForcePathStyle: true}, HTTPTimeout: time.Second})
	return &Catalog{Store: store}, objects
}

func inputDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string]string{"map/map.gml": "<map/>", "map/scenario.xml": "<scenario><firebrigade/><policeforce/><ambulanceteam/></scenario>", "config/common.cfg": "random.seed: 1\n", "config/kernel.cfg": "kernel.timesteps: 300\n"} {
		path := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte(data), 0600)
	}
	return root
}

func TestRegisterInspectListAndResolveLatest(t *testing.T) {
	c, objects := catalogFixture(t)
	ctx := context.Background()
	input := inputDirectory(t)
	first, err := c.Register(ctx, input, RegisterOptions{Name: "test", Version: "v1", Description: "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Agents.FireBrigade != 1 || first.Source != "s3://results/maps/test/v1/map.zip" {
		t.Fatal(first)
	}
	if _, err = c.Register(ctx, input, RegisterOptions{Name: "test", Version: "v1"}); err == nil {
		t.Fatal("registered versions must not be overwritten")
	}
	second, err := c.Register(ctx, input, RegisterOptions{Name: "test", Version: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != first.SHA256 {
		t.Fatal("identical directory content should have identical hashes")
	}
	latest, err := c.Inspect(ctx, "test", "latest")
	if err != nil || latest.Version != "v2" {
		t.Fatal(latest, err)
	}
	entries, err := c.List(ctx, "")
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	e, err := experiment.ReadFile("../../examples/experiments/precompute-baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e.Spec.Conditions.Maps = []experiment.Map{{Name: "test", Version: "latest"}}
	preview, err := experiment.BuildPlan(e)
	if err != nil || !preview.UnresolvedMaps {
		t.Fatal("offline plan must mark unresolved map IDs")
	}
	resolved, err := c.Resolve(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experiment.BuildPlan(resolved)
	if err != nil || plan.UnresolvedMaps {
		t.Fatal(err)
	}
	m := plan.Runs[0].Map
	if m.Resolved == nil || m.Resolved.Version != "v2" || m.ExpectedSHA256() != second.SHA256 || m.Location() != second.Source {
		t.Fatal(m)
	}
	if e.Spec.Conditions.Maps[0].Resolved != nil {
		t.Fatal("resolving must not mutate the user's definition")
	}
	if _, err = c.Register(ctx, input, RegisterOptions{Name: "test", Version: "v3"}); err != nil {
		t.Fatal(err)
	}
	if plan.Runs[0].Map.Resolved.Version != "v2" {
		t.Fatal("resolved plans must stay pinned when latest changes")
	}
	objects.mu.Lock()
	original := append([]byte(nil), objects.data["/results/maps/test/v1/map.zip"]...)
	objects.mu.Unlock()
	if len(original) == 0 {
		t.Fatal("missing registered archive")
	}
}

func TestResolveMultipleVersionsOfTheSameMap(t *testing.T) {
	c, _ := catalogFixture(t)
	ctx := context.Background()
	input := inputDirectory(t)
	first, err := c.Register(ctx, input, RegisterOptions{Name: "test", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "config/common.cfg"), []byte("random.seed: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := c.Register(ctx, input, RegisterOptions{Name: "test", Version: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := experiment.ReadFile("../../examples/experiments/precompute-baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e.Spec.Agents = e.Spec.Agents[:1]
	e.Spec.Agents[0].Matrix = nil
	e.Spec.Conditions.Maps = []experiment.Map{{Name: "test", Versions: []string{"v2", "v1"}}}
	e.Spec.Conditions.Seeds = []int64{42, 7}
	resolved, err := c.Resolve(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Spec.Conditions.Maps) != 1 || len(e.Spec.Conditions.Maps[0].Versions) != 2 || e.Spec.Conditions.Maps[0].Resolved != nil {
		t.Fatal("resolve modified input versions")
	}
	plan, err := experiment.BuildPlan(resolved)
	if err != nil || plan.TotalRuns != 4 || plan.UnresolvedMaps {
		t.Fatal("resolved versions did not expand", err)
	}
	for i, metadata := range []Metadata{second, first} {
		m := plan.Runs[i*2].Map
		if m.Name != "test" || m.Version != metadata.Version || m.Resolved.Version != metadata.Version || m.Location() != metadata.Source || m.ExpectedSHA256() != metadata.SHA256 {
			t.Fatal("wrong resolved version", m)
		}
	}
	if first.SHA256 == second.SHA256 || plan.Runs[0].ID == plan.Runs[2].ID {
		t.Fatal("versions lost their own contents or identities")
	}
	e.Spec.Conditions.Maps[0].Versions = []string{"v2", "latest"}
	if _, err := c.Resolve(ctx, e); err == nil {
		t.Fatal("overlapping resolved versions must be rejected")
	}
	e.Spec.Conditions.Maps[0].Versions = []string{"v1", "missing"}
	if _, err := c.Resolve(ctx, e); err == nil {
		t.Fatal("a missing requested version must fail resolution")
	}
}

func TestConcurrentRegistrationAndOrphanRecovery(t *testing.T) {
	c, objects := catalogFixture(t)
	input := inputDirectory(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Register(ctx, input, RegisterOptions{Name: "concurrent", Version: "v1"})
			outcomes <- err
		}()
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one registrar must publish a version, got %d", successes)
	}
	// Simulate a completed archive upload whose metadata publication failed.
	objects.mu.Lock()
	delete(objects.data, "/results/maps/concurrent/v1/metadata.json")
	objects.mu.Unlock()
	if _, err := c.Register(ctx, input, RegisterOptions{Name: "concurrent", Version: "v1"}); err != nil {
		t.Fatalf("identical orphan upload should be recoverable: %v", err)
	}
	objects.mu.Lock()
	delete(objects.data, "/results/maps/concurrent/v1/metadata.json")
	objects.mu.Unlock()
	os.WriteFile(filepath.Join(input, "map/map.gml"), []byte("<different/>"), 0600)
	if _, err := c.Register(ctx, input, RegisterOptions{Name: "concurrent", Version: "v1"}); err == nil {
		t.Fatal("orphan content must not be replaced by different bytes")
	}
}
