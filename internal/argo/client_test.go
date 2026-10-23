package argo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"simworkflow/internal/config"
)

func TestSubmitAndGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-test-token" {
			t.Error("missing Argo authentication")
		}
		if r.Method == http.MethodPost {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["workflow"] == nil {
				t.Error("submission must use Argo's workflow envelope")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "test-workflow", "namespace": "experiments"}, "status": map[string]any{"phase": "Succeeded"}, "spec": map[string]any{"secret": "must-not-be-returned"}})
	}))
	defer server.Close()
	client := New(config.Config{Argo: config.Argo{Server: server.URL, Namespace: "experiments", Token: "secret-test-token"}, HTTPTimeout: time.Second})
	submitted, err := client.Submit(context.Background(), map[string]any{"kind": "Workflow"})
	if err != nil || submitted.Metadata.Name != "test-workflow" {
		t.Fatalf("submit: %v", err)
	}
	result, err := client.Get(context.Background(), "test-workflow")
	if err != nil || result.Status.Phase != "Succeeded" {
		t.Fatalf("get: %v", err)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "must-not-be-returned") {
		t.Fatal("snapshots must not include worker transfer capabilities")
	}
	if _, err := client.Get(context.Background(), "../other"); err == nil {
		t.Fatal("workflow names must not alter request paths")
	}
}

func TestErrorsDoNotExposeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); w.Write([]byte("secret-test-token")) }))
	defer server.Close()
	client := New(config.Config{Argo: config.Argo{Server: server.URL, Namespace: "experiments"}, HTTPTimeout: time.Second})
	_, err := client.Get(context.Background(), "test")
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), server.URL) {
		t.Fatal("API errors must contain only a safe status")
	}
}
