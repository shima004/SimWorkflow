package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/term"
	"github.com/zalando/go-keyring"

	"simworkflow/internal/credentials"
)

type credentialBackend struct {
	items         map[string]string
	err           error
	reads, writes int
}

func (m *credentialBackend) Get(service, account string) (string, error) {
	m.reads++
	if m.err != nil {
		return "", m.err
	}
	if value, ok := m.items[service+account]; ok {
		return value, nil
	}
	return "", keyring.ErrNotFound
}
func (m *credentialBackend) Set(service, account, value string) error {
	if m.err != nil {
		return m.err
	}
	if m.items == nil {
		m.items = map[string]string{}
	}
	m.items[service+account] = value
	m.writes++
	return nil
}
func (m *credentialBackend) Delete(service, account string) error {
	if m.err != nil {
		return m.err
	}
	delete(m.items, service+account)
	return nil
}

const keyringConfig = `credentials:
  store: keyring
argoWorkflow:
  server: https://argo.example.com
s3:
  endpoint: https://s3.example.com
  bucket: results
registry:
  url: https://registry.example.com
`

func TestCredentialLifecycleAndPlaintextIsolation(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(keyringConfig), 0600); err != nil {
		t.Fatal(err)
	}
	dotenv := "S3_ACCESS_KEY_ID=file-key\nS3_SECRET_ACCESS_KEY=file-secret\nREGISTRY_TOKEN=file-token\n"
	if err := os.WriteFile(".env", []byte(dotenv), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &credentialBackend{}
	store := &credentials.Store{Backend: backend}
	answers := []string{"private-key", "private-secret", ""}
	prompt := func(label string) (string, error) {
		if len(answers) == 0 {
			t.Fatal("unexpected prompt")
		}
		value := answers[0]
		answers = answers[1:]
		return value, nil
	}
	get := func(string) (string, bool) { return "", false }
	var out, errOut bytes.Buffer
	execute := func(args ...string) error {
		out.Reset()
		errOut.Reset()
		err := runWithCredentials(append([]string{"--config", path}, args...), get, &out, &errOut, store, prompt)
		if strings.Contains(out.String()+errOut.String(), "private-") || (err != nil && strings.Contains(err.Error(), "private-")) {
			t.Fatal("credential values must never reach output/errors")
		}
		return err
	}
	if err := execute("credentials", "set", "s3"); err != nil {
		t.Fatal(err)
	}
	if backend.writes != 1 || len(answers) != 0 {
		t.Fatal("S3 registration must save one complete record")
	}
	if err := execute("credentials", "status"); err != nil || !strings.Contains(out.String(), "s3: registered") || !strings.Contains(out.String(), "registry: not registered") {
		t.Fatal("status must show only registration state")
	}
	if err := execute("config", "check"); err != nil || !strings.Contains(out.String(), "Registry authentication: anonymous") {
		t.Fatal("stored credentials must resolve without falling back to plaintext files")
	}
	answers = []string{"private-token"}
	if err := execute("credentials", "set", "registry", "--auth", "token"); err != nil {
		t.Fatal(err)
	}
	if err := execute("config", "check"); err != nil || !strings.Contains(out.String(), "Registry authentication: bearer token") {
		t.Fatal("registry token must resolve from keyring")
	}
	if err := execute("credentials", "delete", "s3"); err != nil {
		t.Fatal(err)
	}
	if err := execute("config", "check"); err == nil || !strings.Contains(err.Error(), "credentials set s3") {
		t.Fatal("deleted credentials must not fall back to .env")
	}
	file, _ := os.ReadFile(path)
	env, _ := os.ReadFile(".env")
	if string(file) != keyringConfig || string(env) != dotenv {
		t.Fatal("credential commands must never write plaintext configuration")
	}
}

func TestCredentialsFailedInputAndBackendPreserveExistingRecord(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(keyringConfig), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &credentialBackend{}
	store := &credentials.Store{Backend: backend}
	get := func(string) (string, bool) { return "", false }
	for _, tc := range []struct {
		args   []string
		prompt func(string) (string, error)
	}{
		{[]string{"credentials", "set", "s3"}, func(string) (string, error) { return "", errors.New("private-secret") }},
		{[]string{"credentials", "set", "argo"}, func(string) (string, error) { return "", nil }},
		{[]string{"credentials", "set", "s3", "private-secret"}, nil},
		{[]string{"credentials", "set", "private-secret"}, nil},
		{[]string{"credentials", "set", "argo", "--auth", "token"}, nil},
	} {
		var out, errOut bytes.Buffer
		err := runWithCredentials(append([]string{"--config", path}, tc.args...), get, &out, &errOut, store, tc.prompt)
		if err == nil || strings.Contains(err.Error()+out.String()+errOut.String(), "private-secret") || backend.writes != 0 {
			t.Fatal("invalid or cancelled input must not be saved or exposed")
		}
	}
	backend.err = errors.New("private-secret")
	var out, errOut bytes.Buffer
	err := runWithCredentials([]string{"--config", path, "credentials", "status"}, get, &out, &errOut, store, nil)
	if err == nil || strings.Contains(err.Error()+out.String()+errOut.String(), "private-secret") {
		t.Fatal("backend errors must be sanitized")
	}
}

func TestKeyringModeEnvironmentBypassesUnavailableStoreAndOfflineCommands(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(keyringConfig), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &credentialBackend{err: errors.New("must not access OS store")}
	env := map[string]string{"ARGO_WORKFLOW_TOKEN": "", "S3_ACCESS_KEY_ID": "private-key", "S3_SECRET_ACCESS_KEY": "private-secret", "REGISTRY_TOKEN": ""}
	get := func(key string) (string, bool) { value, ok := env[key]; return value, ok }
	var out, errOut bytes.Buffer
	if err := runWithCredentials([]string{"--config", path, "config", "check"}, get, &out, &errOut, &credentials.Store{Backend: backend}, nil); err != nil || backend.reads != 0 {
		t.Fatal("environment credentials must work without an OS store")
	}
	delete(env, "S3_SECRET_ACCESS_KEY")
	if err := runWithCredentials([]string{"--config", path, "config", "check"}, get, &out, &errOut, &credentials.Store{Backend: backend}, nil); err == nil || backend.reads != 0 {
		t.Fatal("partial environment credentials must not be mixed with saved credentials")
	}
	if err := runWithCredentials([]string{"--config", path, "--version"}, get, &out, &errOut, &credentials.Store{Backend: backend}, nil); err != nil || backend.reads != 0 {
		t.Fatal("offline version must not access OS store")
	}
	env["CREDENTIALS_STORE"] = "environment"
	env["S3_SECRET_ACCESS_KEY"] = "private-secret"
	delete(env, "ARGO_WORKFLOW_TOKEN")
	delete(env, "REGISTRY_TOKEN")
	if err := runWithCredentials([]string{"--config", path, "config", "check"}, get, &out, &errOut, &credentials.Store{Backend: backend}, nil); err != nil || backend.reads != 0 {
		t.Fatal("CI mode must disable keyring even when optional auth variables are omitted")
	}
}

func TestHiddenInputRefusesNonTerminal(t *testing.T) {
	if term.IsTerminal(os.Stdin.Fd()) {
		t.Skip("requires non-interactive stdin")
	}
	var out bytes.Buffer
	if _, err := readCredential("Token", &out); err == nil || out.Len() != 0 {
		t.Fatal("secret input must not fall back to an echoed or redirected stream")
	}
}
