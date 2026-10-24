package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"simworkflow/internal/storage"
)

// ReadRunConfig keeps the legacy inline format and supports a private S3
// configuration fetched through one signed GET, reducing large Workflow specs.
func ReadRunConfig(ctx context.Context, inline, remote string) (Config, error) {
	var c Config
	if (inline == "") == (remote == "") {
		return c, errors.New("set exactly one of SIMWORKFLOW_RUN or SIMWORKFLOW_RUN_URL")
	}
	var body io.Reader = strings.NewReader(inline)
	if remote != "" {
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return c, errors.New("invalid run configuration URL")
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resp, err := storage.Request(ctx, http.MethodGet, storage.Transfer{URL: remote}, nil)
		if err != nil {
			return c, errors.New("could not download run configuration")
		}
		defer resp.Body.Close()
		body = resp.Body
	}
	const maxConfigBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(body, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes || json.Unmarshal(data, &c) != nil {
		return Config{}, errors.New("invalid or oversized run configuration JSON")
	}
	return c, nil
}
