// Package imageio transfers pinned linux/amd64 images without Docker or credential files.
package imageio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"simworkflow/internal/agentbuild"
	"simworkflow/internal/config"
)

const Platform = "linux/amd64"

type Client struct {
	Config     config.Config
	Transport  http.RoundTripper
	Repository string
}

func New(c config.Config) *Client { return &Client{Config: c} }

func (c *Client) options(ctx context.Context, host string) ([]remote.Option, error) {
	origin := &url.URL{Scheme: "https", Host: host}
	auth := authn.Anonymous
	configured, err := url.Parse(c.Config.Registry.URL)
	if err != nil {
		return nil, errors.New("invalid registry configuration")
	}
	if strings.EqualFold(configured.Host, host) {
		origin = configured
		auth = authn.FromConfig(authn.AuthConfig{Username: c.Config.Registry.Username, Password: c.Config.Registry.Password, RegistryToken: c.Config.Registry.Token})
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	return []remote.Option{remote.WithContext(ctx), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}), remote.WithAuth(auth), remote.WithTransport(&transport{origin: origin, base: base, timeout: c.Config.HTTPTimeout})}, nil
}

func (c *Client) Fetch(ctx context.Context, reference string) (v1.Image, string, error) {
	ref, err := name.ParseReference(reference)
	if err != nil {
		return nil, "", errors.New("invalid container image reference")
	}
	options, err := c.options(ctx, ref.Context().RegistryStr())
	if err != nil {
		return nil, "", err
	}
	image, err := remote.Image(ref, options...)
	if err != nil {
		return nil, "", errors.New("could not fetch image; check registry access and image availability")
	}
	digest, err := image.Digest()
	if err != nil {
		return nil, "", errors.New("could not determine image digest")
	}
	cfg, err := image.ConfigFile()
	if err != nil || cfg.OS != "linux" || cfg.Architecture != "amd64" {
		return nil, "", errors.New("image must support linux/amd64")
	}
	return image, ref.Context().Digest(digest.String()).Name(), nil
}
func (c *Client) Resolve(ctx context.Context, reference string) (string, error) {
	_, pinned, err := c.Fetch(ctx, reference)
	return pinned, err
}

func (c *Client) Push(ctx context.Context, image v1.Image) (string, error) {
	digest, err := image.Digest()
	if err != nil {
		return "", errors.New("invalid bundled image")
	}
	repository := c.Repository
	if repository == "" {
		repository = "simworkflow/reproduced"
	}
	ref, err := agentbuild.Reference(c.Config.Registry.URL, repository, "sha-"+digest.Hex)
	if err != nil {
		return "", err
	}
	options, err := c.options(ctx, ref.RegistryStr())
	if err != nil {
		return "", err
	}
	if err := remote.Write(ref, image, options...); err != nil {
		return "", errors.New("could not upload bundled image")
	}
	descriptor, err := remote.Head(ref, options...)
	if err != nil || descriptor.Digest != digest {
		return "", errors.New("uploaded image digest verification failed")
	}
	return ref.Context().Digest(digest.String()).Name(), nil
}

type transport struct {
	origin  *url.URL
	base    http.RoundTripper
	timeout time.Duration
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	same := strings.EqualFold(r.URL.Host, t.origin.Host)
	if r.URL.User != nil || (!same && (r.Header.Get("Authorization") != "" || r.URL.Scheme != "https" || r.Method != "GET" && r.Method != "HEAD")) {
		return nil, errors.New("cross-origin registry authentication or upload rejected")
	}
	request := r.Clone(r.Context())
	u := *r.URL
	request.URL = &u
	if same {
		request.URL.Scheme = t.origin.Scheme
	}
	timeout := t.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if strings.Contains(request.URL.Path, "/blobs/") && timeout < 10*time.Minute {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(request.Context(), timeout)
	request = request.WithContext(ctx)
	response, err := t.base.RoundTrip(request)
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &body{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type body struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *body) Close() error { defer b.cancel(); return b.ReadCloser.Close() }
