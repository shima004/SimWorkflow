package cliupdate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

func TestPublicSourcePaginatesAndNeverSendsCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "private-secret")
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("public updates must not use service/GitHub credentials")
		}
		pages++
		if r.URL.Query().Get("page") == "1" {
			values := make([]map[string]any, 100)
			for i := range values {
				values[i] = map[string]any{"tag_name": "v0.1.0", "draft": i == 0}
			}
			json.NewEncoder(w).Encode(values)
		} else {
			io.WriteString(w, `[{"tag_name":"v0.2.0","draft":false}]`)
		}
	}))
	defer server.Close()
	source := &PublicSource{Client: server.Client(), API: server.URL, Downloads: server.URL}
	releases, err := source.ListReleases(context.Background(), selfupdate.ParseSlug(Repository))
	if err != nil || pages != 2 || len(releases) != 100 || releases[99].GetTagName() != "v0.2.0" {
		t.Fatalf("pagination/draft filtering failed: %d %d %v", pages, len(releases), err)
	}
}

func TestPublicDownloadsHaveOriginAndSizeLimits(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; io.WriteString(w, strings.Repeat("a", 20)) }))
	defer server.Close()
	source := &PublicSource{Client: server.Client(), API: server.URL, Downloads: server.URL}
	release := &selfupdate.Release{AssetID: 1, AssetURL: "https://foreign.invalid/" + Repository + "/releases/download/v1.0.0/asset.tar.gz"}
	if _, err := source.DownloadReleaseAsset(context.Background(), release, 1); err == nil || requests != 0 {
		t.Fatal("off-origin downloads must be rejected before network")
	}
	release.AssetURL = server.URL + "/" + Repository + "/releases/download/v1.0.0/asset.tar.gz"
	body, err := source.DownloadReleaseAsset(context.Background(), release, 1)
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	if _, err := source.get(context.Background(), release.AssetURL, 10); err == nil {
		t.Fatal("oversized response must be rejected")
	}
	redirect := NewPublicSource().Client.CheckRedirect
	for _, address := range []string{"http://github.com/file", "https://foreign.invalid/file", "https://user:password@github.com/file"} {
		parsed, _ := url.Parse(address)
		if err := redirect(&http.Request{URL: parsed}, nil); err == nil {
			t.Fatal("unsafe redirect accepted")
		}
	}
}
