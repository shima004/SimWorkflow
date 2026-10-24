package config

import (
	"bytes"
	_ "embed"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"go.yaml.in/yaml/v3"
)

//go:embed config.example.yaml
var exampleYAML []byte

// DefaultPath uses XDG_CONFIG_HOME when provided, otherwise the operating
// system's user configuration directory. It never depends on the working dir.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		var err error
		dir, err = os.UserConfigDir()
		if err != nil {
			return "", errors.New("cannot determine user configuration directory")
		}
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("user configuration directory must be an absolute path")
	}
	return filepath.Join(dir, "simworkflow", "config.yaml"), nil
}

// Pointer fields preserve omitted values and explicit empty/false values.
type fileConfig struct {
	Argo struct {
		Server    *string `yaml:"server"`
		Namespace *string `yaml:"namespace"`
		Token     *string `yaml:"token"`
	} `yaml:"argoWorkflow"`
	S3 struct {
		Endpoint        *string `yaml:"endpoint"`
		Region          *string `yaml:"region"`
		Bucket          *string `yaml:"bucket"`
		AccessKeyID     *string `yaml:"accessKeyID"`
		SecretAccessKey *string `yaml:"secretAccessKey"`
		SessionToken    *string `yaml:"sessionToken"`
		ForcePathStyle  *bool   `yaml:"forcePathStyle"`
	} `yaml:"s3"`
	Registry struct {
		URL      *string `yaml:"url"`
		Username *string `yaml:"username"`
		Password *string `yaml:"password"`
		Token    *string `yaml:"token"`
	} `yaml:"registry"`
	SimScope struct {
		URL *string `yaml:"url"`
	} `yaml:"simScope"`
	HTTPTimeout *string `yaml:"httpTimeout"`
}

// ReadYAML maps settings onto the existing environment schema, so validation
// and explicitly set process values retain the same semantics as before.
func ReadYAML(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, errors.New("could not read config YAML")
	}
	if len(data) > limit {
		return nil, errors.New("config YAML exceeds 1MiB")
	}
	return parseYAML(data)
}

func parseYAML(data []byte) (map[string]string, error) {
	// Do not return YAML decoder errors: they may quote credential values.
	invalid := errors.New("invalid config YAML: check field names, types, duplicate keys and document count")
	var c fileConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, invalid
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalid
	}
	values := map[string]string{}
	for _, field := range []struct {
		key   string
		value *string
	}{
		{"ARGO_WORKFLOW_SERVER", c.Argo.Server}, {"ARGO_WORKFLOW_NAMESPACE", c.Argo.Namespace}, {"ARGO_WORKFLOW_TOKEN", c.Argo.Token},
		{"S3_ENDPOINT", c.S3.Endpoint}, {"S3_REGION", c.S3.Region}, {"S3_BUCKET", c.S3.Bucket}, {"S3_ACCESS_KEY_ID", c.S3.AccessKeyID}, {"S3_SECRET_ACCESS_KEY", c.S3.SecretAccessKey}, {"S3_SESSION_TOKEN", c.S3.SessionToken},
		{"REGISTRY_URL", c.Registry.URL}, {"REGISTRY_USERNAME", c.Registry.Username}, {"REGISTRY_PASSWORD", c.Registry.Password}, {"REGISTRY_TOKEN", c.Registry.Token},
		{"SIMSCOPE_URL", c.SimScope.URL}, {"HTTP_TIMEOUT", c.HTTPTimeout},
	} {
		if field.value != nil {
			values[field.key] = *field.value
		}
	}
	if c.S3.ForcePathStyle != nil {
		values["S3_FORCE_PATH_STYLE"] = strconv.FormatBool(*c.S3.ForcePathStyle)
	}
	return values, nil
}

// Init creates a private template and never replaces an existing file.
func Init(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(exampleYAML)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(path)
		return errors.New("could not write config template")
	}
	return nil
}
