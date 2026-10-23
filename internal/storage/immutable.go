package storage

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

var ErrAlreadyExists = errors.New("S3 object already exists or has a conflicting write")

type ObjectInfo struct {
	SizeBytes int64
	Metadata  map[string]string
}

// PutImmutable requests create-only semantics using If-None-Match. Atomicity
// across clients requires the S3 backend to implement conditional PUT correctly.
func (s *Store) PutImmutable(ctx context.Context, key string, body io.ReadSeeker, contentType string, metadata map[string]string) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key), Body: body, ContentType: aws.String(contentType), Metadata: metadata, IfNoneMatch: aws.String("*")})
	var response *smithyhttp.ResponseError
	if errors.As(err, &response) && (response.HTTPStatusCode() == 412 || response.HTTPStatusCode() == 409) {
		return ErrAlreadyExists
	}
	return safeError(err)
}

func (s *Store) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		return ObjectInfo{}, safeError(err)
	}
	return ObjectInfo{SizeBytes: aws.ToInt64(out.ContentLength), Metadata: out.Metadata}, nil
}

func (s *Store) ListKeys(ctx context.Context, prefix, suffix string) ([]string, error) {
	keys := []string{}
	pages := s3.NewListObjectsV2Paginator(s.Client, &s3.ListObjectsV2Input{Bucket: aws.String(s.Bucket), Prefix: aws.String(prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, safeError(err)
		}
		for _, entry := range page.Contents {
			key := aws.ToString(entry.Key)
			if strings.HasSuffix(key, suffix) {
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}
