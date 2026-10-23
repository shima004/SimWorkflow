package workflow

import (
	"encoding/json"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

func TestBuildTwoPhaseWorkflow(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/python-module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experiment.BuildPlan(e, 100)
	if err != nil {
		t.Fatal(err)
	}
	configs := []worker.Config{}
	for _, run := range plan.Runs {
		configs = append(configs, worker.Config{Run: run, TimeoutSeconds: 1800})
	}
	options := Options{Namespace: "experiments", HelperImage: "registry/worker:test", ServiceAccount: "runner", ExecutionID: "execution-test", ResultPrefix: "experiments/test"}
	wf, err := Build(plan, configs, options)
	if err != nil {
		t.Fatal(err)
	}
	spec := wf["spec"].(map[string]any)
	if spec["parallelism"] != 4 || spec["serviceAccountName"] != "runner" {
		t.Fatal("workflow must retain execution constraints")
	}
	templates := spec["templates"].([]any)
	if len(templates) != 7 {
		t.Fatal("expected one DAG and six isolated run templates")
	}
	for _, template := range templates[1:] {
		template := template.(map[string]any)
		if template["activeDeadlineSeconds"] != int64(1980) {
			t.Fatal("deadline must include artifact-upload grace")
		}
		containers := template["containerSet"].(map[string]any)["containers"].([]any)
		if len(containers) != 3 {
			t.Fatal("one simulator and one agent must restart their processes across the two phases without doubling resource allocation")
		}
		for _, container := range containers {
			container := container.(map[string]any)
			if container["name"] == "simulator" {
				resources := container["resources"].(map[string]any)
				requests, _ := json.Marshal(resources["requests"])
				limits, _ := json.Marshal(resources["limits"])
				if string(requests) != string(limits) {
					t.Fatal("limits and requests must match the declared quantities")
				}
			}
		}
	}
	configs[0].Run.ID = "mismatch"
	if _, err := Build(plan, configs, options); err == nil {
		t.Fatal("mismatched worker configs must be rejected")
	}
}
