// Package connections checks service access without changing remote resources.
package connections

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"simworkflow/internal/config"
)

type Result struct {
	Service string
	Detail  string
	Err     error
}

// Check tests workflow-list access, bucket access, and the Registry V2 API.
// No tokens, endpoint URLs, credentials, or response bodies enter its errors.
func Check(ctx context.Context, c config.Config) []Result {
	client := HTTPClient(c)
	results := []Result{
		{Service: "Argo Workflow", Detail: "workflow list access"},
		{Service: "S3", Detail: "bucket access (HeadBucket)"},
		{Service: "Registry", Detail: "Registry V2 API access"},
	}
	checks := []func() error{
		func() error { return checkArgo(ctx, client, c.Argo) },
		func() error { return checkS3(ctx, NewS3(c.S3, client), c.S3.Bucket) },
		func() error { return checkRegistry(ctx, client, c.Registry) },
	}
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].Err = check()
		}()
	}
	wg.Wait()
	return results
}

func HTTPClient(c config.Config) *http.Client {
	return &http.Client{
		Timeout: c.HTTPTimeout,
		// A redirect could disclose Authorization to another endpoint. Report
		// its status so users can configure the intended API URL directly.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func NewS3(c config.S3, client *http.Client) *s3.Client {
	return s3.New(s3.Options{
		Region: c.Region, BaseEndpoint: aws.String(c.Endpoint), UsePathStyle: c.ForcePathStyle,
		Credentials: credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, c.SessionToken),
		HTTPClient:  client, RetryMaxAttempts: 1,
	})
}

func checkArgo(ctx context.Context, client *http.Client, c config.Argo) error {
	u := strings.TrimRight(c.Server, "/") + "/api/v1/workflows/" + url.PathEscape(c.Namespace) + "?listOptions.limit=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errors.New("invalid API URL")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", bearer(c.Token))
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return safeNetworkError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode)
	}
	// A proxy or login page can return HTTP 200. Check the API response shape.
	var body map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return errors.New("invalid workflow-list JSON response")
	}
	if _, items := body["items"]; !items {
		if _, metadata := body["metadata"]; !metadata {
			return errors.New("response is not a workflow list")
		}
	}
	return nil
}

func checkS3(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return nil
	}
	var responseError *smithyhttp.ResponseError
	if errors.As(err, &responseError) {
		return statusError(responseError.HTTPStatusCode())
	}
	return safeNetworkError(err)
}

var challengeParameter = regexp.MustCompile(`(?i)([a-z]+)="([^"]*)"`)

func checkRegistry(ctx context.Context, client *http.Client, c config.Registry) error {
	u := strings.TrimRight(c.URL, "/") + "/v2/"
	request := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, errors.New("invalid registry URL")
		}
		if token != "" {
			req.Header.Set("Authorization", bearer(token))
		} else if c.Username != "" {
			req.SetBasicAuth(c.Username, c.Password)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, safeNetworkError(err)
		}
		return resp, nil
	}
	resp, err := request(c.Token)
	if err != nil {
		return err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	status := resp.StatusCode
	resp.Body.Close()
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusUnauthorized || !strings.HasPrefix(strings.ToLower(challenge), "bearer ") || c.Token != "" {
		return statusError(status)
	}
	params := map[string]string{}
	for _, match := range challengeParameter.FindAllStringSubmatch(challenge, -1) {
		params[strings.ToLower(match[1])] = match[2]
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" || realm.User != nil || realm.Fragment != "" {
		return errors.New("invalid registry token endpoint")
	}
	registryURL, _ := url.Parse(c.URL)
	sameOrigin := realm.Scheme == registryURL.Scheme && realm.Host == registryURL.Host
	if realm.Scheme != "https" && !(sameOrigin && realm.Scheme == "http") {
		return errors.New("registry token endpoint requires HTTPS")
	}
	if c.Username != "" && !sameOrigin {
		return errors.New("registry token endpoint must share the configured origin to receive credentials")
	}
	query := realm.Query()
	for _, name := range []string{"service", "scope"} {
		if params[name] != "" {
			query.Set(name, params[name])
		}
	}
	realm.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return errors.New("invalid registry token request")
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	tokenResponse, err := client.Do(req)
	if err != nil {
		return safeNetworkError(err)
	}
	defer tokenResponse.Body.Close()
	if tokenResponse.StatusCode != http.StatusOK {
		return statusError(tokenResponse.StatusCode)
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(tokenResponse.Body, 1<<20)).Decode(&token); err != nil {
		return errors.New("invalid registry token response")
	}
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	if token.Token == "" {
		return errors.New("registry token response contains no token")
	}
	resp, err = request(token.Token)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode)
	}
	return nil
}

func bearer(token string) string {
	token = strings.TrimSpace(token)
	if len(token) >= 7 && strings.EqualFold(token[:7], "Bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	return "Bearer " + token
}

func statusError(status int) error {
	return fmt.Errorf("HTTP %d (%s)", status, http.StatusText(status))
}

func safeNetworkError(err error) error {
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var unknownCA x509.UnknownAuthorityError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("request timed out")
	case errors.Is(err, context.Canceled):
		return errors.New("request canceled")
	case errors.As(err, &dns):
		return errors.New("DNS lookup failed")
	case errors.As(err, &cert), errors.As(err, &unknownCA):
		return errors.New("TLS certificate verification failed")
	default:
		return errors.New("connection failed")
	}
}
