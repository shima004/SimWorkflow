package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var ErrRangeUnsupported = errors.New("S3 server does not support safe ranged downloads")

// OpenReaderAt pins all requests to the ETag observed by HEAD. It never reads
// the object in full or exposes its signed URL in an error.
func (s *Store) OpenReaderAt(ctx context.Context, key string) (io.ReaderAt, int64, error) {
	head, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		return nil, 0, safeError(err)
	}
	size := aws.ToInt64(head.ContentLength)
	if head.ContentLength == nil || size < 0 || aws.ToString(head.ETag) == "" {
		return nil, size, ErrRangeUnsupported
	}
	transfer, err := s.SignDownload(ctx, s.Bucket, key)
	if err != nil {
		return nil, 0, err
	}
	return &rangeObject{ctx: ctx, transfer: transfer, size: size, etag: *head.ETag}, size, nil
}

type rangeObject struct {
	ctx      context.Context
	transfer Transfer
	size     int64
	etag     string
}

func (r *rangeObject) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative S3 range offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if offset >= r.size {
		return 0, io.EOF
	}
	length := min(int64(len(p)), r.size-offset)
	transfer := Transfer{URL: r.transfer.URL, Headers: http.Header(r.transfer.Headers).Clone()}
	if transfer.Headers == nil {
		transfer.Headers = map[string][]string{}
	}
	transfer.Headers["Range"] = []string{fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)}
	transfer.Headers["If-Match"] = []string{r.etag}
	response, err := Request(r.ctx, http.MethodGet, transfer, nil)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		return 0, ErrRangeUnsupported
	}
	want := fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, r.size)
	if response.Header.Get("Content-Range") != want || (response.ContentLength >= 0 && response.ContentLength != length) || response.Header.Get("ETag") != r.etag {
		return 0, errors.New("S3 range response changed or has an invalid byte range")
	}
	n, err := io.ReadFull(response.Body, p[:length])
	if err == nil && int64(len(p)) > length {
		err = io.EOF
	}
	return n, err
}
