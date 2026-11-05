package artifacts

import "io"

// Progress reports local output paths and observed bytes, never signed URLs.
// CompletedRuns counts processed selections, including skips and missing data.
// TotalBytes is negative when Content-Length is unavailable. TransferredBytes
// counts bytes read from Source, excluding local extraction/copying.
type Progress struct {
	Phase, Path              string
	Bytes, TotalBytes        int64
	TransferredBytes         int64
	CompletedRuns, TotalRuns int
}

func (d *downloader) emit(phase, path string, n, total int64) {
	if d.onProgress == nil {
		return
	}
	d.progress.Phase, d.progress.Path = phase, path
	d.progress.Bytes, d.progress.TotalBytes = n, total
	d.onProgress(d.progress)
}

func (d *downloader) open(key, path string) (io.ReadCloser, error) {
	d.emit("downloading", path, 0, -1)
	r, err := d.source.Open(d.ctx, key)
	if err != nil || d.onProgress == nil {
		return r, err
	}
	total := int64(-1)
	if sized, ok := r.(interface{ Size() int64 }); ok {
		total = sized.Size()
	}
	d.emit("downloading", path, 0, total)
	return &progressBody{ReadCloser: r, report: func(n int) {
		d.progress.Bytes += int64(n)
		d.progress.TransferredBytes += int64(n)
		d.onProgress(d.progress)
	}}, nil
}

type progressBody struct {
	io.ReadCloser
	report func(int)
}

func (r *progressBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.report(n)
	}
	return n, err
}
