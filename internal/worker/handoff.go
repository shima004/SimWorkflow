package worker

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

type handoffManifest struct {
	Version     int       `json:"version"`
	ExecutionID string    `json:"executionId"`
	RunID       string    `json:"runId"`
	Deadline    time.Time `json:"deadline"`
	MapSHA256   string    `json:"mapSHA256"`
}

func makeHandoff(root, destination string, c Config) (err error) {
	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	defer func() {
		for _, close := range []func() error{z.Close, f.Close} {
			if e := close(); err == nil && e != nil {
				err = e
			}
		}
	}()
	manifest := handoffManifest{Version: 1, ExecutionID: c.ExecutionID, RunID: c.Run.ID, Deadline: c.Deadline, MapSHA256: c.MapSHA256}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	entry, err := z.Create("handoff.json")
	if err != nil {
		return err
	}
	if _, err = entry.Write(data); err != nil {
		return err
	}
	size := int64(len(data))
	count := 1
	return filepath.WalkDir(filepath.Join(root, "precompute"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("precompute handoff requires regular files")
		}
		count++
		if count > 10000 || info.Size() > maxMapBytes-size {
			return errors.New("precompute handoff exceeds archive limits")
		}
		size += info.Size()
		name, _ := filepath.Rel(root, path)
		target, err := z.Create(filepath.ToSlash(name))
		if err != nil {
			return err
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(target, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func restoreHandoff(ctx context.Context, root string, c *Config) error {
	if c.PrecomputeDownload == nil {
		return errors.New("simulation requires a precompute download")
	}
	archive := filepath.Join(root, ".precompute.zip")
	defer os.Remove(archive)
	if err := downloadFile(ctx, archive, *c.PrecomputeDownload); err != nil {
		return errors.New("precompute download failed")
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		return errors.New("invalid precompute ZIP")
	}
	var manifest handoffManifest
	found := false
	for _, entry := range z.File {
		if entry.Name == "handoff.json" {
			if found || entry.UncompressedSize64 > 4096 {
				z.Close()
				return errors.New("invalid precompute manifest")
			}
			f, err := entry.Open()
			if err != nil {
				z.Close()
				return err
			}
			err = json.NewDecoder(io.LimitReader(f, 4097)).Decode(&manifest)
			f.Close()
			if err != nil {
				z.Close()
				return errors.New("invalid precompute manifest")
			}
			found = true
			continue
		}
		parts := strings.Split(strings.TrimSuffix(entry.Name, "/"), "/")
		if len(parts) < 2 || parts[0] != "precompute" || !experiment.ValidRole(parts[1]) {
			z.Close()
			return errors.New("unexpected path in precompute handoff")
		}
	}
	z.Close()
	if !found || manifest.Version != 1 || manifest.ExecutionID != c.ExecutionID || manifest.RunID != c.Run.ID || len(manifest.MapSHA256) != 64 || manifest.Deadline.IsZero() {
		return errors.New("precompute handoff does not match this execution and run")
	}
	if !time.Now().Before(manifest.Deadline) {
		return errors.New("run deadline expired before simulation")
	}
	if err := extractZIP(archive, root); err != nil {
		return err
	}
	c.Deadline = manifest.Deadline
	c.MapSHA256 = manifest.MapSHA256
	return nil
}

func downloadFile(ctx context.Context, destination string, transfer storage.Transfer) error {
	response, err := storage.Request(ctx, http.MethodGet, transfer, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(response.Body, maxMapBytes+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		return errors.New("could not save downloaded archive")
	}
	if n > maxMapBytes {
		return errors.New("downloaded archive exceeds 2GiB limit")
	}
	return nil
}
