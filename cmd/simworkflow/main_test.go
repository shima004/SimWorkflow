package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "ARGO_WORKFLOW_SERVER=https://argo.example.com\nARGO_WORKFLOW_TOKEN=secret-token\nS3_ENDPOINT=http://localhost:9000\nS3_BUCKET=workflows\nS3_ACCESS_KEY_ID=secret-key\nS3_SECRET_ACCESS_KEY=secret-password\nREGISTRY_URL=https://registry.example.com\nREGISTRY_TOKEN=secret-registry-token\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	lookup := func(string) (string, bool) { return "", false }
	if err := run([]string{"-env-file", path, "config", "check"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Configuration valid") || strings.Contains(out.String()+errOut.String(), "secret-") {
		t.Fatal("expected successful check without credentials in output")
	}
	if err := run([]string{"-env-file", path + ".missing", "config", "check"}, lookup, &out, &errOut); err == nil {
		t.Fatal("explicit missing file must fail")
	}
}
