package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/maparchive"
	"simworkflow/internal/storage"
)

const maxMapBytes int64 = 2 << 30

func Prepare(ctx context.Context, root string, c Config) error {
	if c.TimeoutSeconds <= 0 || c.Run.ID == "" || (c.Stage != Precompute && c.Stage != Simulation) {
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
	if c.Stage == Precompute {
		c.Deadline = time.Now().UTC().Add(time.Duration(c.TimeoutSeconds) * time.Second)
	} else if err := restoreHandoff(ctx, root, &c); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, c.Deadline)
	defer cancel()
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
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, maxMapBytes+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		return errors.New("could not save map archive")
	}
	if n > maxMapBytes {
		return errors.New("map archive exceeds 2GiB limit")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if c.Stage == Simulation && c.MapSHA256 != digest {
		return errors.New("map archive changed since precompute")
	}
	if expected := c.Run.Map.ExpectedSHA256(); expected != "" && expected != digest {
		return errors.New("map SHA-256 does not match the pinned experiment input")
	}
	c.MapSHA256 = digest
	if err := extractZIP(f.Name(), filepath.Join(root, "scenario")); err != nil {
		return err
	}
	for _, path := range []string{"map/scenario.xml", "config/kernel.cfg", "config/common.cfg"} {
		if _, err := os.Stat(filepath.Join(root, "scenario", path)); err != nil {
			return fmt.Errorf("map archive must contain %s", path)
		}
	}
	if err := seedConfig(filepath.Join(root, "scenario", "config"), c.Run.Seed); err != nil {
		return err
	}
	if p := c.Run.Simulator.Parameters; p != nil && p.Timesteps != nil {
		if err := setTimesteps(filepath.Join(root, "scenario/config"), *p.Timesteps); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(root, "run.json"), c)
}

func extractZIP(archive, destination string) error {
	return maparchive.ExtractZIP(archive, destination)
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

var timestepLine = regexp.MustCompile(`(?m)^\s*kernel\.timesteps\s*:[^\r\n]*`)

func setTimesteps(dir string, steps int) error {
	if steps <= 0 || int64(steps) > 2147483647 {
		return errors.New("invalid simulation timestep count")
	}
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
		if !timestepLine.Match(data) {
			return nil
		}
		changed = true
		return os.WriteFile(path, timestepLine.ReplaceAll(data, []byte(fmt.Sprintf("kernel.timesteps: %d", steps))), 0644)
	})
	if err != nil {
		return err
	}
	if !changed {
		f, err := os.OpenFile(filepath.Join(dir, "kernel.cfg"), os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(f, "\nkernel.timesteps: %d\n", steps)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return nil
}
