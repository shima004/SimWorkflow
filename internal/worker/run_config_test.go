package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"simworkflow/internal/experiment"
)

func TestReadRunConfigRemoteAndLegacy(t *testing.T) {
	want := Config{Stage: Simulation, ExecutionID: "execution", Run: experiment.Run{ID: "run-test"}, TimeoutSeconds: 300}
	data, _ := json.Marshal(want)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Query().Get("signature") != "test-capability" {
			t.Error("configuration must use a signed GET")
		}
		w.Write(data)
	}))
	defer server.Close()
	for _, remote := range []bool{false, true} {
		inline, target := string(data), ""
		if remote {
			inline, target = "", server.URL+"/config.json?signature=test-capability"
		}
		got, err := ReadRunConfig(context.Background(), inline, target)
		if err != nil || got.Run.ID != want.Run.ID || got.Stage != want.Stage || got.ExecutionID != want.ExecutionID || got.TimeoutSeconds != 300 {
			t.Fatalf("configuration decode failed: %v", err)
		}
	}
	if requests != 1 {
		t.Fatal("inline format must not make a network request")
	}
}

func TestReadRunConfigRejectsInvalidOrOversizedInput(t *testing.T) {
	for _, tc := range []struct{ inline, remote string }{
		{}, {inline: "{}", remote: "https://example/config?secret=private"},
		{remote: "file:///private"}, {remote: "https://user:private@example/config"},
		{inline: "{invalid private"}, {inline: "{} {}"}, {inline: strings.Repeat(" ", 1<<20) + "{}"},
	} {
		if _, err := ReadRunConfig(context.Background(), tc.inline, tc.remote); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid configuration must fail without echoing inputs")
		}
	}
	for _, status := range []int{http.StatusForbidden, http.StatusTemporaryRedirect, http.StatusOK} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://invalid.example/private")
			w.WriteHeader(status)
			w.Write([]byte("private invalid JSON"))
		}))
		_, err := ReadRunConfig(context.Background(), "", server.URL+"/?secret=private")
		server.Close()
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("remote errors must be sanitized and redirects rejected")
		}
	}
}
