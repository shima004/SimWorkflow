package reproduction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/maparchive"
	"simworkflow/internal/operation"
	"simworkflow/internal/storage"
)

func saveMap(ctx context.Context, store *storage.Store, path, sha string) (string, error) {
	if !experiment.ValidSHA256(sha) {
		return "", errors.New("invalid map digest")
	}
	key := "reproduction/maps/" + sha + ".zip"
	// Avoid sending the entire body of an already stored snapshot. A HEAD is
	// only an existence check: actual bytes are still verified below.
	_, err := store.Head(ctx, key)
	if storage.IsNotFound(err) {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		err = store.PutImmutable(ctx, key, f, "application/zip", map[string]string{"sha256": sha})
		f.Close()
		if err != nil && !errors.Is(err, storage.ErrAlreadyExists) {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	// Verify actual bytes for both existing and new objects, independently of
	// the S3 service's metadata and conditional-write implementation.
	body, err := store.Open(ctx, key)
	if err != nil {
		return "", err
	}
	defer body.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx, body}, maparchive.MaxBytes+1))
	if err != nil || n > maparchive.MaxBytes || hex.EncodeToString(h.Sum(nil)) != sha {
		return "", errors.New("stored map snapshot failed SHA-256 verification")
	}
	return "s3://" + store.Bucket + "/" + key, nil
}

// snapshotInputs deduplicates sources and recorded digests before running map
// transfers. Apply the verified references afterward to retain alias order and
// avoid mutating the caller's input or sharing result slots between workers.
func snapshotInputs(ctx context.Context, c config.Config, e *experiment.Experiment) error {
	type job struct{ source, expected string }
	jobs := []job{}
	indexes := map[string]int{}
	for _, m := range e.Spec.Conditions.Maps {
		if i, ok := indexes[m.Location()]; ok {
			if expected := m.ExpectedSHA256(); expected != "" {
				if jobs[i].expected != "" && jobs[i].expected != expected {
					return errors.New("map aliases disagree on SHA-256")
				}
				jobs[i].expected = expected
			}
		} else {
			indexes[m.Location()] = len(jobs)
			jobs = append(jobs, job{m.Location(), m.ExpectedSHA256()})
		}
	}
	// A recorded digest identifies content independently of its catalog name or
	// source location. Verify that content once while retaining all aliases.
	unique := make([]job, 0, len(jobs))
	byContent := map[string]int{}
	for _, j := range jobs {
		key := "source:" + j.source
		if j.expected != "" {
			key = "sha256:" + j.expected
		}
		i, ok := byContent[key]
		if !ok {
			i = len(unique)
			byContent[key] = i
			unique = append(unique, j)
		}
		indexes[j.source] = i
	}
	jobs = unique
	type snapshot struct{ source, sha string }
	results := make([]snapshot, len(jobs))
	store := storage.New(c)
	if err := operation.For(ctx, "Verify and snapshot maps", len(jobs), func(ctx context.Context, i int) error {
		location, sha, err := snapshotMap(ctx, store, jobs[i].source, jobs[i].expected)
		results[i] = snapshot{location, sha}
		return err
	}); err != nil {
		return err
	}
	for i, m := range e.Spec.Conditions.Maps {
		saved := results[indexes[m.Location()]]
		if m.Resolved != nil {
			copy := *m.Resolved
			copy.Source = saved.source
			m.Resolved = &copy
			m.Version = copy.Version
		} else {
			m.Source, m.SHA256 = saved.source, saved.sha
		}
		e.Spec.Conditions.Maps[i] = m
	}
	return nil
}

func snapshotMap(ctx context.Context, store *storage.Store, source, expected string) (string, string, error) {
	if expected != "" {
		if !experiment.ValidSHA256(expected) {
			return "", "", errors.New("invalid map digest")
		}
		key := "reproduction/maps/" + expected + ".zip"
		// GET directly: a HEAD adds a round trip without verifying content.
		// Neither the object name nor its metadata proves integrity.
		body, err := store.Open(ctx, key)
		if err == nil {
			path, _, err := readMapArchive(ctx, body, expected)
			if err != nil {
				return "", "", fmt.Errorf("stored map snapshot verification: %w", err)
			}
			os.Remove(path)
			return "s3://" + store.Bucket + "/" + key, expected, nil
		}
		if !storage.IsNotFound(err) {
			return "", "", err
		}
	}
	transfer, err := store.Download(ctx, source)
	if err != nil {
		return "", "", err
	}
	response, err := storage.Request(ctx, "GET", transfer, nil)
	if err != nil {
		return "", "", err
	}
	path, info, err := readMapArchive(ctx, response.Body, expected)
	if err != nil {
		return "", "", err
	}
	defer os.Remove(path)
	location, err := saveMap(ctx, store, path, info.SHA256)
	return location, info.SHA256, err
}

// Keep byte-level SHA-256 and full archive/XML validation on reused snapshots,
// not just on new maps. The caller owns the returned temporary file.
func readMapArchive(ctx context.Context, body io.ReadCloser, expected string) (path string, info maparchive.Info, err error) {
	defer body.Close()
	file, err := os.CreateTemp("", "simworkflow-map-snapshot-*.zip")
	if err != nil {
		return "", info, err
	}
	path = file.Name()
	defer func() {
		if err != nil {
			os.Remove(path)
		}
	}()
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx, body}, maparchive.MaxBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || n > maparchive.MaxBytes {
		return path, info, errors.New("could not snapshot map within 2GiB limit")
	}
	if expected != "" && expected != hex.EncodeToString(hash.Sum(nil)) {
		return path, info, errors.New("map does not match recorded SHA-256")
	}
	info, err = maparchive.Inspect(path)
	return path, info, err
}
