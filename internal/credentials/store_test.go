package credentials

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

type memoryBackend struct {
	items         map[string]string
	err           error
	reads, writes int
}

func (m *memoryBackend) Get(service, account string) (string, error) {
	m.reads++
	if m.err != nil {
		return "", m.err
	}
	if value, ok := m.items[service+account]; ok {
		return value, nil
	}
	return "", keyring.ErrNotFound
}
func (m *memoryBackend) Set(service, account, value string) error {
	if m.err != nil {
		return m.err
	}
	if m.items == nil {
		m.items = map[string]string{}
	}
	m.writes++
	m.items[service+account] = value
	return nil
}
func (m *memoryBackend) Delete(service, account string) error {
	if m.err != nil {
		return m.err
	}
	delete(m.items, service+account)
	return nil
}
func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}
func settings() map[string]string {
	return map[string]string{"ARGO_WORKFLOW_SERVER": "https://argo.example", "ARGO_WORKFLOW_NAMESPACE": "argo", "S3_ENDPOINT": "https://s3.example", "S3_BUCKET": "results", "REGISTRY_URL": "https://registry.example"}
}

func TestEndpointNamespaceAndBucketIsolation(t *testing.T) {
	base := settings()
	for _, tc := range []struct{ target, field, value string }{
		{"argo", "ARGO_WORKFLOW_SERVER", "https://other.example"},
		{"argo", "ARGO_WORKFLOW_NAMESPACE", "other"},
		{"s3", "S3_ENDPOINT", "http://s3.example"},
		{"s3", "S3_BUCKET", "other"},
		{"registry", "REGISTRY_URL", "https://other.example"},
	} {
		original, err := Account(tc.target, lookup(base))
		if err != nil {
			t.Fatal(err)
		}
		copy := settings()
		copy[tc.field] = tc.value
		changed, err := Account(tc.target, lookup(copy))
		if err != nil || changed == original || strings.Contains(changed, "example") {
			t.Fatal("credentials must be isolated without exposing endpoint metadata")
		}
	}
	first, _ := Account("registry", lookup(base))
	base["REGISTRY_URL"] = "https://REGISTRY.example/"
	second, _ := Account("registry", lookup(base))
	if first != second {
		t.Fatal("host case and trailing slash must be canonical")
	}
	base["REGISTRY_URL"] = "https://user:private-value@registry.example"
	if _, err := Account("registry", lookup(base)); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatal("invalid endpoints must fail safely")
	}
	base["ARGO_WORKFLOW_NAMESPACE"] = ""
	if _, err := Account("argo", lookup(base)); err == nil {
		t.Fatal("explicit empty namespace must not select another scope")
	}
}

func TestStoreValidationReplacementAndSafeErrors(t *testing.T) {
	backend := &memoryBackend{}
	store := &Store{Backend: backend}
	values := map[string]string{"S3_ACCESS_KEY_ID": "private-key", "S3_SECRET_ACCESS_KEY": "private-secret"}
	if err := store.Set("s3", "account", values); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get("s3", "account")
	if err != nil || !found || got["S3_SECRET_ACCESS_KEY"] != "private-secret" {
		t.Fatal("credentials must round-trip")
	}
	for _, invalid := range []map[string]string{
		{"S3_ACCESS_KEY_ID": "partial"},
		{"S3_ACCESS_KEY_ID": "key", "S3_SECRET_ACCESS_KEY": "secret", "OTHER": "value"},
		{"S3_ACCESS_KEY_ID": "key", "S3_SECRET_ACCESS_KEY": "private\nsecret"},
		{"S3_ACCESS_KEY_ID": "key", "S3_SECRET_ACCESS_KEY": strings.Repeat("private", 1000)},
	} {
		if err := store.Set("s3", "account", invalid); err == nil {
			t.Fatal("invalid replacements must fail")
		}
	}
	if backend.writes != 1 {
		t.Fatal("invalid replacement must preserve the existing complete credential")
	}
	backend.err = errors.New("backend revealed private-secret")
	for _, operation := range []func() error{
		func() error { return store.Set("s3", "account", values) },
		func() error { _, _, err := store.Get("s3", "account"); return err },
		func() error { return store.Delete("account") },
	} {
		if err := operation(); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("backend errors must not leak values")
		}
	}
	backend.err = nil
	backend.items[service+"account"] = `{"version":1,"values":{"private-secret":[]}}`
	if _, _, err := store.Get("s3", "account"); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("corrupt stored credentials must fail safely")
	}
	if err := store.Delete("account"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get("s3", "account"); err != nil || found {
		t.Fatal("deleted credentials must be absent")
	}
	if err := store.Delete("account"); err != nil {
		t.Fatal("delete must be idempotent")
	}
}

func TestResolveIgnoresPlaintextAndOverridesCompleteGroups(t *testing.T) {
	backend := &memoryBackend{}
	store := &Store{Backend: backend}
	file := settings()
	file["ARGO_WORKFLOW_TOKEN"] = "file-token"
	file["REGISTRY_TOKEN"] = "file-registry-token"
	file["S3_SECRET_ACCESS_KEY"] = "file-secret"
	account, _ := Account("s3", lookup(file))
	if err := store.Set("s3", account, map[string]string{"S3_ACCESS_KEY_ID": "stored-key", "S3_SECRET_ACCESS_KEY": "stored-secret", "S3_SESSION_TOKEN": "stored-session"}); err != nil {
		t.Fatal(err)
	}
	get, err := store.Resolve(lookup(file), lookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := get("ARGO_WORKFLOW_TOKEN"); v != "" {
		t.Fatal("keyring must not fall back to file credentials")
	}
	if v, _ := get("S3_SESSION_TOKEN"); v != "stored-session" {
		t.Fatal("stored temporary credentials must resolve together")
	}
	for _, target := range []string{"argo", "registry"} {
		account, _ := Account(target, lookup(file))
		key := Fields(target)[0]
		if target == "registry" {
			key = "REGISTRY_TOKEN"
		}
		if err := store.Set(target, account, map[string]string{key: "private-token"}); err != nil {
			t.Fatal(err)
		}
	}
	backend.reads = 0
	env := map[string]string{"ARGO_WORKFLOW_TOKEN": "", "S3_ACCESS_KEY_ID": "env-key", "S3_SECRET_ACCESS_KEY": "env-secret", "REGISTRY_USERNAME": "", "REGISTRY_PASSWORD": ""}
	get, err = store.Resolve(lookup(file), lookup(env))
	if err != nil || backend.reads != 0 {
		t.Fatal("explicit environment credentials must bypass keyring access")
	}
	for _, field := range []string{"ARGO_WORKFLOW_TOKEN", "S3_SESSION_TOKEN", "REGISTRY_TOKEN"} {
		if v, _ := get(field); v != "" {
			t.Fatal("must not mix credential groups or ignore explicit empty values")
		}
	}
	file["S3_BUCKET"] = "other"
	if _, err := store.Resolve(lookup(file), lookup(nil)); err == nil || !strings.Contains(err.Error(), "credentials set s3") {
		t.Fatal("another bucket must not reuse credentials")
	}
	backend.err = errors.New("private-secret")
	if _, err := store.Resolve(lookup(file), lookup(nil)); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("unavailable OS store must fail without plaintext fallback")
	}
}
