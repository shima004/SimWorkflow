package experiment

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

func sample(t *testing.T) *Experiment {
	t.Helper()
	e, err := ReadFile("../../examples/experiments/module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSamplePlan(t *testing.T) {
	e := sample(t)
	plan, err := BuildPlan(e, 9)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalRuns != 9 || plan.Agents[0].Configurations != 3 {
		t.Fatalf("expected 3 configurations and 9 runs: %+v", plan.Agents)
	}
	ids := map[string]bool{}
	for _, run := range plan.Runs {
		if ids[run.ID] {
			t.Fatalf("duplicate run ID: %s", run.ID)
		}
		ids[run.ID] = true
		if run.Simulator.Resources.Memory != "8Gi" || run.Configuration.Resources.CPU != "4" {
			t.Fatal("resources must be retained independently for simulator and agent")
		}
		for _, exclude := range e.Spec.Agents[0].Matrix.Exclude {
			match := true
			for key, value := range exclude.Modules {
				match = match && run.Configuration.Modules[key] == value
			}
			if match {
				t.Fatal("excluded configuration was emitted")
			}
		}
	}
	if _, err := BuildPlan(e, 8); err == nil {
		t.Fatal("run limit must be enforced")
	}
	if _, err := BuildPlan(e, 0); err == nil {
		t.Fatal("nonpositive run limit must fail")
	}
	first, _ := json.Marshal(plan)
	// Reinsert the matrix keys in reverse order; Go map iteration cannot
	// affect either expansion order or identifiers.
	matrix := e.Spec.Agents[0].Matrix
	old := matrix.Modules
	matrix.Modules = map[string][]string{}
	keys := sortedKeys(old)
	for i := len(keys) - 1; i >= 0; i-- {
		matrix.Modules[keys[i]] = old[keys[i]]
	}
	again, err := BuildPlan(e, 9)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := json.Marshal(again)
	if string(first) != string(second) {
		t.Fatal("planning must be deterministic")
	}
}

func TestFixedAndMultipleAgents(t *testing.T) {
	e := sample(t)
	e.Spec.Agents[0].Matrix = nil
	e.Spec.Agents[0].Modules = map[string]string{"SampleRoadDetector.PathPlanning": "team.Planner"}
	other := e.Spec.Agents[0]
	other.Name = "second"
	e.Spec.Agents = append(e.Spec.Agents, other)
	e.Spec.Conditions.Maps = append(e.Spec.Conditions.Maps, Map{Name: "second-map", Source: "s3://maps/second.zip"})
	plan, err := BuildPlan(e, 12)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalRuns != 12 || len(plan.Agents) != 2 || !maps.Equal(plan.Runs[0].Configuration.Modules, other.Modules) {
		t.Fatal("fixed modules and agent/map/seed product must be preserved")
	}
	plan.Runs[0].Configuration.Modules["new.Key"] = "team.Other"
	if _, exists := e.Spec.Agents[0].Modules["new.Key"]; exists {
		t.Fatal("planning must not share the input module map")
	}
}

func TestExclusions(t *testing.T) {
	e := sample(t)
	a := &e.Spec.Agents[0]
	key := sortedKeys(a.Matrix.Modules)[0]
	values := a.Matrix.Modules[key]
	a.Matrix.Exclude = []Exclusion{{Modules: map[string]string{key: values[0]}}}
	plan, err := BuildPlan(e, 10)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalRuns != 6 {
		t.Fatal("partial exclusion must remove all configurations with the matching axis value")
	}
	a.Matrix.Exclude = append(a.Matrix.Exclude, Exclusion{Modules: map[string]string{key: values[1]}})
	if _, err := BuildPlan(e, 10); err == nil || !strings.Contains(err.Error(), "remove all") {
		t.Fatalf("all-excluded agent must fail, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Experiment)
		want string
	}{
		{"version", func(e *Experiment) { e.APIVersion = "unknown" }, "apiVersion"},
		{"kind", func(e *Experiment) { e.Kind = "Workflow" }, "kind"},
		{"name", func(e *Experiment) { e.Metadata.Name = "../bad" }, "metadata.name"},
		{"image", func(e *Experiment) { e.Spec.Simulator.Image = "" }, "simulator.image"},
		{"cpu", func(e *Experiment) { e.Spec.Simulator.Resources.CPU = "0.0001" }, "resources.cpu"},
		{"cpu-zero", func(e *Experiment) { e.Spec.Agents[0].Resources.CPU = "0" }, "resources.cpu"},
		{"cpu-inf", func(e *Experiment) { e.Spec.Simulator.Resources.CPU = ".inf" }, "resources.cpu"},
		{"memory", func(e *Experiment) { e.Spec.Agents[0].Resources.Memory = "4GB" }, "resources.memory"},
		{"memory-overflow", func(e *Experiment) { e.Spec.Simulator.Resources.Memory = "999999999999999Gi" }, "resources.memory"},
		{"no-agents", func(e *Experiment) { e.Spec.Agents = nil }, "spec.agents"},
		{"duplicate-agent", func(e *Experiment) { e.Spec.Agents = append(e.Spec.Agents, e.Spec.Agents[0]) }, ".name"},
		{"fixed-overlap", func(e *Experiment) {
			e.Spec.Agents[0].Modules = map[string]string{sortedKeys(e.Spec.Agents[0].Matrix.Modules)[0]: "team.Fixed"}
		}, "fixed modules"},
		{"empty-matrix", func(e *Experiment) { e.Spec.Agents[0].Matrix = &Matrix{} }, "matrix.modules"},
		{"empty-axis", func(e *Experiment) { e.Spec.Agents[0].Matrix.Modules["Empty"] = nil }, "candidate"},
		{"duplicate-candidate", func(e *Experiment) { e.Spec.Agents[0].Matrix.Modules["Test"] = []string{"team.A", "team.A"} }, "unique"},
		{"invalid-implementation", func(e *Experiment) { e.Spec.Agents[0].Modules = map[string]string{"Test": "bad\nvalue"} }, "identifiers"},
		{"unknown-exclude", func(e *Experiment) { e.Spec.Agents[0].Matrix.Exclude[0].Modules["Unknown"] = "team.A" }, "existing matrix"},
		{"empty-exclude", func(e *Experiment) { e.Spec.Agents[0].Matrix.Exclude = []Exclusion{{}} }, "at least one module"},
		{"no-maps", func(e *Experiment) { e.Spec.Conditions.Maps = nil }, "conditions.maps"},
		{"duplicate-map", func(e *Experiment) {
			e.Spec.Conditions.Maps = append(e.Spec.Conditions.Maps, e.Spec.Conditions.Maps[0])
		}, ".name"},
		{"map-source", func(e *Experiment) { e.Spec.Conditions.Maps[0].Source = "s3://bucket" }, ".source"},
		{"no-seeds", func(e *Experiment) { e.Spec.Conditions.Seeds = nil }, "conditions.seeds"},
		{"duplicate-seed", func(e *Experiment) { e.Spec.Conditions.Seeds = []int64{1, 1} }, "duplicate seeds"},
		{"parallelism", func(e *Experiment) { e.Spec.Execution.MaxParallelRuns = 0 }, "maxParallelRuns"},
		{"timeout", func(e *Experiment) { e.Spec.Execution.Timeout = "-1m" }, "timeout"},
		{"outputs", func(e *Experiment) { e.Spec.Outputs.Collect = []string{"unknown"} }, "outputs.collect"},
		{"duplicate-output", func(e *Experiment) { e.Spec.Outputs.Collect = []string{"score", "score"} }, "outputs.collect"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := sample(t)
			tc.edit(e)
			if err := e.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestStrictDecode(t *testing.T) {
	for _, text := range []string{
		"apiVersion: simworkflow/v1alpha1\nunknown: true\n",
		"apiVersion: a\napiVersion: b\n",
		"kind: Experiment\n---\nkind: Experiment\n",
		"spec:\n  simulator:\n    resources:\n      cpu: '2'\n",
		"spec:\n  simulator:\n    resources:\n      cpu: true\n",
		"spec: [invalid",
		"",
	} {
		if _, err := Decode(strings.NewReader(text)); err == nil {
			t.Fatalf("expected rejection of %q", text)
		}
	}
}

func TestResourceQuantities(t *testing.T) {
	for _, tc := range []struct {
		cpu   CPU
		milli int64
	}{{"2", 2000}, {"0.5", 500}, {"0.001", 1}, {"1.001", 1001}} {
		got, err := tc.cpu.MilliCores()
		if err != nil || got != tc.milli {
			t.Fatalf("%s: got %d, %v", tc.cpu, got, err)
		}
	}
	if got, err := (Resources{Memory: "4Gi"}).MemoryBytes(); err != nil || got != 4<<30 {
		t.Fatalf("memory: %d, %v", got, err)
	}
}
