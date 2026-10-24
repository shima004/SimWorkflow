// Package config loads connection settings without making network requests.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Argo struct {
	Server    string
	Namespace string
	Token     string
}

type S3 struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	ForcePathStyle  bool
}

type Registry struct {
	URL      string
	Username string
	Password string
	Token    string
}

type Config struct {
	SimScopeURL string
	Argo        Argo
	S3          S3
	Registry    Registry
	HTTPTimeout time.Duration
}

// Load takes an environment lookup so callers can layer .env values underneath
// process environment values without mutating the process environment.
func Load(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, fallback string) string {
		if value, ok := lookup(key); ok {
			return value
		}
		return fallback
	}
	c := Config{
		SimScopeURL: get("SIMSCOPE_URL", ""),
		Argo:        Argo{Server: get("ARGO_WORKFLOW_SERVER", ""), Namespace: get("ARGO_WORKFLOW_NAMESPACE", "argo"), Token: get("ARGO_WORKFLOW_TOKEN", "")},
		S3: S3{
			Endpoint: get("S3_ENDPOINT", ""), Region: get("S3_REGION", "us-east-1"),
			Bucket: get("S3_BUCKET", ""), AccessKeyID: get("S3_ACCESS_KEY_ID", ""),
			SecretAccessKey: get("S3_SECRET_ACCESS_KEY", ""), SessionToken: get("S3_SESSION_TOKEN", ""),
		},
		Registry: Registry{URL: get("REGISTRY_URL", ""), Username: get("REGISTRY_USERNAME", ""), Password: get("REGISTRY_PASSWORD", ""), Token: get("REGISTRY_TOKEN", "")},
	}
	for _, entry := range []struct{ key, value string }{
		{"ARGO_WORKFLOW_SERVER", c.Argo.Server}, {"S3_ENDPOINT", c.S3.Endpoint}, {"REGISTRY_URL", c.Registry.URL}, {"SIMSCOPE_URL", c.SimScopeURL},
	} {
		if entry.key == "SIMSCOPE_URL" && entry.value == "" {
			continue
		}
		u, err := url.Parse(entry.value)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, fmt.Errorf("%s must be an http(s) URL without credentials, query, or fragment", entry.key)
		}
	}
	for _, entry := range []struct{ key, value string }{
		{"ARGO_WORKFLOW_NAMESPACE", c.Argo.Namespace}, {"S3_REGION", c.S3.Region}, {"S3_BUCKET", c.S3.Bucket},
		{"S3_ACCESS_KEY_ID", c.S3.AccessKeyID}, {"S3_SECRET_ACCESS_KEY", c.S3.SecretAccessKey},
	} {
		if strings.TrimSpace(entry.value) == "" {
			return Config{}, fmt.Errorf("%s is required", entry.key)
		}
	}
	if (c.Registry.Username == "") != (c.Registry.Password == "") {
		return Config{}, fmt.Errorf("REGISTRY_USERNAME and REGISTRY_PASSWORD must be set together")
	}
	if c.Registry.Token != "" && c.Registry.Username != "" {
		return Config{}, fmt.Errorf("REGISTRY_TOKEN cannot be combined with REGISTRY_USERNAME/REGISTRY_PASSWORD")
	}
	var err error
	c.S3.ForcePathStyle, err = strconv.ParseBool(get("S3_FORCE_PATH_STYLE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("S3_FORCE_PATH_STYLE must be a boolean")
	}
	c.HTTPTimeout, err = time.ParseDuration(get("HTTP_TIMEOUT", "30s"))
	if err != nil || c.HTTPTimeout <= 0 {
		return Config{}, fmt.Errorf("HTTP_TIMEOUT must be a positive duration such as 30s")
	}
	return c, nil
}
