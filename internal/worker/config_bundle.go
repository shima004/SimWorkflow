package worker

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

const ConfigBundleCapabilityLabel = "io.simworkflow.config-bundle"
const maxConfigBundleBytes = int64(64 << 20)
const maxConfigBundleExpanded = int64(256 << 20)
const maxRunConfigBytes = 1 << 20

var bundleRunID = regexp.MustCompile(`^run-[a-f0-9]{32}$`)

type ConfigBundleReference struct {
	URL    string
	SHA256 string
}

type configBundleManifest struct {
	Schema      string `json:"schema"`
	ExecutionID string `json:"executionId"`
	Stage       string `json:"stage"`
	Runs        int    `json:"runs"`
}

// CreateConfigBundle writes one stage only. The caller removes the private file.
func CreateConfigBundle(ctx context.Context, executionID, stage string, configs []Config) (path, digest string, err error) {
	if executionID == "" || (stage != Precompute && stage != Simulation) || len(configs) == 0 || len(configs) > experiment.DefaultMaxRuns {
		return "", "", errors.New("invalid configuration bundle inputs")
	}
	f, err := os.CreateTemp("", "simworkflow-config-*.zip")
	if err != nil {
		return "", "", err
	}
	path = f.Name()
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	z := zip.NewWriter(io.MultiWriter(f, h))
	write := func(name string, data []byte) error {
		entry, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}
	manifest, _ := json.Marshal(configBundleManifest{Schema: "simworkflow/config-bundle/v1", ExecutionID: executionID, Stage: stage, Runs: len(configs)})
	if err = write("manifest.json", manifest); err != nil {
		return path, "", err
	}
	seen := map[string]bool{}
	expanded := int64(len(manifest))
	for _, c := range configs {
		if err = ctx.Err(); err != nil {
			return path, "", err
		}
		if !bundleRunID.MatchString(c.Run.ID) || seen[c.Run.ID] || !validBundleConfig(c, executionID, stage, c.Run.ID) {
			return path, "", errors.New("duplicate, mismatched or incorrectly scoped bundle configuration")
		}
		seen[c.Run.ID] = true
		data, e := json.Marshal(c)
		if e != nil || len(data) > maxRunConfigBytes {
			return path, "", errors.New("invalid or oversized bundled run configuration")
		}
		expanded += int64(len(data))
		if expanded > maxConfigBundleExpanded {
			return path, "", errors.New("configuration bundle exceeds expanded size limit")
		}
		if err = write(c.Run.ID+".json", data); err != nil {
			return path, "", err
		}
	}
	if err = z.Close(); err != nil {
		return path, "", err
	}
	info, e := f.Stat()
	if e != nil || info.Size() > maxConfigBundleBytes {
		return path, "", errors.New("configuration bundle exceeds 64MiB limit")
	}
	if err = f.Close(); err != nil {
		return path, "", err
	}
	return path, hex.EncodeToString(h.Sum(nil)), nil
}

func validBundleConfig(c Config, executionID, stage, runID string) bool {
	if c.ExecutionID != executionID || c.Stage != stage || c.Run.ID != runID || c.TimeoutSeconds <= 0 {
		return false
	}
	if stage == Precompute {
		return c.PrecomputeUpload != nil && c.PrecomputeDownload == nil
	}
	return stage == Simulation && c.PrecomputeDownload != nil && c.PrecomputeUpload == nil
}

type RunConfigSource struct {
	Inline, Remote, BundleURL, BundleSHA256, RunID, Stage, ExecutionID string
}

func ReadRunConfigSource(ctx context.Context, source RunConfigSource) (Config, error) {
	bundle := source.BundleURL != "" || source.BundleSHA256 != "" || source.RunID != "" || source.Stage != "" || source.ExecutionID != ""
	if !bundle {
		return ReadRunConfig(ctx, source.Inline, source.Remote)
	}
	if source.Inline != "" || source.Remote != "" || !validRunConfigURL(source.BundleURL) || !experiment.ValidSHA256(source.BundleSHA256) || !bundleRunID.MatchString(source.RunID) || source.ExecutionID == "" || (source.Stage != Precompute && source.Stage != Simulation) {
		return Config{}, errors.New("invalid or conflicting configuration bundle source")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	response, err := storage.Request(ctx, http.MethodGet, storage.Transfer{URL: source.BundleURL}, nil)
	if err != nil {
		return Config{}, errors.New("could not download configuration bundle")
	}
	defer response.Body.Close()
	f, err := os.CreateTemp("", "simworkflow-config-download-*.zip")
	if err != nil {
		return Config{}, errors.New("could not create temporary configuration bundle")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(response.Body, maxConfigBundleBytes+1))
	if copyErr != nil || n > maxConfigBundleBytes || hex.EncodeToString(h.Sum(nil)) != source.BundleSHA256 {
		return Config{}, errors.New("configuration bundle failed size or SHA-256 verification")
	}
	z, err := zip.NewReader(f, n)
	if err != nil || len(z.File) < 2 || len(z.File) > experiment.DefaultMaxRuns+1 {
		return Config{}, errors.New("invalid configuration bundle ZIP")
	}
	entries := map[string]*zip.File{}
	var expanded uint64
	for _, entry := range z.File {
		if !entry.Mode().IsRegular() || entries[entry.Name] != nil || entry.UncompressedSize64 > maxRunConfigBytes || (entry.Name != "manifest.json" && !bundleRunID.MatchString(strings.TrimSuffix(entry.Name, ".json"))) || (entry.Name != "manifest.json" && !strings.HasSuffix(entry.Name, ".json")) {
			return Config{}, errors.New("unsafe, duplicate or oversized configuration bundle member")
		}
		expanded += entry.UncompressedSize64
		if expanded > uint64(maxConfigBundleExpanded) {
			return Config{}, errors.New("configuration bundle exceeds expanded size limit")
		}
		entries[entry.Name] = entry
	}
	decode := func(name string, limit int64, target any) error {
		entry := entries[name]
		if entry == nil || entry.UncompressedSize64 > uint64(limit) {
			return errors.New("missing or oversized bundle entry")
		}
		body, err := entry.Open()
		if err != nil {
			return err
		}
		defer body.Close()
		data, err := io.ReadAll(io.LimitReader(body, limit+1))
		if err != nil || int64(len(data)) > limit {
			return errors.New("invalid bundle entry")
		}
		return json.Unmarshal(data, target)
	}
	var manifest configBundleManifest
	if decode("manifest.json", 4096, &manifest) != nil || manifest.Schema != "simworkflow/config-bundle/v1" || manifest.Stage != source.Stage || manifest.ExecutionID != source.ExecutionID || manifest.Runs != len(entries)-1 {
		return Config{}, errors.New("configuration bundle manifest mismatch")
	}
	var c Config
	if decode(source.RunID+".json", maxRunConfigBytes, &c) != nil || !validBundleConfig(c, source.ExecutionID, source.Stage, source.RunID) {
		return Config{}, errors.New("configuration bundle run mismatch or invalid permissions")
	}
	return c, nil
}
