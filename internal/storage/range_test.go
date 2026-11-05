package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simworkflow/internal/config"
)

func TestRangedReaderPinsObjectAndNeverGetsWholeObject(t *testing.T) {
	payload := []byte("0123456789abcdef")
	var heads, ranges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"snapshot"`)
		if r.Method == http.MethodHead {
			heads.Add(1)
		} else {
			ranges.Add(1)
			if r.Header.Get("Range") == "" || r.Header.Get("If-Match") != `"snapshot"` {
				t.Error("unbounded or unpinned GET")
			}
		}
		http.ServeContent(w, r, "bundle.zip", time.Time{}, bytes.NewReader(payload))
	}))
	defer server.Close()
	store := New(config.Config{S3: config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "test", SecretAccessKey: "secret-key", ForcePathStyle: true}, HTTPTimeout: time.Second})
	r, size, err := store.OpenReaderAt(context.Background(), "bundle.zip")
	if err != nil || size != int64(len(payload)) {
		t.Fatal(size, err)
	}
	p := make([]byte, 4)
	if n, err := r.ReadAt(p, 3); n != 4 || err != nil || string(p) != "3456" {
		t.Fatal(n, err, string(p))
	}
	if n, err := r.ReadAt(p, 14); n != 2 || err != io.EOF || string(p[:n]) != "ef" {
		t.Fatal(n, err, string(p))
	}
	if heads.Load() != 1 || ranges.Load() != 2 {
		t.Fatal(heads.Load(), ranges.Load())
	}
}

func TestRangedReaderRejectsIgnoredRangesAndChangedObjects(t *testing.T) {
	for _, mode := range []string{"ignored", "changed", "bad-range", "no-etag"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode != "no-etag" {
					w.Header().Set("ETag", `"snapshot"`)
				}
				if r.Method == http.MethodHead {
					w.Header().Set("Content-Length", "16")
					return
				}
				if mode == "ignored" {
					io.WriteString(w, "0123456789abcdef")
					return
				}
				if mode == "changed" {
					w.Header().Set("ETag", `"different"`)
				}
				w.Header().Set("Content-Range", "bytes 0-3/16")
				if mode == "bad-range" {
					w.Header().Set("Content-Range", "bytes 2-5/16")
				}
				w.WriteHeader(http.StatusPartialContent)
				io.WriteString(w, "0123")
			}))
			defer server.Close()
			store := New(config.Config{S3: config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "test", SecretAccessKey: "secret-key", ForcePathStyle: true}, HTTPTimeout: time.Second})
			r, _, err := store.OpenReaderAt(context.Background(), "bundle.zip")
			if err == nil {
				_, err = r.ReadAt(make([]byte, 4), 0)
			}
			if err == nil || strings.Contains(err.Error(), "X-Amz-") || strings.Contains(err.Error(), "secret-key") {
				t.Fatal("unsafe or accepted bad range", err)
			}
			if (mode == "ignored" || mode == "no-etag") && !errors.Is(err, ErrRangeUnsupported) {
				t.Fatal("unsupported range must permit full ZIP fallback", err)
			}
		})
	}
}
