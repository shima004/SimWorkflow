// Package cliupdate updates distributed CLI binaries without loading service settings.
package cliupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/google/go-github/v30/github"
)

const Repository = "shima004/SimWorkflow"

// PublicSource deliberately uses no Argo/S3/registry credentials or GitHub token.
// The custom source adds pagination, deadlines and response size limits.
type PublicSource struct {
	Client         *http.Client
	API, Downloads string
}

func NewPublicSource() *PublicSource {
	return &PublicSource{API: "https://api.github.com", Downloads: "https://github.com", Client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || r.URL.Scheme != "https" || r.URL.User != nil || !(r.URL.Hostname() == "github.com" || r.URL.Hostname() == "api.github.com" || strings.HasSuffix(r.URL.Hostname(), ".githubusercontent.com")) {
			return errors.New("unsupported release redirect")
		}
		return nil
	}}}
}

func (s *PublicSource) get(ctx context.Context, address string, limit int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("invalid release request")
	}
	req.Header.Set("User-Agent", "simworkflow-self-update")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, errors.New("GitHub release request failed")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub release request returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, errors.New("release response exceeds size limit")
	}
	return http.MaxBytesReader(nil, resp.Body, limit), nil
}

func (s *PublicSource) ListReleases(ctx context.Context, repository selfupdate.Repository) ([]selfupdate.SourceRelease, error) {
	owner, repo, err := repository.GetSlug()
	if err != nil || owner+"/"+repo != Repository {
		return nil, errors.New("unsupported update repository")
	}
	var result []selfupdate.SourceRelease
	for page := 1; page <= 20; page++ {
		body, err := s.get(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", s.API, Repository, page), 4<<20)
		if err != nil {
			return nil, err
		}
		var releases []*github.RepositoryRelease
		err = json.NewDecoder(body).Decode(&releases)
		body.Close()
		if err != nil {
			return nil, errors.New("invalid GitHub release response")
		}
		for _, release := range releases {
			if release != nil && !release.GetDraft() {
				result = append(result, selfupdate.NewGitHubRelease(release))
			}
		}
		if len(releases) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("too many release pages; specify a newer distribution")
}

func (s *PublicSource) DownloadReleaseAsset(ctx context.Context, release *selfupdate.Release, id int64) (io.ReadCloser, error) {
	if release == nil {
		return nil, errors.New("missing release")
	}
	address, limit := "", int64(128<<20)
	if id == release.AssetID {
		address = release.AssetURL
	} else if id == release.ValidationAssetID {
		address = release.ValidationAssetURL
		limit = 256 << 10
	}
	u, err := url.Parse(address)
	base, baseErr := url.Parse(s.Downloads)
	if err != nil || baseErr != nil || u.User != nil || u.Scheme != base.Scheme || u.Host != base.Host || !strings.HasPrefix(u.Path, "/"+Repository+"/releases/download/") {
		return nil, errors.New("release asset is outside the official repository")
	}
	return s.get(ctx, address, limit)
}
