package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"simworkflow/internal/config"
	"simworkflow/internal/connections"
)

type Store struct {
	Client *s3.Client
	Bucket string
}

// Transfer is a short-lived, single-object capability. Do not log its URL.
type Transfer struct {
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers,omitempty"`
}

func New(c config.Config) *Store {
	return &Store{Client: connections.NewS3(c.S3, connections.HTTPClient(c)), Bucket: c.S3.Bucket}
}

func (s *Store) PutJSON(ctx context.Context, key string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentType: aws.String("application/json")})
	return safeError(err)
}

func (s *Store) GetJSON(ctx context.Context, key string, value any) error {
	result, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		return safeError(err)
	}
	defer result.Body.Close()
	if err := json.NewDecoder(io.LimitReader(result.Body, 32<<20)).Decode(value); err != nil {
		return errors.New("invalid result JSON in S3")
	}
	return nil
}

// Open streams a single result object using the longer artifact transfer timeout.
// The signed capability remains internal and must never be printed.
func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	transfer, err := s.SignDownload(ctx, s.Bucket, key)
	if err != nil {
		return nil, err
	}
	response, err := Request(ctx, http.MethodGet, transfer, nil)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (s *Store) Download(ctx context.Context, source string) (Transfer, error) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "s3" || u.Host == "" || u.Path == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Transfer{}, errors.New("invalid map S3 reference")
	}
	key := strings.TrimPrefix(u.Path, "/")
	if _, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(u.Host), Key: aws.String(key)}); err != nil {
		return Transfer{}, fmt.Errorf("map object: %w", safeError(err))
	}
	return s.SignDownload(ctx, u.Host, key)
}

// SignDownload also supports objects produced by an earlier workflow step.
// It deliberately does not require the object to exist at submission time.
func (s *Store) SignDownload(ctx context.Context, bucket, key string) (Transfer, error) {
	request, err := s3.NewPresignClient(s.Client).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}, s3.WithPresignExpires(24*time.Hour))
	if err != nil {
		return Transfer{}, errors.New("could not sign map download")
	}
	return Transfer{URL: request.URL, Headers: request.SignedHeader}, nil
}

func (s *Store) Upload(ctx context.Context, key string) (Transfer, error) {
	request, err := s3.NewPresignClient(s.Client).PresignPutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)}, s3.WithPresignExpires(24*time.Hour))
	if err != nil {
		return Transfer{}, errors.New("could not sign result upload")
	}
	return Transfer{URL: request.URL, Headers: request.SignedHeader}, nil
}

var ErrNotFound = errors.New("S3 object not found")

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func safeError(err error) error {
	if err == nil {
		return nil
	}
	var response *smithyhttp.ResponseError
	if errors.As(err, &response) {
		if response.HTTPStatusCode() == http.StatusNotFound {
			return ErrNotFound
		}
		return fmt.Errorf("S3 HTTP %d", response.HTTPStatusCode())
	}
	return errors.New("S3 request failed")
}

func Request(ctx context.Context, method string, transfer Transfer, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, transfer.URL, body)
	if err != nil {
		return nil, errors.New("invalid transfer request")
	}
	if file, ok := body.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return nil, errors.New("cannot read transfer file")
		}
		position, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, errors.New("cannot determine transfer length")
		}
		req.ContentLength = info.Size() - position
	}
	for key, values := range transfer.Headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("object transfer failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("object transfer HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
