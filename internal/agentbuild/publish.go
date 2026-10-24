package agentbuild

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"simworkflow/internal/config"
)

var repositoryComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*$`)
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

func Reference(endpoint, repository, tag string) (name.Tag, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return name.Tag{}, errors.New("REGISTRY_URL for agent builds must be an http(s) registry root URL without credentials, path, query or fragment")
	}
	if len(repository) == 0 || len(repository) > 255 {
		return name.Tag{}, errors.New("repository must be a relative image repository path of 1-255 characters")
	}
	for _, part := range strings.Split(repository, "/") {
		if !repositoryComponent.MatchString(part) {
			return name.Tag{}, errors.New("repository must contain lowercase repository components, without a registry host, tag or digest")
		}
	}
	if !tagPattern.MatchString(tag) {
		return name.Tag{}, errors.New("tag must contain 1-128 letters, digits, underscores, dots or hyphens and not begin with a dot or hyphen")
	}
	options := []name.Option{name.StrictValidation}
	if u.Scheme == "http" {
		options = append(options, name.Insecure)
	}
	ref, err := name.NewTag(strings.ToLower(u.Host)+"/"+repository+":"+tag, options...)
	if err != nil {
		return name.Tag{}, errors.New("invalid image reference for the configured registry")
	}
	if ref.RegistryStr() != strings.ToLower(u.Host) {
		return name.Tag{}, errors.New("REGISTRY_URL must use an explicit canonical registry hostname; single-label hosts need a port")
	}
	return ref, nil
}

func CheckPlatform(archive, platform string) error {
	image, err := tarball.ImageFromPath(archive, nil)
	if err != nil {
		return errors.New("Docker export is not a readable single-image archive")
	}
	c, err := image.ConfigFile()
	if err != nil {
		return errors.New("could not inspect exported image platform")
	}
	want, err := v1.ParsePlatform(platform)
	if err != nil {
		return errors.New("invalid target platform")
	}
	if c.OS != want.OS || c.Architecture != want.Architecture {
		return errors.New("built image platform does not match --platform")
	}
	if want.Variant != "" && c.Variant != want.Variant && !(want.Architecture == "arm64" && want.Variant == "v8" && c.Variant == "") {
		return errors.New("built image variant does not match --platform")
	}
	return nil
}

func Publish(ctx context.Context, c config.Registry, timeout time.Duration, archive, reference string) (string, error) {
	return publish(ctx, c, timeout, archive, reference, http.DefaultTransport.(*http.Transport).Clone())
}

func publish(ctx context.Context, c config.Registry, timeout time.Duration, archive, reference string, base http.RoundTripper) (string, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return "", errors.New("invalid registry URL")
	}
	ref, err := name.NewTag(reference, name.StrictValidation)
	if err != nil {
		return "", errors.New("invalid image reference")
	}
	// Validate the reference against the configured destination again at the
	// upload boundary. No Docker credential/config file is consulted or written.
	expected, err := Reference(c.URL, ref.RepositoryStr(), ref.TagStr())
	if err != nil || expected.Name() != ref.Name() {
		return "", errors.New("image reference does not match REGISTRY_URL")
	}
	ref = expected
	image, err := tarball.ImageFromPath(archive, nil)
	if err != nil {
		return "", errors.New("could not read exported image")
	}
	digest, err := image.Digest()
	if err != nil {
		return "", errors.New("could not calculate exported image digest")
	}
	auth := authn.FromConfig(authn.AuthConfig{Username: c.Username, Password: c.Password, RegistryToken: c.Token})
	if c.Username == "" && c.Password == "" && c.Token == "" {
		auth = authn.Anonymous
	}
	options := []remote.Option{remote.WithContext(ctx), remote.WithAuth(auth), remote.WithTransport(&registryTransport{origin: u, inner: base, timeout: timeout})}
	if err := remote.Write(ref, image, options...); err != nil {
		return "", safeRegistryError("upload", err)
	}
	descriptor, err := remote.Head(ref, options...)
	if err != nil {
		return "", safeRegistryError("verification", err)
	}
	if descriptor.Digest != digest {
		return "", errors.New("registry tag digest changed during verification; use a unique tag and retry")
	}
	return ref.Context().Digest(digest.String()).Name(), nil
}

func safeRegistryError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("registry operation cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("registry request timed out")
	}
	var status *transport.Error
	if errors.As(err, &status) {
		return fmt.Errorf("registry %s failed (HTTP %d)", operation, status.StatusCode)
	}
	return fmt.Errorf("registry %s failed; check the configured URL, TLS certificate, credentials and repository permissions", operation)
}

type registryTransport struct {
	origin  *url.URL
	inner   http.RoundTripper
	timeout time.Duration
}

func (t *registryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.User != nil || !strings.EqualFold(r.URL.Host, t.origin.Host) || (r.Host != "" && !strings.EqualFold(r.Host, t.origin.Host)) {
		return nil, errors.New("registry authentication and uploads must stay on the configured registry origin")
	}
	timeout := t.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	req := r.Clone(ctx)
	copyURL := *r.URL
	req.URL = &copyURL
	// The library normally guesses HTTP for private/loopback addresses. The
	// user's scheme is authoritative, including explicit HTTPS on private IPs.
	req.URL.Scheme = t.origin.Scheme
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error { defer b.cancel(); return b.ReadCloser.Close() }
