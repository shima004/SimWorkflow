package credentials

import (
	"crypto/rand"
	"os"
	"testing"
)

// Opt in only: the ordinary suite must never touch a developer's real keyring.
func TestSystemStoreRoundTrip(t *testing.T) {
	if os.Getenv("SIMWORKFLOW_TEST_OS_KEYRING") != "1" {
		t.Skip("set SIMWORKFLOW_TEST_OS_KEYRING=1 to test an isolated temporary OS credential")
	}
	store := New()
	account := "validation:" + rand.Text()
	values := map[string]string{"S3_ACCESS_KEY_ID": "simworkflow-validation-key", "S3_SECRET_ACCESS_KEY": "simworkflow-validation-secret"}
	if err := store.Set("s3", account, values); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Delete(account); err != nil {
			t.Error(err)
		}
	})
	got, found, err := store.Get("s3", account)
	if err != nil || !found || got["S3_ACCESS_KEY_ID"] != values["S3_ACCESS_KEY_ID"] || got["S3_SECRET_ACCESS_KEY"] != values["S3_SECRET_ACCESS_KEY"] {
		t.Fatal("OS credential round-trip failed")
	}
	if err := store.Delete(account); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get("s3", account); err != nil || found {
		t.Fatal("temporary OS credential deletion failed")
	}
}
