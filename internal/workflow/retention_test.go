package workflow

import (
	"strings"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

func TestGeneratedWorkflowsCollectCompletedPodsWithoutExpiringHistory(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/python-module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e.Spec.Agents = e.Spec.Agents[:1]
	e.Spec.Agents[0].Matrix = nil
	e.Spec.Conditions.Maps = e.Spec.Conditions.Maps[:1]
	e.Spec.Conditions.Seeds = []int64{1}
	plan, err := experiment.BuildPlan(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"inline", "s3", "s3-bundle", "s3-sequence"} {
		t.Run(mode, func(t *testing.T) {
			run := plan.Runs[0]
			configs := []RunConfig{{
				Precompute: worker.Config{Stage: worker.Precompute, Run: run, TimeoutSeconds: 1800, PrecomputeUpload: &storage.Transfer{URL: "https://s3.example/precompute"}},
				Simulation: worker.Config{Stage: worker.Simulation, Run: run, TimeoutSeconds: 1800, PrecomputeDownload: &storage.Transfer{URL: "https://s3.example/precompute"}},
			}}
			opts := Options{Namespace: "experiments", HelperImage: "worker:test", ServiceAccount: "runner", ExecutionID: "execution", ResultPrefix: "experiments/execution"}
			if mode == "s3" {
				configs[0].PrecomputeURL = "https://s3.example/precompute.json"
				configs[0].SimulationURL = "https://s3.example/simulation.json"
			}
			if mode == "s3-bundle" || mode == "s3-sequence" {
				opts.PrecomputeBundle = &worker.ConfigBundleReference{URL: "https://s3.example/precompute.zip", SHA256: strings.Repeat("a", 64)}
				opts.SimulationBundle = &worker.ConfigBundleReference{URL: "https://s3.example/simulation.zip", SHA256: strings.Repeat("b", 64)}
				opts.Sequences = mode == "s3-sequence"
			}
			wf, err := Build(plan, configs, opts)
			if err != nil {
				t.Fatal(err)
			}
			spec := wf["spec"].(map[string]any)
			gc, ok := spec["podGC"].(map[string]any)
			if !ok || gc["strategy"] != "OnPodCompletion" || gc["deleteDelayDuration"] != "10m" {
				t.Fatal("successful and failed Pods should be collected after ten minutes", gc)
			}
			if _, exists := spec["ttlStrategy"]; exists {
				t.Fatal("Workflow history must remain available for status/download/retry/export")
			}
		})
	}
}
