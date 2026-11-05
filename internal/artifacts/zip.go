package artifacts

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"simworkflow/internal/resultarchive"
	"simworkflow/internal/storage"
)

type randomAccessSource interface {
	OpenReaderAt(context.Context, string) (io.ReaderAt, int64, error)
}

func (d *downloader) archive(key, local, stage string, files []string, extract bool) error {
	if source, ok := d.source.(randomAccessSource); ok {
		zipKey := strings.TrimSuffix(key, "bundle.tar.gz") + "bundle.zip"
		handled, err := d.zipArchive(source, zipKey, local, stage, files, extract)
		if handled || err != nil {
			return err
		}
	}
	return d.tarArchive(key, local, stage, files, extract)
}

func (d *downloader) zipArchive(source randomAccessSource, key, local, stage string, files []string, extract bool) (bool, error) {
	d.emit("indexing", local+"/bundle.zip", 0, -1)
	r, size, err := source.OpenReaderAt(d.ctx, key)
	if storage.IsNotFound(err) {
		return false, nil // Only a missing ZIP permits trying the legacy format.
	}
	if err != nil && !errors.Is(err, storage.ErrRangeUnsupported) {
		return true, err
	}
	if !extract {
		return true, d.object(key, local+"/bundle.zip")
	}
	if d.selected["bundle"] || errors.Is(err, storage.ErrRangeUnsupported) {
		return true, d.fullZIP(key, local, stage, files)
	}
	z, err := resultarchive.NewReader(r, size, func(n int) {
		d.progress.TransferredBytes += int64(n)
		if d.progress.Phase == "indexing" {
			d.progress.Bytes += int64(n)
		}
		if d.onProgress != nil {
			d.onProgress(d.progress)
		}
	})
	if errors.Is(err, storage.ErrRangeUnsupported) {
		return true, d.fullZIP(key, local, stage, files)
	}
	if err != nil {
		return true, err
	}
	return true, d.extractZIP(z, key, local, stage, files)
}

func (d *downloader) fullZIP(key, local, stage string, files []string) error {
	r, err := d.open(key, local+"/bundle.zip")
	if err != nil {
		return err
	}
	defer r.Close()
	f, err := os.CreateTemp(d.output, ".bundle-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	const max = int64(resultarchive.MaxExpanded) + resultarchive.MaxIndex
	n, err := io.Copy(f, io.LimitReader(r, max+1))
	if err != nil {
		return errors.New("result ZIP transfer interrupted")
	}
	if n > max {
		return errors.New("result ZIP exceeds size limit")
	}
	if d.selected["bundle"] {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := d.save(local+"/bundle.zip", key, f); err != nil {
			return err
		}
	}
	d.emit("indexing", local+"/bundle.zip", 0, -1)
	z, err := resultarchive.NewReader(f, n, nil)
	if err != nil {
		return err
	}
	return d.extractZIP(z, key, local, stage, files)
}

func (d *downloader) extractZIP(z *resultarchive.Reader, key, local, stage string, files []string) error {
	found, matched := map[string]bool{}, map[string]bool{}
	for _, file := range z.File {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			continue
		}
		choose := false
		for _, requested := range files {
			if file.Name == requested {
				found[requested], choose = true, true
			}
		}
		kind := ""
		switch {
		case strings.HasPrefix(file.Name, "logs/"+stage+"/"):
			kind = "simulatorLogs"
			if strings.Contains(filepath.Base(file.Name), "agent") {
				kind = "agentLogs"
			}
		case strings.HasPrefix(file.Name, "modules/"):
			kind = "modules"
		case strings.HasPrefix(file.Name, "scenario/config/"):
			kind = "config"
		}
		if d.selected[kind] {
			choose, matched[kind] = true, true
		}
		if !choose {
			continue
		}
		name, provenance := bundleOutput(local, stage, file.Name), key+"#"+file.Name
		d.emit("checking", name, 0, -1)
		if skip, err := d.skipExisting(name, provenance); err != nil {
			return err
		} else if skip {
			continue
		}
		d.emit("extracting", name, 0, int64(file.UncompressedSize64))
		r, err := z.OpenFile(file)
		if err != nil {
			return err
		}
		err = d.save(name, provenance, &zipFileProgress{reader: r, d: d})
		closeErr := r.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, name := range files {
		if !found[name] {
			d.requiredMissing = true
			d.report.Unavailable = append(d.report.Unavailable, key+"#"+name)
		}
	}
	for _, kind := range []string{"simulatorLogs", "agentLogs", "modules", "config"} {
		if d.selected[kind] && !matched[kind] {
			d.report.Unavailable = append(d.report.Unavailable, key+"#"+kind)
		}
	}
	return nil
}

type zipFileProgress struct {
	reader io.Reader
	d      *downloader
}

func (r *zipFileProgress) Read(p []byte) (int, error) {
	if err := r.d.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if r.d.onProgress != nil && n > 0 {
		r.d.progress.Bytes += int64(n)
		r.d.onProgress(r.d.progress)
	}
	return n, err
}
