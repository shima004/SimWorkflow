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
	"simworkflow/internal/storage"
)

func TestNamesUsesOnlyPaginatedDelimitedListing(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != "maps/" || r.URL.Query().Get("delimiter") != "/" {
			t.Error("names must not read metadata", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		if r.URL.Query().Get("continuation-token") == "next" {
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated><CommonPrefixes><Prefix>maps/aichi/</Prefix></CommonPrefixes><CommonPrefixes><Prefix>maps/vc/</Prefix></CommonPrefixes></ListBucketResult>`)
		} else {
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><CommonPrefixes><Prefix>maps/vc/</Prefix></CommonPrefixes><CommonPrefixes><Prefix>maps/INVALID/</Prefix></CommonPrefixes></ListBucketResult>`)
		}
	}))
	defer server.Close()
	c := &Catalog{Store: storage.New(config.Config{S3: config.S3{Endpoint: server.URL, Bucket: "results", Region: "us-east-1", AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true}, HTTPTimeout: time.Second})}
	names, err := c.Names(context.Background())
	if err != nil || strings.Join(names, ",") != "aichi,vc" || calls.Load() != 2 {
		t.Fatal(names, err, calls.Load())
	}
}

func TestListSelectedMapFetchesVersionsConcurrentlyAndSorts(t *testing.T) {
	c, _ := catalogFixture(t)
	base, err := c.Register(context.Background(), inputDirectory(t), RegisterOptions{Name: "selected", Version: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 16)
	release := make(chan struct{})
	var active, peak, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			if r.URL.Query().Get("prefix") != "maps/selected/" {
				t.Error("wrong selected prefix")
			}
			fmt.Fprint(w, "<ListBucketResult><IsTruncated>false</IsTruncated>")
			for i := 15; i >= 0; i-- {
				fmt.Fprintf(w, "<Contents><Key>maps/selected/v%02d/metadata.json</Key></Contents>", i)
			}
			fmt.Fprint(w, "</ListBucketResult>")
			return
		}
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		ready <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		version := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/results/maps/selected/"), "/metadata.json")
		if strings.Contains(version, "/") {
			t.Error("other map requested")
		}
		m := base
		m.Version = version
		m.Source = "s3://results/maps/selected/" + version + "/map.zip"
		json.NewEncoder(w).Encode(m)
	}))
	defer server.Close()
	c.Store = storage.New(config.Config{S3: config.S3{Endpoint: server.URL, Bucket: "results", Region: "us-east-1", AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true}, HTTPTimeout: 5 * time.Second})
	type result struct {
		entries []Metadata
		err     error
	}
	done := make(chan result, 1)
	go func() { entries, err := c.List(context.Background(), "selected"); done <- result{entries, err} }()
	for range 8 {
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("version reads stayed sequential")
		}
	}
	close(release)
	got := <-done
	if got.err != nil || len(got.entries) != 16 || peak.Load() != 8 || calls.Load() != 16 {
		t.Fatal(got.err, len(got.entries), peak.Load(), calls.Load())
	}
	for i, m := range got.entries {
		if m.Version != fmt.Sprintf("v%02d", i) {
			t.Fatal("unstable order", m.Version)
		}
	}
}
