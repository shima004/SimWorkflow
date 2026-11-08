package reproduction

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"simworkflow/internal/operation"
	"simworkflow/internal/resultarchive"
	"simworkflow/internal/storage"
)

func exportZIPReference(ctx context.Context, store *storage.Store, key, destination string, logs bool) (bool, error) {
	r, size, err := store.OpenReaderAt(ctx, key)
	if storage.IsNotFound(err) {
		return false, nil
	}
	if err != nil && !errors.Is(err, storage.ErrRangeUnsupported) {
		return true, err
	}
	var z *resultarchive.Reader
	if err == nil {
		z, err = resultarchive.NewReader(r, size, nil)
	}
	if errors.Is(err, storage.ErrRangeUnsupported) {
		body, err := store.Open(ctx, key)
		if err != nil {
			return true, err
		}
		defer body.Close()
		f, err := os.CreateTemp("", "simworkflow-reference-*.zip")
		if err != nil {
			return true, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		limit := int64(resultarchive.MaxExpanded) + resultarchive.MaxIndex
		n, copyErr := io.Copy(f, io.LimitReader(contextReader{ctx, body}, limit+1))
		if copyErr != nil {
			return true, errors.New("reference ZIP transfer interrupted")
		}
		if n > limit {
			return true, errors.New("reference ZIP exceeds size limit")
		}
		z, err = resultarchive.NewReader(f, n, nil)
		if err != nil {
			return true, err
		}
	} else if err != nil {
		return true, err
	}
	for _, file := range z.File {
		if file.FileInfo().IsDir() || !(strings.HasPrefix(file.Name, "modules/") || strings.HasPrefix(file.Name, "scenario/config/") || logs && strings.HasPrefix(file.Name, "logs/")) {
			continue
		}
		r, err := z.OpenFile(file)
		if err != nil {
			return true, err
		}
		err = copyFile(ctx, filepath.Join(destination, filepath.FromSlash(file.Name)), operation.Reader(ctx, "Export result file", int64(file.UncompressedSize64), r), int64(file.UncompressedSize64))
		closeErr := r.Close()
		if err != nil {
			return true, err
		}
		if closeErr != nil {
			return true, closeErr
		}
	}
	return true, nil
}
