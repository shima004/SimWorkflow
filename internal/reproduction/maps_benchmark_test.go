package reproduction

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/reproduction/testfixture"
	"simworkflow/internal/storage"
)

// Model a fixed 10ms object-service latency, keeping validation, ZIP inspection
// and SHA-256 readback in both paths. This measures overlap, not a real cluster's
// throughput or a guarantee about the user's network and map sizes.
func BenchmarkSnapshotPreparation(b *testing.B) {
	for _, parallel := range []bool{false, true} {
		b.Run(fmt.Sprintf("parallel=%t", parallel), func(b *testing.B) {
			env := testfixture.New(b)
			input := env.Experiment()
			input.Spec.Conditions.Maps = nil
			for i := range 32 {
				name := fmt.Sprintf("map-%02d", i)
				env.Objects.Set("/fixture/"+name+".zip", env.Map)
				input.Spec.Conditions.Maps = append(input.Spec.Conditions.Maps, experiment.Map{Name: name, Source: "s3://fixture/" + name + ".zip"})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				timer := time.NewTimer(10 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
				env.Objects.ServeHTTP(w, r)
			}))
			defer server.Close()
			env.Config.S3.Endpoint = server.URL
			store := storage.New(env.Config)
			// Warm the immutable snapshot before measuring the repeat-submit case.
			if _, _, err := snapshotMap(context.Background(), store, input.Spec.Conditions.Maps[0].Source, ""); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
				if parallel {
					copy, err := CloneExperiment(input)
					if err != nil {
						b.Fatal(err)
					}
					if err := snapshotInputs(context.Background(), env.Config, copy); err != nil {
						b.Fatal(err)
					}
				} else {
					for _, m := range input.Spec.Conditions.Maps {
						if _, _, err := snapshotMap(context.Background(), store, m.Source, ""); err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}
