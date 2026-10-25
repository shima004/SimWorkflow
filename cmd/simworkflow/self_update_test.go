package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestSelfUpdateDoesNotLoadServiceConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("invalid private-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	get := func(string) (string, bool) {
		t.Fatal("self-update must not load settings or credentials")
		return "", false
	}
	if err := run([]string{"--config", "nonexistent.yaml", "self-update", "--help"}, get, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "prerelease") {
		t.Fatal("missing self-update help")
	}
	err := run([]string{"self-update"}, get, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "distributed versioned binary") {
		t.Fatalf("development build should be rejected before network or file changes: %v", err)
	}
}
