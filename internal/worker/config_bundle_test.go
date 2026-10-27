package worker

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

func bundleConfigs(stage string) []Config {
	configs := []Config{}
	for _, id := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
		c := Config{Stage: stage, ExecutionID: "execution", Run: experiment.Run{ID: "run-" + id}, TimeoutSeconds: 300}
		if stage == Precompute {
			c.PrecomputeUpload = &storage.Transfer{URL: "https://s3.example/precompute-only"}
		} else {
			c.PrecomputeDownload = &storage.Transfer{URL: "https://s3.example/simulation-only"}
		}
		configs = append(configs, c)
	}
	return configs
}

func TestConfigurationBundlesKeepStagesAndRunsSeparate(t *testing.T) {
	for _, stage := range []string{Precompute, Simulation} {
		configs := bundleConfigs(stage)
		path, sha, err := CreateConfigBundle(context.Background(), "execution", stage, configs)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(path)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
		for _, want := range configs {
			source := RunConfigSource{BundleURL: server.URL, BundleSHA256: sha, Stage: stage, ExecutionID: "execution", RunID: want.Run.ID}
			got, err := ReadRunConfigSource(context.Background(), source)
			if err != nil || !validBundleConfig(got, "execution", stage, want.Run.ID) {
				t.Fatal("bundle configuration did not round trip", err)
			}
			for _, change := range []func(*RunConfigSource){
				func(s *RunConfigSource) { s.Stage = "other" },
				func(s *RunConfigSource) {
					if stage == Precompute {
						s.Stage = Simulation
					} else {
						s.Stage = Precompute
					}
				},
				func(s *RunConfigSource) { s.ExecutionID = "other" },
				func(s *RunConfigSource) { s.RunID = "run-" + strings.Repeat("c", 32) },
				func(s *RunConfigSource) { s.BundleSHA256 = strings.Repeat("0", 64) },
				func(s *RunConfigSource) { s.Inline = "private inline" },
				func(s *RunConfigSource) { s.Remote = "https://s3.example/private" },
			} {
				changed := source
				change(&changed)
				if _, err := ReadRunConfigSource(context.Background(), changed); err == nil || strings.Contains(err.Error(), "private") {
					t.Fatal("invalid bundle selector accepted or exposed inputs", err)
				}
			}
		}
		server.Close()
		z, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range z.File {
			if entry.Name == "manifest.json" {
				continue
			}
			r, _ := entry.Open()
			data, _ := io.ReadAll(r)
			r.Close()
			other := "simulation-only"
			if stage == Simulation {
				other = "precompute-only"
			}
			if bytes.Contains(data, []byte(other)) {
				t.Fatal("stage bundle contains another stage's capability")
			}
		}
		z.Close()
	}
}

func TestConfigBundleRejectsUnsafeArchives(t *testing.T) {
	configs := bundleConfigs(Precompute)
	for _, kind := range []string{"traversal", "duplicate", "symlink", "oversized", "wrong-run", "wrong-stage", "wrong-permission", "wrong-manifest", "malformed-json", "corrupt-zip"} {
		t.Run(kind, func(t *testing.T) {
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			manifest := configBundleManifest{Schema: "simworkflow/config-bundle/v1", ExecutionID: "execution", Stage: Precompute, Runs: 1}
			if kind == "wrong-manifest" {
				manifest.Runs = 2
			}
			r, _ := z.Create("manifest.json")
			json.NewEncoder(r).Encode(manifest)
			c := configs[0]
			if kind == "wrong-run" {
				c.Run.ID = configs[1].Run.ID
			}
			if kind == "wrong-stage" {
				c.Stage = Simulation
			}
			if kind == "wrong-permission" {
				c.PrecomputeDownload = &storage.Transfer{URL: "private"}
			}
			r, _ = z.Create(configs[0].Run.ID + ".json")
			if kind == "malformed-json" {
				io.WriteString(r, "private invalid json")
			} else {
				json.NewEncoder(r).Encode(c)
			}
			switch kind {
			case "traversal":
				r, _ = z.Create("../private")
				io.WriteString(r, "private")
			case "duplicate":
				r, _ = z.Create(configs[0].Run.ID + ".json")
				io.WriteString(r, "{}")
			case "symlink":
				h := &zip.FileHeader{Name: "run-" + strings.Repeat("d", 32) + ".json"}
				h.SetMode(os.ModeSymlink | 0600)
				r, _ = z.CreateHeader(h)
				io.WriteString(r, "private")
			case "oversized":
				r, _ = z.Create("run-" + strings.Repeat("d", 32) + ".json")
				io.WriteString(r, strings.Repeat(" ", maxRunConfigBytes+1))
			}
			z.Close()
			data := b.Bytes()
			if kind == "corrupt-zip" {
				data = []byte("private invalid zip")
			}
			sha := sha256.Sum256(data)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
			defer server.Close()
			_, err := ReadRunConfigSource(context.Background(), RunConfigSource{BundleURL: server.URL, BundleSHA256: hex.EncodeToString(sha[:]), Stage: Precompute, ExecutionID: "execution", RunID: configs[0].Run.ID})
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe archive accepted or error leaked inputs", err)
			}
		})
	}
	for _, change := range []func([]Config){
		func(c []Config) { c[1] = c[0] },
		func(c []Config) { c[0].Stage = Simulation },
		func(c []Config) { c[0].ExecutionID = "other" },
		func(c []Config) { c[0].PrecomputeDownload = &storage.Transfer{} },
	} {
		c := bundleConfigs(Precompute)
		change(c)
		path, _, err := CreateConfigBundle(context.Background(), "execution", Precompute, c)
		if err == nil {
			os.Remove(path)
			t.Fatal("unsafe bundle created")
		}
		if _, e := os.Stat(path); path != "" && !os.IsNotExist(e) {
			t.Fatal("failed bundle left temporary data")
		}
	}
}
