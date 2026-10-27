package imageio_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"simworkflow/internal/config"
	"simworkflow/internal/imageio"
	"simworkflow/internal/reproduction/testfixture"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCredentialsScopedAndHTTPSPreserved(t *testing.T) {
	env := testfixture.New(t)
	for _, kind := range []string{"public", "configured-https"} {
		t.Run(kind, func(t *testing.T) {
			c := env.Config
			c.Registry.Username, c.Registry.Password = "fixture-user", "private-registry-secret"
			host := "registry.public.example"
			if kind == "configured-https" {
				host = strings.TrimPrefix(env.Registry.URL, "http://")
				c.Registry.URL = "https://" + host
			}
			client := imageio.New(c)
			client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" {
					t.Error("HTTPS must not downgrade to HTTP")
				}
				if kind == "public" && r.Header.Get("Authorization") != "" {
					t.Error("credentials sent to unrelated image host")
				}
				clone := r.Clone(r.Context())
				u := *r.URL
				destination, _ := url.Parse(env.Registry.URL)
				u.Scheme, u.Host = destination.Scheme, destination.Host
				clone.URL, clone.Host = &u, u.Host
				return http.DefaultTransport.RoundTrip(clone)
			})
			pinned, err := client.Resolve(context.Background(), host+"/fixture/agent:v1")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(pinned, host+"/fixture/agent@sha256:") {
				t.Fatal("resolved image lost source identity")
			}
		})
	}
}

func TestPublicImageRead(t *testing.T) {
	reference := os.Getenv("SIMWORKFLOW_TEST_PUBLIC_IMAGE")
	if reference == "" {
		t.Skip("explicit public image read only")
	}
	image, pinned, err := imageio.New(config.Config{}).Fetch(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range layers {
		body, err := layer.Compressed()
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, body)
		body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Log(pinned)
}
