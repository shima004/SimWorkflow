package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExperimentListAndStopWithOnlyArgoConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	writes := 0
	stopped := false
	fixture := func(name, phase, created string) map[string]any {
		return map[string]any{"metadata": map[string]any{"name": name, "namespace": "argo", "creationTimestamp": created, "labels": map[string]string{"app.kubernetes.io/managed-by": "simworkflow"}}, "status": map[string]any{"phase": phase, "message": "private-signed-url"}, "spec": map[string]any{"templates": "private-signed-url"}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("Argo authentication missing")
		}
		if r.Method == http.MethodPut {
			if r.URL.Path != "/api/v1/workflows/argo/new/stop" {
				t.Error("unexpected mutation")
			}
			writes++
			stopped = true
			json.NewEncoder(w).Encode(fixture("new", "Running", "2026-10-25T12:00:00Z"))
			return
		}
		if r.URL.Path == "/api/v1/workflows/argo" {
			json.NewEncoder(w).Encode(map[string]any{"items": []any{fixture("old", "Succeeded", "2026-10-25T10:00:00Z"), fixture("new", "Running", "2026-10-25T12:00:00Z")}})
			return
		}
		if r.URL.Path == "/api/v1/workflows/argo/new" {
			item := fixture("new", "Running", "2026-10-25T12:00:00Z")
			if stopped {
				item["spec"].(map[string]any)["shutdown"] = "Stop"
			}
			json.NewEncoder(w).Encode(item)
			return
		}
		t.Error("list/stop must not contact S3 or registry")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	values := map[string]string{"CREDENTIALS_STORE": "keyring", "ARGO_WORKFLOW_SERVER": server.URL, "ARGO_WORKFLOW_TOKEN": "private-token"}
	lookup := func(key string) (string, bool) { v, ok := values[key]; return v, ok }
	for _, tc := range []struct {
		args         []string
		want, absent string
	}{
		{[]string{"list"}, "new", "private"},
		{[]string{"list", "--phase", "Running", "--limit", "1"}, "new", "old"},
		{[]string{"list", "--phase", "Pending"}, "No SimWorkflow experiments", "new"},
		{[]string{"stop", "new"}, "Stop requested", "private"},
		{[]string{"stop", "new"}, "already has a shutdown request", "private"},
	} {
		var out, errOut bytes.Buffer
		if err := run(append([]string{"--config=", "--env-file=", "experiment"}, tc.args...), lookup, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), tc.absent) {
			t.Fatal("unexpected or unsafe command output")
		}
	}
	if writes != 1 {
		t.Fatal("repeated stop must not send another mutation")
	}
	var out, errOut bytes.Buffer
	if err := run([]string{"--config=", "--env-file=", "experiment", "list", "--format", "json", "--limit", "1"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var items []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(out.Bytes(), &items) != nil || len(items) != 1 || items[0].Name != "new" || strings.Contains(out.String(), "private") {
		t.Fatal("JSON list must contain only safe sorted summaries")
	}
}

func TestLifecycleInvalidArgumentsDoNotContactArgo(t *testing.T) {
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid arguments must not cause API calls") }))
	defer server.Close()
	lookup := func(key string) (string, bool) {
		if key == "ARGO_WORKFLOW_SERVER" {
			return server.URL, true
		}
		return "", false
	}
	for _, args := range [][]string{{"list", "--phase", "invalid"}, {"list", "--format", "yaml"}, {"list", "--limit", "-1"}, {"list", "name"}, {"stop"}, {"stop", "one", "two"}, {"stop", "@latest"}} {
		var out, errOut bytes.Buffer
		if err := run(append([]string{"--config=", "--env-file=", "experiment"}, args...), lookup, &out, &errOut); err == nil {
			t.Fatal("expected argument validation error")
		}
	}
}
