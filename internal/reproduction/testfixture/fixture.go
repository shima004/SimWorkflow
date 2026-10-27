// Package testfixture provides local registry/S3 fixtures; it never contacts real services.
package testfixture

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
)

type Objects struct {
	Mu       sync.Mutex
	Data     map[string][]byte
	Requests int
}

func (o *Objects) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.Mu.Lock()
	defer o.Mu.Unlock()
	o.Requests++
	value, exists := o.Data[r.URL.Path]
	switch r.Method {
	case "PUT":
		if exists && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(412)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		o.Data[r.URL.Path] = data
	case "GET", "HEAD":
		if !exists {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(value)))
		if r.Method == "GET" {
			w.Write(value)
		}
	default:
		w.WriteHeader(405)
	}
}
func (o *Objects) Set(path string, value []byte) {
	o.Mu.Lock()
	defer o.Mu.Unlock()
	o.Data[path] = value
}
func (o *Objects) Get(path string) []byte {
	o.Mu.Lock()
	defer o.Mu.Unlock()
	return append([]byte(nil), o.Data[path]...)
}
func (o *Objects) Delete(path string) { o.Mu.Lock(); defer o.Mu.Unlock(); delete(o.Data, path) }

type Environment struct {
	Config       config.Config
	Objects      *Objects
	S3, Registry *httptest.Server
	Helper       string
	Map          []byte
	ImageDigest  string
}

func New(t testing.TB) *Environment {
	t.Helper()
	objects := &Objects{Data: map[string][]byte{}}
	s3 := httptest.NewServer(objects)
	reg := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(s3.Close)
	t.Cleanup(reg.Close)
	c := config.Config{Registry: config.Registry{URL: reg.URL}, S3: config.S3{Endpoint: s3.URL, Region: "us-east-1", Bucket: "fixture", AccessKeyID: "fixture-access-do-not-export", SecretAccessKey: "fixture-secret-do-not-export", ForcePathStyle: true}, HTTPTimeout: 5 * time.Second}
	var layerData bytes.Buffer
	tw := tar.NewWriter(&layerData)
	tw.WriteHeader(&tar.Header{Name: "fixture.txt", Mode: 0644, Size: len64("image fixture")})
	tw.Write([]byte("image fixture"))
	tw.Close()
	layer, err := tarball.LayerFromReader(bytes.NewReader(layerData.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg.OS, cfg.Architecture = "linux", "amd64"
	cfg.Config.Labels = map[string]string{"io.simworkflow.config-bundle": "v1"}
	image, err = mutate.ConfigFile(image, cfg)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(reg.URL, "http://")
	for _, repo := range []string{"simulator", "agent", "helper"} {
		ref, err := name.NewTag(host+"/fixture/"+repo+":v1", name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, image); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var mapData bytes.Buffer
	z := zip.NewWriter(&mapData)
	for path, value := range map[string]string{"map/map.gml": "<map/>", "map/scenario.xml": "<scenario><firebrigade/><policeforce/><ambulanceteam/></scenario>", "config/common.cfg": "random.seed: 1\n", "config/kernel.cfg": "kernel.timesteps: 5\n"} {
		w, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(value))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	objects.Set("/fixture/original.zip", mapData.Bytes())
	return &Environment{Config: c, Objects: objects, S3: s3, Registry: reg, Helper: host + "/fixture/helper:v1", Map: mapData.Bytes(), ImageDigest: digest.String()}
}

func NewTarget(t testing.TB) *Environment {
	e := New(t)
	e.Registry.Close()
	e.Registry = httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(e.Registry.Close)
	e.Config.Registry.URL = e.Registry.URL
	return e
}
func len64(s string) int64 { return int64(len(s)) }
func (e *Environment) Experiment() *experiment.Experiment {
	host := strings.TrimPrefix(e.Registry.URL, "http://")
	timesteps := 5
	return &experiment.Experiment{APIVersion: experiment.APIVersion, Kind: "Experiment", Metadata: experiment.Metadata{Name: "reproduction-fixture"}, Spec: experiment.Spec{
		Simulator: experiment.Container{Image: host + "/fixture/simulator:v1", Resources: experiment.Resources{CPU: "1", Memory: "1Gi"}, Parameters: &experiment.SimulatorParameters{Timesteps: &timesteps}},
		Agents: []experiment.Agent{{Name: "python-fixture", Runtime: "python", Image: host + "/fixture/agent:v1", Resources: experiment.Resources{CPU: "0.5", Memory: "512Mi"}, Matrix: &experiment.Matrix{Groups: map[string][]experiment.ModuleSet{
			"A": {{Name: "a1", Modules: map[string]string{"Move.PathPlanning": "example.A1", "Clear.PathPlanning": "example.A1"}}, {Name: "a2", Modules: map[string]string{"Move.PathPlanning": "example.A2", "Clear.PathPlanning": "example.A2"}}},
			"B": {{Name: "b1", Modules: map[string]string{"Fire.Clustering": "example.B1"}}, {Name: "b2", Modules: map[string]string{"Fire.Clustering": "example.B2"}}},
		}}}}, Conditions: experiment.Conditions{Maps: []experiment.Map{{Name: "fixture-map", Source: "s3://fixture/original.zip"}}, Seeds: []int64{1, 2}}, Execution: experiment.Execution{MaxParallelRuns: 4, Timeout: "5m"}, Outputs: experiment.Outputs{Collect: []string{"score", "simulatorLogs", "agentLogs"}},
	}}
}
func JSON(t testing.TB, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
