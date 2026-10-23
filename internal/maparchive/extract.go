// Package maparchive validates and packages simulation map inputs.
package maparchive

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const MaxBytes int64 = 2 << 30

func ExtractZIP(archive, destination string) error {
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
		if entry.UncompressedSize64 > uint64(MaxBytes)-size {
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
