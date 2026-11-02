package reproduction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"simworkflow/internal/reproduction/testfixture"
	"simworkflow/internal/storage"
)

// Compare equal validation guarantees with fixed 10ms S3 request latency.
// This isolates avoided round trips, not production bandwidth or ZIP sizes.
func BenchmarkPinnedSnapshotReuse(b *testing.B) {
	for _, reuse := range []bool{false, true} {
		name := "original-and-snapshot"
		if reuse {
			name = "verified-snapshot"
		}
		b.Run(name, func(b *testing.B) {
			env := testfixture.New(b)
			digest := sha256.Sum256(env.Map)
			sha := hex.EncodeToString(digest[:])
			env.Objects.Set("/fixture/reproduction/maps/"+sha+".zip", env.Map)
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				time.Sleep(10 * time.Millisecond)
				env.Objects.ServeHTTP(w, r)
			}))
			defer server.Close()
			env.Config.S3.Endpoint = server.URL
			store := storage.New(env.Config)
			b.ResetTimer()
			for range b.N {
				ctx := context.Background()
				if reuse {
					if _, _, err := snapshotMap(ctx, store, "s3://fixture/original.zip", sha); err != nil {
						b.Fatal(err)
					}
				} else {
					transfer, err := store.Download(ctx, "s3://fixture/original.zip")
					if err != nil {
						b.Fatal(err)
					}
					response, err := storage.Request(ctx, http.MethodGet, transfer, nil)
					if err != nil {
						b.Fatal(err)
					}
					path, _, err := readMapArchive(ctx, response.Body, sha)
					if err != nil {
						b.Fatal(err)
					}
					_, err = saveMap(ctx, store, path, sha)
					os.Remove(path)
					if err != nil {
						b.Fatal(err)
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(requests.Load())/float64(b.N), "requests/map")
		})
	}
}
