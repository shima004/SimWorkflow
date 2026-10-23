package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"simworkflow/internal/config"
)

func TestSignedTransfersAndJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"name":"test"}`)
		} else if r.Method == http.MethodPut {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
				t.Error("persistent writes must be signed")
			}
		}
	}))
	defer server.Close()
	store := New(config.Config{S3: config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "test-access-key", SecretAccessKey: "secret-signing-key", ForcePathStyle: true}, HTTPTimeout: time.Second})
	if err := store.PutJSON(context.Background(), "plan.json", map[string]string{"name": "test"}); err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := store.GetJSON(context.Background(), "plan.json", &result); err != nil || result["name"] != "test" {
		t.Fatal(err)
	}
	for _, source := range []string{"s3://maps/map%20one.zip", "s3://maps/map.zip"} {
		transfer, err := store.Download(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(transfer.URL, "X-Amz-Signature=") || strings.Contains(transfer.URL, "secret-signing-key") {
			t.Fatal("workers must receive short-lived signatures, not long-lived secret keys")
		}
	}
	if _, err := store.Download(context.Background(), "https://untrusted/map.zip"); err == nil {
		t.Fatal("map references must be S3 URIs")
	}
}

func TestFileTransferHasContentLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 7 {
			t.Errorf("S3 PUT requires known length, got %d", r.ContentLength)
		}
		data, _ := io.ReadAll(r.Body)
		if string(data) != "payload" {
			t.Error("unexpected payload")
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "payload")
	os.WriteFile(path, []byte("payload"), 0600)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	response, err := Request(context.Background(), http.MethodPut, Transfer{URL: server.URL}, file)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}
