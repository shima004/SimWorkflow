package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"simworkflow/internal/experiment"
)

func TestDownloadCommandAndFileSelection(t *testing.T) {
	const prefix = "experiments/0123456789abcdef0123456789abcdef"
	const id = "run-00000000000000000000000000000001"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte("rescue log bytes")
	if err := tw.WriteHeader(&tar.Header{Name: "logs/simulation/rescue.log.7z", Mode: 0644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(data)
	tw.Close()
	gz.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workflows/argo/test-workflow":
			json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"annotations": map[string]string{"simworkflow.io/result-prefix": prefix}}, "status": map[string]any{"phase": "Succeeded"}})
		case "/results/" + prefix + "/plan.json":
			json.NewEncoder(w).Encode(experiment.Plan{Runs: []experiment.Run{{ID: id}}})
		case "/results/" + prefix + "/" + id + "/summary.json":
			io.WriteString(w, `{"score":19.25}`)
		case "/results/" + prefix + "/" + id + "/bundle.tar.gz":
			w.Write(buf.Bytes())
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	env := map[string]string{"ARGO_WORKFLOW_SERVER": server.URL, "S3_ENDPOINT": server.URL, "S3_BUCKET": "results", "S3_ACCESS_KEY_ID": "secret-key", "S3_SECRET_ACCESS_KEY": "secret-password", "REGISTRY_URL": server.URL}
	lookup := func(key string) (string, bool) { v, ok := env[key]; return v, ok }
	var out, errOut bytes.Buffer
	dest := filepath.Join(t.TempDir(), "summaries")
	if err := run([]string{"experiment", "download", "--include", "score", "--stage", "simulation", "--output", dest, "test-workflow"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Downloaded 1 files") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(dest, id, "simulation/summary.json")); err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(t.TempDir(), "rescue-only")
	out.Reset()
	if err := run([]string{"experiment", "download", "--include", "summary", "--stage", "simulation", "--output", dest, "test-workflow"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"experiment", "download", "--file", "logs/simulation/rescue.log.7z", "--run", id, "--output", dest, "test-workflow"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(dest, id, "simulation/logs/rescue.log.7z"))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("%v %q", err, actual)
	}
	if _, err := os.Stat(filepath.Join(dest, "plan.json")); !os.IsNotExist(err) {
		t.Fatal("file-only request saved unselected plan")
	}
	manifest, err := os.ReadFile(filepath.Join(dest, "download.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{out.String(), errOut.String(), string(manifest)} {
		if strings.Contains(text, "secret-") || strings.Contains(text, "X-Amz-Signature") {
			t.Fatal("capability leaked")
		}
	}
}
