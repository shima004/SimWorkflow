package agentbuild

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"simworkflow/internal/config"
)

func TestReference(t *testing.T) {
	ref, err := Reference("http://localhost:5000/", "my-team/agent-python", "v1")
	if err != nil || ref.Name() != "localhost:5000/my-team/agent-python:v1" {
		t.Fatalf("%v %v", ref, err)
	}
	for _, tc := range []struct{ endpoint, repository, tag string }{
		{"https://user:private-secret@example.com", "agent", "v1"},
		{"https://example.com/prefix", "agent", "v1"},
		{"https://example.com?token=private-secret", "agent", "v1"},
		{"https://example.com", "../agent", "v1"},
		{"https://example.com", "host:5000/agent", "v1"},
		{"https://example.com", "agent@sha256:abc", "v1"},
		{"https://example.com", "Agent", "v1"},
		{"https://example.com", "agent", "-bad"},
	} {
		if _, err := Reference(tc.endpoint, tc.repository, tc.tag); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("expected safe reference error")
		}
	}
}

func TestPublishAuthenticationAndTLS(t *testing.T) {
	archive := testArchive(t)
	img, err := tarball.ImageFromPath(archive, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"anonymous", "basic", "token", "challenge", "https"} {
		t.Run(mode, func(t *testing.T) {
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			var endpoint string
			var seen atomic.Int32
			c := config.Registry{}
			want := ""
			switch mode {
			case "basic":
				c.Username, c.Password = "user", "private-secret"
				want = "Basic " + base64.StdEncoding.EncodeToString([]byte("user:private-secret"))
			case "token":
				c.Token = "private-secret"
				want = "Bearer private-secret"
			case "challenge":
				c.Username, c.Password = "user", "private-secret"
				want = "Bearer issued-token"
			}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "challenge" && r.URL.Path == "/token" {
					user, password, ok := r.BasicAuth()
					if !ok || user != "user" || password != "private-secret" {
						http.Error(w, "private-secret", 401)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"token":"issued-token"}`)
					return
				}
				if r.Header.Get("Authorization") != want {
					if mode == "basic" {
						w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
					} else {
						w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test"`, endpoint))
					}
					http.Error(w, "private-secret", 401)
					return
				}
				seen.Add(1)
				handler.ServeHTTP(w, r)
			})
			var server *httptest.Server
			if mode == "https" {
				server = httptest.NewTLSServer(h)
			} else {
				server = httptest.NewServer(h)
			}
			defer server.Close()
			endpoint = server.URL
			c.URL = endpoint
			ref, err := Reference(c.URL, "team/agent", "v1")
			if err != nil {
				t.Fatal(err)
			}
			got, err := publish(context.Background(), c, time.Second, archive, ref.Name(), server.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			if seen.Load() == 0 || got != ref.Context().Digest(digest.String()).Name() {
				t.Fatal("uploaded digest was not verified")
			}
		})
	}
}

func TestPublishRejectsOffOriginCredentialsAndRedactsErrors(t *testing.T) {
	archive := testArchive(t)
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true); http.Error(w, "private-secret", 401) }))
	defer other.Close()
	for _, mode := range []string{"token-realm", "redirect", "error", "changed-digest", "untrusted-tls"} {
		t.Run(mode, func(t *testing.T) {
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "token-realm":
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test"`, other.URL))
					http.Error(w, "private-secret", 401)
				case "redirect":
					http.Redirect(w, r, other.URL+"/private-secret", 307)
				case "error":
					http.Error(w, `{"errors":[{"code":"DENIED","message":"private-secret"}]}`, 403)
				case "changed-digest":
					if r.Method == "HEAD" && strings.Contains(r.URL.Path, "/manifests/") {
						w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("a", 64))
						w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
						w.Header().Set("Content-Length", "1")
						w.WriteHeader(200)
						return
					}
					handler.ServeHTTP(w, r)
				default:
					handler.ServeHTTP(w, r)
				}
			})
			var server *httptest.Server
			if mode == "untrusted-tls" {
				server = httptest.NewUnstartedServer(h)
				server.Config.ErrorLog = log.New(io.Discard, "", 0)
				server.StartTLS()
			} else {
				server = httptest.NewServer(h)
			}
			defer server.Close()
			c := config.Registry{URL: server.URL, Username: "user", Password: "private-secret"}
			ref, err := Reference(c.URL, "team/agent", "v1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = Publish(context.Background(), c, time.Second, archive, ref.Name())
			if err == nil || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("expected sanitized failure: %v", err)
			}
			if leaked.Load() {
				t.Fatal("contacted an off-origin destination")
			}
		})
	}
}
