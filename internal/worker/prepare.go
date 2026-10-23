package worker

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

const maxMapBytes int64 = 2 << 30

func Prepare(ctx context.Context, root string, c Config) error {
	if c.TimeoutSeconds <= 0 || c.Run.ID == "" {
		return errors.New("invalid run configuration")
	}
	if err := os.MkdirAll(root, 0777); err != nil {
		return err
	}
	if err := os.Chmod(root, 0777); err != nil {
		return err
	}
	for _, dir := range []string{"state", "logs", "precompute", "modules"} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0777); err != nil {
			return err
		}
		if err := os.Chmod(path, 0777); err != nil {
			return err
		}
	}
	for _, role := range experiment.Roles() {
		for _, directory := range []string{"modules", "precompute"} {
			path := filepath.Join(root, directory, role)
			if err := os.MkdirAll(path, 0777); err != nil {
				return err
			}
			if err := os.Chmod(path, 0777); err != nil {
				return err
			}
		}
	}
	if err := writeJSON(filepath.Join(root, "run.json"), c); err != nil {
		return err
	}
	resp, err := storage.Request(ctx, http.MethodGet, c.Map, nil)
	if err != nil {
		return fmt.Errorf("map download: %w", err)
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(root, ".map-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxMapBytes+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		return errors.New("could not save map archive")
	}
	if n > maxMapBytes {
		return errors.New("map archive exceeds 2GiB limit")
	}
	if err := extractZIP(f.Name(), filepath.Join(root, "scenario")); err != nil {
		return err
	}
	for _, path := range []string{"map/scenario.xml", "config/kernel.cfg", "config/common.cfg"} {
		if _, err := os.Stat(filepath.Join(root, "scenario", path)); err != nil {
			return fmt.Errorf("map archive must contain %s", path)
		}
	}
	return seedConfig(filepath.Join(root, "scenario", "config"), c.Run.Seed)
}

func extractZIP(archive, destination string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return errors.New("map must be a ZIP archive")
	}
	defer r.Close()
	if len(r.File) > 10000 {
		return errors.New("map archive contains too many files")
	}
	var size uint64
	seen := map[string]bool{}
	for _, entry := range r.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || name == "." {
			continue
		}
		if !fs.ValidPath(name) || strings.Contains(name, "\\") || entry.Mode()&os.ModeSymlink != 0 {
			return errors.New("map archive contains an unsafe path or symlink")
		}
		if seen[name] {
			return errors.New("map archive contains duplicate paths")
		}
		seen[name] = true
		if entry.UncompressedSize64 > uint64(maxMapBytes)-size {
			return errors.New("expanded map exceeds 2GiB limit")
		}
		size += entry.UncompressedSize64
		path := filepath.Join(destination, filepath.FromSlash(name))
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return errors.New("map archive contains a non-regular file")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			source.Close()
			return err
		}
		n, copyErr := io.Copy(target, io.LimitReader(source, int64(entry.UncompressedSize64)+1))
		source.Close()
		closeErr := target.Close()
		if copyErr != nil || closeErr != nil || n != int64(entry.UncompressedSize64) {
			return errors.New("invalid or truncated map archive entry")
		}
	}
	return nil
}

var seedLine = regexp.MustCompile(`(?m)^\s*random\.seed\s*:[^\r\n]*`)

func seedConfig(dir string, seed int64) error {
	changed := false
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".cfg") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !seedLine.Match(data) {
			return nil
		}
		changed = true
		return os.WriteFile(path, seedLine.ReplaceAll(data, []byte(fmt.Sprintf("random.seed: %d", seed))), 0644)
	})
	if err != nil {
		return err
	}
	if !changed {
		f, err := os.OpenFile(filepath.Join(dir, "common.cfg"), os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(f, "\nrandom.seed: %d\n", seed)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return nil
}
