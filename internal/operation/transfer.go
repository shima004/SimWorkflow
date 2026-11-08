package operation

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
)

var transferID atomic.Uint64

// Reader reports bytes consumed without buffering or making extra requests.
// Stage must be a safe display label, never an object key or signed URL.
func Reader(ctx context.Context, stage string, total int64, reader io.Reader) io.Reader {
	if !HasReporter(ctx) {
		return reader
	}
	r := &transferReader{ctx: ctx, reader: reader, event: Event{Stage: stage, TransferID: transferID.Add(1), TotalBytes: total}}
	Report(ctx, r.event)
	return r
}

// Writer is the counterpart of Reader for subprocess output streams.
func Writer(ctx context.Context, stage string, total int64, writer io.Writer) io.Writer {
	if !HasReporter(ctx) {
		return writer
	}
	w := &transferWriter{ctx: ctx, writer: writer, event: Event{Stage: stage, TransferID: transferID.Add(1), TotalBytes: total}}
	Report(ctx, w.event)
	return w
}

type transferWriter struct {
	ctx    context.Context
	writer io.Writer
	event  Event
}

func (w *transferWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(p)
	if n > 0 {
		w.event.Bytes += int64(n)
		Report(w.ctx, w.event)
	}
	return n, err
}

type transferReader struct {
	ctx    context.Context
	reader io.Reader
	event  Event
}

func (r *transferReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		r.event.Bytes += int64(n)
		Report(r.ctx, r.event)
	}
	return n, err
}

type transferBody struct {
	io.Reader
	io.Closer
}

// Body leaves small transfers quiet; callers can show their overall item count.
func Body(ctx context.Context, stage string, body io.ReadCloser, size int64) io.ReadCloser {
	if body == nil || !HasReporter(ctx) || size >= 0 && size < 1<<20 {
		return body
	}
	return &transferBody{Reader: Reader(ctx, stage, size, body), Closer: body}
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Client measures HTTP body reads, after SDK checksum/signature preparation.
// It preserves the original client's redirects, timeouts and retry behavior.
type Client struct{ Base HTTPClient }

func (c Client) Do(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	if req.Method == http.MethodPut || req.Method == http.MethodPatch || req.Method == http.MethodPost {
		copy.Body = Body(req.Context(), "Upload S3 object", req.Body, req.ContentLength)
	}
	response, err := c.Base.Do(copy)
	if err == nil && req.Method == http.MethodGet && response.StatusCode >= 200 && response.StatusCode < 300 {
		response.Body = Body(req.Context(), "Download S3 object", response.Body, response.ContentLength)
	}
	return response, err
}
