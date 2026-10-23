package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"simworkflow/internal/experiment"
)

func TestExperimentCommandsWithoutServiceCredentials(t *testing.T) {
	file := "../../examples/experiments/module-comparison.yaml"
	noEnv := func(string) (string, bool) { return "", false }
	var out, errOut bytes.Buffer
	if err := run([]string{"experiment", "validate", "-f", file}, noEnv, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "9 runs") {
		t.Fatalf("unexpected validation output: %s", &out)
	}
	out.Reset()
	if err := run([]string{"experiment", "plan", "-f", file, "--format", "json"}, noEnv, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var plan experiment.Plan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.TotalRuns != 9 || len(plan.Runs) != 9 || plan.Runs[0].Configuration.ID == "" {
		t.Fatalf("unexpected JSON plan: %+v", plan)
	}
	out.Reset()
	if err := run([]string{"experiment", "plan", "-f", file}, noEnv, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SampleRoadDetector.PathPlanning") || !strings.Contains(out.String(), "4Gi") {
		t.Fatal("text plan must show resolved modules and resource settings")
	}
	for _, args := range [][]string{
		{"experiment"},
		{"experiment", "unknown"},
		{"experiment", "plan"},
		{"experiment", "plan", "-f", file, "--max-runs", "8"},
		{"experiment", "plan", "-f", file, "--format", "yaml"},
		{"experiment", "validate", "-f", file, "unexpected"},
		{"experiment", "plan", "-f", "missing.yaml"},
	} {
		out.Reset()
		if err := run(args, noEnv, &out, &errOut); err == nil || out.Len() != 0 {
			t.Fatalf("expected failure without partial plan output for %v", args)
		}
	}
}
