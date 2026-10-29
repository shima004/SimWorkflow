package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simworkflow/internal/config"
)

func TestMapListNonInteractiveListsNamesWithoutMetadata(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("delimiter") != "/" || r.URL.Query().Get("prefix") != "maps/" {
			t.Error("unexpected metadata lookup")
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated><CommonPrefixes><Prefix>maps/kobe/</Prefix></CommonPrefixes><CommonPrefixes><Prefix>maps/aichi/</Prefix></CommonPrefixes></ListBucketResult>`)
	}))
	defer server.Close()
	c := config.Config{S3: config.S3{Endpoint: server.URL, Bucket: "results", Region: "us-east-1", AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true}, HTTPTimeout: time.Second}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"list"}, "aichi\nkobe\n"},
		{[]string{"list", "--format", "text"}, "aichi\nkobe\n"},
		{[]string{"list", "--format", "json"}, "[\n  \"aichi\",\n  \"kobe\"\n]\n"},
	} {
		var out, errOut bytes.Buffer
		before := calls.Load()
		if err := runMap(tc.args, c, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if out.String() != tc.want || calls.Load() != before+1 || strings.Contains(out.String(), "Loading") {
			t.Fatal(out.String(), calls.Load())
		}
	}
}
