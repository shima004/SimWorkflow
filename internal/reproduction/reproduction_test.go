package reproduction_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/layout"
	"simworkflow/internal/experiment"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/reproduction/testfixture"
	"simworkflow/internal/worker"
)

const prefix = "experiments/0123456789abcdef0123456789abcdef"

func TestSignedTransferDurationBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		runs      int
		timeout   string
		wantError bool
	}{
		{"beyond previous one-day budget", 24, "54m", false},
		{"one hour before seven-day expiry", 167, "54m", false},
		{"no safety margin", 168, "54m", true},
		{"single run over twenty hours", 1, "48h", false},
		{"single run at total budget", 1, "166h54m", false},
		{"single run exceeds total budget", 1, "166h54m1s", true},
		{"extreme timeout cannot overflow budget check", 1, "2562047h47m16s", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := experiment.ReadFile("../../examples/experiments/python-module-comparison.yaml")
			if err != nil {
				t.Fatal(err)
			}
			e.Spec.Agents = e.Spec.Agents[:1]
			e.Spec.Agents[0].Matrix = nil
			e.Spec.Conditions.Maps = e.Spec.Conditions.Maps[:1]
			e.Spec.Conditions.Seeds = make([]int64, tc.runs)
			for i := range e.Spec.Conditions.Seeds {
				e.Spec.Conditions.Seeds[i] = int64(i + 1)
			}
			e.Spec.Execution.MaxParallelRuns = 1
			e.Spec.Execution.Timeout = tc.timeout
			plan, err := experiment.BuildPlan(e)
			if err != nil {
				t.Fatal(err)
			}
			err = reproduction.ValidatePlan(plan)
			if (err != nil) != tc.wantError {
				t.Fatalf("duration validation error = %v, want error %v", err, tc.wantError)
			}
			if err != nil && !strings.Contains(err.Error(), "signed transfer validity") {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestPlansAboveTenThousandHaveNoFixedCountLimit(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/python-module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e.Spec.Agents = e.Spec.Agents[:1]
	e.Spec.Agents[0].Matrix = nil
	e.Spec.Conditions.Maps = e.Spec.Conditions.Maps[:1]
	e.Spec.Conditions.Seeds = make([]int64, 10001)
	for i := range e.Spec.Conditions.Seeds {
		e.Spec.Conditions.Seeds[i] = int64(i + 1)
	}
	// Keep the independently enforced signed-transfer duration within bounds.
	e.Spec.Execution.MaxParallelRuns = 10001
	e.Spec.Execution.Timeout = "1m"
	plan, err := experiment.BuildPlan(e)
	if err != nil || plan.TotalRuns != 10001 {
		t.Fatal("planning retained a fixed count limit", err)
	}
	if err := reproduction.ValidatePlan(plan); err != nil {
		t.Fatal("submission/export/reproduction retained a fixed count limit", err)
	}
}

func tarBundle(t *testing.T) []byte {
	t.Helper()
	var result bytes.Buffer
	g := gzip.NewWriter(&result)
	tw := tar.NewWriter(g)
	for path, value := range map[string]string{"modules/fireBrigade/module.yaml": "Move.PathPlanning: example.A1\n", "scenario/config/common.cfg": "random.seed: 1\n", "logs/simulation/kernel.log": "LOG_SENTINEL", "run.json": "SIGNED_URL_DO_NOT_EXPORT", "state/private.json": "STATE_DO_NOT_EXPORT"} {
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0600, Size: int64(len(value))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(value))
	}
	tw.Close()
	g.Close()
	return result.Bytes()
}
func exported(t *testing.T, logs bool) (*testfixture.Environment, *experiment.Plan, reproduction.Metadata, string) {
	t.Helper()
	env := testfixture.New(t)
	input := env.Experiment()
	locked, meta, err := reproduction.Capture(context.Background(), env.Config, input, env.Helper, "v0.1.3", "fixture-commit")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input.Spec.Simulator.Image, "@sha256:") {
		t.Fatal("capture modified caller's definition")
	}
	if !strings.Contains(locked.Spec.Simulator.Image, "@sha256:") || !strings.Contains(locked.Spec.Conditions.Maps[0].Source, "/reproduction/maps/") {
		t.Fatal("inputs were not pinned and snapshotted")
	}
	plan, err := experiment.BuildPlan(locked)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalRuns != 8 {
		t.Fatal("grouped module combinations were lost")
	}
	meta.PlanSHA256 = reproduction.PlanSHA256(plan)
	for _, run := range plan.Runs {
		score := 12.5
		for _, stage := range []string{worker.Precompute, worker.Simulation} {
			key := "/fixture/" + prefix + "/" + run.ID
			if stage == worker.Precompute {
				key += "/precompute"
			}
			oldRun := run
			oldRun.Configuration.Groups = nil
			env.Objects.Set(key+"/summary.json", testfixture.JSON(t, worker.Summary{Run: oldRun, Stage: stage, ExecutionID: "fixture-execution", Phase: "Succeeded", Score: &score}))
			env.Objects.Set(key+"/bundle.tar.gz", tarBundle(t))
		}
		var handoff bytes.Buffer
		w := zip.NewWriter(&handoff)
		w.Close()
		env.Objects.Set("/fixture/"+prefix+"/"+run.ID+"/precompute/data.zip", handoff.Bytes())
	}
	// The original source can disappear after submission: snapshot remains usable.
	env.Objects.Delete("/fixture/original.zip")
	output := filepath.Join(t.TempDir(), "experiment.zip")
	err = reproduction.Export(context.Background(), env.Config, plan, meta, reproduction.ExportOptions{Workflow: "fixture-workflow", Phase: "Succeeded", Prefix: prefix, ExecutionID: "fixture-execution", Output: output, CLIVersion: "fixture", IncludeLogs: logs}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return env, plan, meta, output
}

func TestCompleteBundleAndOfflineImport(t *testing.T) {
	source, plan, _, path := exported(t, false)
	archive, err := reproduction.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.Manifest.Maps) != 1 {
		t.Fatal("map snapshots were not deduplicated")
	}
	index, err := layout.Path(filepath.Join(archive.Directory, "images")).ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := index.IndexManifest()
	if err != nil || len(manifest.Manifests) != 1 {
		t.Fatal("shared image was not deduplicated")
	}
	for name := range archive.Manifest.Files {
		data, err := os.ReadFile(filepath.Join(archive.Directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"fixture-secret-do-not-export", "fixture-access-do-not-export", "SIGNED_URL_DO_NOT_EXPORT", "STATE_DO_NOT_EXPORT", "LOG_SENTINEL"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("unselected/private content in %s", name)
			}
		}
	}
	var reference worker.Summary
	raw, err := os.ReadFile(filepath.Join(archive.Directory, "results", plan.Runs[0].ID, "simulation/summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &reference); err != nil {
		t.Fatal(err)
	}
	if reference.Run.Configuration.Groups["A"] != "a1" {
		t.Fatal("old helper group labels were not restored")
	}
	raw, err = os.ReadFile(filepath.Join(archive.Directory, "experiment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := experiment.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("normalized YAML is not reusable: %v\n%s", err, raw)
	}
	// All original services become unavailable before import.
	source.Registry.Close()
	source.S3.Close()
	target := testfixture.NewTarget(t)
	reproduced, meta, mapping, err := archive.Import(context.Background(), target.Config, "reproduced-fixture", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapping) != len(plan.Runs) || reproduced.TotalRuns != plan.TotalRuns || reproduced.Experiment.Name != "reproduced-fixture" {
		t.Fatal("reproduction lost runs")
	}
	if !strings.Contains(meta.HelperImage, "/simworkflow/reproduced@sha256:") {
		t.Fatal("helper was not imported")
	}
	for i, run := range reproduced.Runs {
		if run.Seed != plan.Runs[i].Seed || run.Configuration.Groups["A"] != plan.Runs[i].Configuration.Groups["A"] || run.Configuration.Modules["Move.PathPlanning"] != plan.Runs[i].Configuration.Modules["Move.PathPlanning"] || run.Map.ExpectedSHA256() != plan.Runs[i].Map.ExpectedSHA256() {
			t.Fatal("reproduced execution conditions differ")
		}
		if strings.Contains(run.Configuration.Image, strings.TrimPrefix(source.Registry.URL, "http://")) {
			t.Fatal("reproduction still depends on original registry")
		}
	}
}

func TestLogsAreExplicitAndExistingZIPIsPreserved(t *testing.T) {
	env, plan, meta, path := exported(t, true)
	archive, err := reproduction.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, ok := archive.Manifest.Files["results/"+plan.Runs[0].ID+"/simulation/logs/simulation/kernel.log"]; !ok {
		t.Fatal("requested logs were omitted")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = reproduction.Export(context.Background(), env.Config, plan, meta, reproduction.ExportOptions{Workflow: "fixture-workflow", Phase: "Succeeded", Prefix: prefix, ExecutionID: "fixture-execution", Output: path}, io.Discard)
	if err == nil {
		t.Fatal("existing output should not be replaced")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing ZIP changed")
	}
}

func TestLegacyTaggedInputsFailWithoutInventingHistoricalDigest(t *testing.T) {
	env := testfixture.New(t)
	plan, err := experiment.BuildPlan(env.Experiment())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "incomplete.zip")
	err = reproduction.Export(context.Background(), env.Config, plan, reproduction.Metadata{}, reproduction.ExportOptions{Helper: env.Helper, Phase: "Succeeded", Output: path}, io.Discard)
	if err == nil {
		t.Fatal("historical tag cannot prove original contents")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("incomplete archive published")
	}
}

func TestImportRejectsExistingMapWithWrongContent(t *testing.T) {
	_, _, _, path := exported(t, false)
	archive, err := reproduction.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	target := testfixture.NewTarget(t)
	for sha := range archive.Manifest.Maps {
		target.Objects.Set("/fixture/reproduction/maps/"+sha+".zip", []byte("different content"))
	}
	if _, _, _, err := archive.Import(context.Background(), target.Config, "reproduced-fixture", io.Discard); err == nil {
		t.Fatal("existing map content was not verified")
	}
}

func TestBundleRejectsTamperingTraversalAndMissingBlobs(t *testing.T) {
	_, _, _, path := exported(t, false)
	for _, kind := range []string{"tamper", "traversal", "duplicate", "symlink", "missing-layer", "extra-map"} {
		t.Run(kind, func(t *testing.T) {
			z, err := zip.OpenReader(path)
			if err != nil {
				t.Fatal(err)
			}
			defer z.Close()
			output := filepath.Join(t.TempDir(), "invalid.zip")
			f, err := os.Create(output)
			if err != nil {
				t.Fatal(err)
			}
			w := zip.NewWriter(f)
			for _, entry := range z.File {
				r, err := entry.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(r)
				r.Close()
				if err != nil {
					t.Fatal(err)
				}
				if kind == "tamper" && entry.Name == "plan.json" {
					data = append(data, ' ')
				}
				if kind == "extra-map" && entry.Name == "manifest.json" {
					var manifest reproduction.Manifest
					if err := json.Unmarshal(data, &manifest); err != nil {
						t.Fatal(err)
					}
					manifest.Maps[strings.Repeat("a", 64)] = "../outside.zip"
					data = testfixture.JSON(t, manifest)
				}
				if kind == "missing-layer" && strings.HasPrefix(entry.Name, "images/blobs/") {
					continue
				}
				header := entry.FileHeader
				header.CRC32 = 0
				out, err := w.CreateHeader(&header)
				if err != nil {
					t.Fatal(err)
				}
				out.Write(data)
			}
			if kind == "traversal" {
				out, _ := w.Create("../escaped")
				out.Write([]byte("unsafe"))
			}
			if kind == "duplicate" {
				out, _ := w.Create("plan.json")
				out.Write([]byte("{}"))
			}
			if kind == "symlink" {
				header := &zip.FileHeader{Name: "link"}
				header.SetMode(os.ModeSymlink | 0700)
				out, _ := w.CreateHeader(header)
				out.Write([]byte("../escaped"))
			}
			w.Close()
			f.Close()
			archive, err := reproduction.Open(output)
			if err == nil {
				archive.Close()
				t.Fatal("unsafe or incomplete archive accepted")
			}
		})
	}
}
