package reproduction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"simworkflow/internal/experiment"
	"simworkflow/internal/maparchive"
	"simworkflow/internal/storage"
)

func saveMap(ctx context.Context, store *storage.Store, path, sha string) (string, error) {
	if !experiment.ValidSHA256(sha) {
		return "", errors.New("invalid map digest")
	}
	key := "reproduction/maps/" + sha + ".zip"
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	err = store.PutImmutable(ctx, key, f, "application/zip", map[string]string{"sha256": sha})
	f.Close()
	if err != nil && !errors.Is(err, storage.ErrAlreadyExists) {
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
