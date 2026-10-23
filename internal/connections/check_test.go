package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simworkflow/internal/config"
)

func TestCheckServices(t *testing.T) {
	var argoCalls, s3Calls, registryCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/argo/api/v1/workflows/experiments":
			argoCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer secret-argo-token" || r.URL.Query().Get("listOptions.limit") != "1" {
				t.Error("workflow request missing scoped authentication or list limit")
			}
			fmt.Fprint(w, `{"metadata":{},"items":[]}`)
		case "/results":
			s3Calls.Add(1)
			if r.Method != http.MethodHead || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || !strings.Contains(r.Header.Get("Authorization"), "Credential=test-access-key/") || r.Header.Get("X-Amz-Security-Token") != "secret-session-token" {
				t.Error("S3 request must be a signed HeadBucket with session token")
			}
		case "/v2/":
			registryCalls.Add(1)
			user, password, ok := r.BasicAuth()
			if !ok || user != "test-user" || password != "secret-password" {
				t.Error("registry request missing basic authentication")
			}
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := config.Config{
		Argo:        config.Argo{Server: server.URL + "/argo", Namespace: "experiments", Token: "Bearer secret-argo-token"},
		S3:          config.S3{Endpoint: server.URL, Region: "us-east-1", Bucket: "results", AccessKeyID: "test-access-key", SecretAccessKey: "secret-s3-key", SessionToken: "secret-session-token", ForcePathStyle: true},
		Registry:    config.Registry{URL: server.URL, Username: "test-user", Password: "secret-password"},
		HTTPTimeout: time.Second,
	}
	results := Check(context.Background(), c)
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("%s: %v", result.Service, result.Err)
		}
	}
	if argoCalls.Load() != 1 || s3Calls.Load() != 1 || registryCalls.Load() != 1 {
		t.Fatal("each probe must perform exactly one read request")
	}
}

func TestRegistryBearerChallenge(t *testing.T) {
	var tokenCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			user, password, ok := r.BasicAuth()
			if !ok || user != "user" || password != "secret-password" || r.URL.Query().Get("service") != "test" || r.URL.Query().Get("scope") != "repository:team/agent:pull,push" {
				t.Error("invalid token exchange")
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "secret-issued-token"})
			return
		}
		if r.Header.Get("Authorization") == "Bearer secret-issued-token" {
			fmt.Fprint(w, `{}`)
			return
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test",scope="repository:team/agent:pull,push"`, server.URL))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	err := checkRegistry(context.Background(), server.Client(), config.Registry{URL: server.URL, Username: "user", Password: "secret-password"})
	if err != nil || tokenCalls.Load() != 1 {
		t.Fatalf("expected successful bearer exchange, got %v", err)
	}
	// A rejected explicitly configured bearer token is not silently replaced
	// by anonymous access.
	err = checkRegistry(context.Background(), server.Client(), config.Registry{URL: server.URL, Token: "secret-invalid-token"})
	if err == nil || tokenCalls.Load() != 1 {
		t.Fatal("invalid explicit token must fail without a second token exchange")
	}
}

func TestProbeErrorsDoNotExposeCredentialsOrResponseBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "secret-response-body secret-password")
	}))
	defer server.Close()
	for _, err := range []error{
		checkArgo(context.Background(), server.Client(), config.Argo{Server: server.URL, Namespace: "argo", Token: "secret-token"}),
		checkRegistry(context.Background(), server.Client(), config.Registry{URL: server.URL, Token: "secret-token"}),
		checkS3(context.Background(), NewS3(config.S3{Endpoint: server.URL, Region: "test", AccessKeyID: "test-key", SecretAccessKey: "secret-key", ForcePathStyle: true}, server.Client()), "results"),
	} {
		if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "secret-") || strings.Contains(err.Error(), server.URL) {
			t.Fatalf("expected a safe HTTP-status error, got %v", err)
		}
	}
}

func TestArgoRejectsLoginPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>Login</html>") }))
	defer server.Close()
	if err := checkArgo(context.Background(), server.Client(), config.Argo{Server: server.URL, Namespace: "argo"}); err == nil {
		t.Fatal("HTTP 200 with a login page is not valid API access")
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	client := HTTPClient(config.Config{HTTPTimeout: time.Second})
	err := checkArgo(context.Background(), client, config.Argo{Server: source.URL, Namespace: "argo", Token: "secret-token"})
	if err == nil || !strings.Contains(err.Error(), "302") || calls.Load() != 0 {
		t.Fatal("redirects must be reported without forwarding the request")
	}
}

func TestNetworkErrorsAreSanitized(t *testing.T) {
	if got := safeNetworkError(errors.New("secret-token at http://private-server")); strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "private") {
		t.Fatal("network errors must not expose URLs or credentials")
	}
	if got := safeNetworkError(context.DeadlineExceeded); got.Error() != "request timed out" {
		t.Fatal(got)
	}
}
