package argo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"simworkflow/internal/config"
)

func overviewFixture(name, phase, created string) map[string]any {
	return map[string]any{"metadata": map[string]any{"name": name, "namespace": "experiments", "creationTimestamp": created, "labels": map[string]string{managedLabel: "simworkflow"}, "annotations": map[string]string{"private": "private-signed-url"}}, "status": map[string]any{"phase": phase, "message": "private-signed-url", "nodes": map[string]any{"private": "private-signed-url"}}, "spec": map[string]any{"templates": "private-signed-url"}}
}

func TestListPaginationFilteringSortingAndRedaction(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/workflows/experiments" || r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("invalid list request or missing authentication")
		}
		if q.Get("listOptions.labelSelector") != managedLabel+"=simworkflow" || q.Get("listOptions.limit") != "100" || strings.Contains(q.Get("fields"), "templates") || strings.Contains(q.Get("fields"), "nodes") {
			t.Error("list must select only managed workflows and safe overview fields")
		}
		var items []map[string]any
		next := ""
		switch q.Get("listOptions.continue") {
		case "":
			next = "next+/?=&"
			unmanaged := overviewFixture("unrelated", "Running", "2026-10-25T15:00:00Z")
			unmanaged["metadata"].(map[string]any)["labels"] = map[string]string{managedLabel: "other"}
			items = []map[string]any{overviewFixture("old", "Succeeded", "2026-10-25T10:00:00Z"), unmanaged}
		case "next+/?=&":
			next = "last" // Empty pages can still have a continuation.
		case "last":
			outside := overviewFixture("outside", "Running", "2026-10-25T16:00:00Z")
			outside["metadata"].(map[string]any)["namespace"] = "other"
			items = []map[string]any{overviewFixture("new", "", "2026-10-25T12:00:00Z"), outside}
		default:
			t.Error("invalid continuation")
		}
		json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"continue": next}, "items": items})
	}))
	defer server.Close()
	client := New(config.Config{Argo: config.Argo{Server: server.URL, Namespace: "experiments", Token: "private-token"}, HTTPTimeout: time.Second})
	items, err := client.List(context.Background())
	if err != nil || requests != 3 || len(items) != 2 || items[0].Name != "new" || items[0].Phase != "Pending" || items[1].Name != "old" {
		t.Fatalf("pagination/filtering/sorting failed: %v", err)
	}
	data, _ := json.Marshal(items)
	if strings.Contains(string(data), "private") {
		t.Fatal("list must never expose templates, annotations or node messages")
	}
}

func TestListInvalidAndRepeatedPagesFailSafely(t *testing.T) {
	for _, response := range []string{`{"metadata":{"continue":"repeat"},"items":[]}`, `{"private-signed-url":"value"}`, `{"items":"private-signed-url"}`, `private-signed-url`} {
		t.Run(fmt.Sprint(len(response)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
			defer server.Close()
			client := New(config.Config{Argo: config.Argo{Server: server.URL, Namespace: "experiments"}, HTTPTimeout: time.Second})
			if _, err := client.List(context.Background()); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid list responses must fail without exposing their contents")
			}
		})
	}
}

func TestStopOwnershipCompletedAndRepeatedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, phase, shutdown       string
		owned, requested, wantError bool
	}{
		{"running", "Running", "", true, true, false},
		{"pending", "", "", true, true, false},
		{"succeeded", "Succeeded", "", true, false, false},
		{"failed", "Failed", "", true, false, false},
		{"error", "Error", "", true, false, false},
		{"already-stopping", "Running", "Stop", true, false, false},
		{"already-terminating", "Running", "Terminate", true, false, false},
		{"unrelated", "Running", "", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resource := overviewFixture(tc.name, tc.phase, "2026-10-25T10:00:00Z")
				resource["spec"].(map[string]any)["shutdown"] = tc.shutdown
				if !tc.owned {
					delete(resource["metadata"].(map[string]any), "labels")
				}
				if r.Method == http.MethodPut {
					writes++
					var body map[string]string
					if r.URL.Path != "/api/v1/workflows/experiments/"+tc.name+"/stop" || json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["name"] != tc.name || body["namespace"] != "experiments" {
						t.Error("stop must target the exact workflow using the Stop API")
					}
				} else if r.Method != http.MethodGet || r.URL.Query().Get("fields") != overviewFields {
					t.Error("stop preflight must request minimal fields")
				}
				json.NewEncoder(w).Encode(resource)
			}))
			defer server.Close()
			client := New(config.Config{Argo: config.Argo{Server: server.URL, Namespace: "experiments"}, HTTPTimeout: time.Second})
			result, requested, err := client.Stop(context.Background(), tc.name)
			if (err != nil) != tc.wantError || requested != tc.requested || (writes != 0) != tc.requested {
				t.Fatalf("unexpected stop result: requested=%v writes=%d err=%v", requested, writes, err)
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "private") {
				t.Fatal("stop must not expose worker capabilities")
			}
		})
	}
}

func TestStopRejectsAliasesAndPropagatesSafeHTTPFailures(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(overviewFixture("exact", "Running", "2026-10-25T10:00:00Z"))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "private-signed-url")
	}))
	defer server.Close()
	client := New(config.Config{Argo: config.Argo{Server: server.URL, Namespace: "experiments"}, HTTPTimeout: time.Second})
	for _, name := range []string{"", "@latest", "../other", "name?x=y", "a,b", "a\nb", "a-", strings.Repeat("a", 254)} {
		if _, _, err := client.Stop(context.Background(), name); err == nil || requests != 0 {
			t.Fatal("aliases, selectors and invalid names must be rejected before sending any request")
		}
	}
	if _, _, err := client.Stop(context.Background(), "exact"); err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "private") {
		t.Fatal("stop failures must be safe and visible")
	}
}
