// Package credentials stores endpoint-scoped authentication in the OS keyring.
package credentials

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "simworkflow.credentials.v1"

type Backend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type systemBackend struct{}

func (systemBackend) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (systemBackend) Set(service, user, value string) error    { return keyring.Set(service, user, value) }
func (systemBackend) Delete(service, user string) error        { return keyring.Delete(service, user) }

type Store struct{ Backend Backend }

func New() *Store { return &Store{Backend: systemBackend{}} }

func Targets() []string { return []string{"argo", "s3", "registry"} }

func Fields(target string) []string {
	switch target {
	case "argo":
		return []string{"ARGO_WORKFLOW_TOKEN"}
	case "s3":
		return []string{"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_SESSION_TOKEN"}
	case "registry":
		return []string{"REGISTRY_USERNAME", "REGISTRY_PASSWORD", "REGISTRY_TOKEN"}
	}
	return nil
}

// Account binds a credential to its endpoint and, where applicable, namespace
// or bucket. No URL or authentication value is put in keyring metadata.
func Account(target string, lookup func(string) (string, bool)) (string, error) {
	var endpoint, scope string
	switch target {
	case "argo":
		endpoint, _ = lookup("ARGO_WORKFLOW_SERVER")
		var configured bool
		scope, configured = lookup("ARGO_WORKFLOW_NAMESPACE")
		if !configured {
			scope = "argo"
		}
		if strings.TrimSpace(scope) == "" {
			return "", errors.New("ARGO_WORKFLOW_NAMESPACE must not be empty")
		}
	case "s3":
		endpoint, _ = lookup("S3_ENDPOINT")
		scope, _ = lookup("S3_BUCKET")
		if strings.TrimSpace(scope) == "" {
			return "", errors.New("S3_BUCKET is required for credentials")
		}
	case "registry":
		endpoint, _ = lookup("REGISTRY_URL")
	default:
		return "", errors.New("credential target must be argo, s3 or registry")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("configure a valid %s endpoint before managing credentials", target)
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	data, _ := json.Marshal([]string{u.String(), scope})
	return fmt.Sprintf("%s:%x", target, sha256.Sum256(data)), nil
}

type record struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

func validate(target string, values map[string]string) error {
	fields := Fields(target)
	if fields == nil {
		return errors.New("credential target must be argo, s3 or registry")
	}
	allowed := map[string]bool{}
	for _, field := range fields {
		allowed[field] = true
	}
	for key, value := range values {
		if !allowed[key] || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid credential fields")
		}
	}
	nonempty := func(key string) bool { return strings.TrimSpace(values[key]) != "" }
	switch target {
	case "argo":
		if !nonempty("ARGO_WORKFLOW_TOKEN") {
			return errors.New("Argo token is required; delete stored credentials for anonymous access")
		}
	case "s3":
		if !nonempty("S3_ACCESS_KEY_ID") || !nonempty("S3_SECRET_ACCESS_KEY") {
			return errors.New("S3 access key ID and secret access key are required")
		}
	case "registry":
		if nonempty("REGISTRY_TOKEN") {
			if values["REGISTRY_USERNAME"] != "" || values["REGISTRY_PASSWORD"] != "" {
				return errors.New("registry token cannot be combined with username/password")
			}
		} else if !nonempty("REGISTRY_USERNAME") || !nonempty("REGISTRY_PASSWORD") {
			return errors.New("registry username and password are required; delete stored credentials for anonymous access")
		}
	}
	return nil
}

func backendError() error {
	return errors.New("OS credential store unavailable or locked; unlock/configure the OS keyring, or use CREDENTIALS_STORE=environment with credentials supplied by your environment")
}

func (s *Store) Set(target, account string, values map[string]string) error {
	if err := validate(target, values); err != nil {
		return err
	}
	data, _ := json.Marshal(record{Version: 1, Values: values})
	// Fit Windows' credential blob limit and macOS security's interactive input.
	if len(data) > 2400 {
		return errors.New("credentials exceed the cross-platform OS store limit of 2400 encoded bytes")
	}
	if err := s.Backend.Set(service, account, string(data)); err != nil {
		return backendError()
	}
	return nil
}

func (s *Store) Get(target, account string) (map[string]string, bool, error) {
	data, err := s.Backend.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, backendError()
	}
	var item record
	if len(data) > 2400 || json.Unmarshal([]byte(data), &item) != nil || item.Version != 1 || validate(target, item.Values) != nil {
		return nil, false, errors.New("stored credentials are invalid; register them again")
	}
	return item.Values, true, nil
}

func (s *Store) Delete(account string) error {
	err := s.Backend.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return backendError()
	}
	return nil
}

// Resolve replaces all authentication fields as a group. In keyring mode,
// authentication never falls through to a plaintext .env or YAML file.
func (s *Store) Resolve(settings, environment func(string) (string, bool)) (func(string) (string, bool), error) {
	return s.ResolveTargets(settings, environment, Targets())
}

// ResolveTargets avoids unlocking unrelated credentials for Argo-only commands.
func (s *Store) ResolveTargets(settings, environment func(string) (string, bool), targets []string) (func(string) (string, bool), error) {
	values := map[string]string{}
	for _, target := range targets {
		fields := Fields(target)
		if fields == nil {
			return nil, errors.New("invalid credential target")
		}
		useEnvironment := false
		for _, field := range fields {
			if _, ok := environment(field); ok {
				useEnvironment = true
			}
		}
		if useEnvironment {
			for _, field := range fields {
				values[field], _ = environment(field)
			}
			continue
		}
		account, err := Account(target, settings)
		if err != nil {
			return nil, err
		}
		stored, found, err := s.Get(target, account)
		if err != nil {
			return nil, err
		}
		if !found && target == "s3" {
			return nil, errors.New("S3 credentials not registered for this endpoint/bucket; run simworkflow credentials set s3 or supply S3 credentials through environment variables")
		}
		for _, field := range fields {
			values[field] = stored[field]
		}
	}
	return func(key string) (string, bool) {
		if value, ok := values[key]; ok {
			return value, true
		}
		return settings(key)
	}, nil
}
