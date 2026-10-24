package workflow

import (
	"encoding/json"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

func TestBuildTwoPhaseWorkflow(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/python-module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e.Spec.Agents[0].ResourcesByRole = map[string]experiment.Resources{"fireBrigade": {CPU: "0.5", Memory: "1Gi"}}
	plan, err := experiment.BuildPlan(e, 100)
	if err != nil {
		t.Fatal(err)
	}
	configs := []RunConfig{}
	for _, run := range plan.Runs {
		configs = append(configs, RunConfig{Precompute: worker.Config{Stage: worker.Precompute, Run: run, TimeoutSeconds: 1800, PrecomputeUpload: &storage.Transfer{URL: "https://s3.example/put"}}, Simulation: worker.Config{Stage: worker.Simulation, Run: run, TimeoutSeconds: 1800, PrecomputeDownload: &storage.Transfer{URL: "https://s3.example/get"}}})
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
	if len(templates) != 19 {
		t.Fatal("expected one DAG and six pairs of stage templates with their step wrappers")
	}
	stages, wrappers := 0, 0
	for _, template := range templates[1:] {
		template := template.(map[string]any)
		if steps, ok := template["steps"].([]any); ok {
			wrappers++
			if len(steps) != 2 || steps[0].([]any)[0].(map[string]any)["name"] != "precompute" || steps[1].([]any)[0].(map[string]any)["name"] != "simulation" {
				t.Fatal("stage order must be precompute then simulation")
			}
			continue
		}
		stages++
		init := template["initContainers"].([]any)[0].(map[string]any)
		encoded := init["env"].([]any)[0].(map[string]any)["value"].(string)
		var c worker.Config
		if err := json.Unmarshal([]byte(encoded), &c); err != nil {
			t.Fatal(err)
		}
		if c.Stage == worker.Precompute {
			if c.PrecomputeUpload == nil || c.PrecomputeDownload != nil || template["retryStrategy"] != nil {
				t.Fatal("precompute must only receive write access")
			}
		} else {
			if c.PrecomputeDownload == nil || c.PrecomputeUpload != nil || template["retryStrategy"] == nil {
				t.Fatal("simulation must only receive read access and retry independently")
			}
			if template["retryStrategy"].(map[string]any)["retryPolicy"] != "Always" {
				t.Fatal("simulation must retry both Failed and Error outcomes")
			}
		}
		if template["activeDeadlineSeconds"] != int64(1980) {
			t.Fatal("deadline must include artifact-upload grace")
		}
		containers := template["containerSet"].(map[string]any)["containers"].([]any)
		if len(containers) != 5 {
			t.Fatal("one simulator, three agent roles and one collector are required")
		}
		seen := map[string]bool{}
		for _, container := range containers {
			container := container.(map[string]any)
			if container["name"] == "main" {
				env := container["env"].([]any)[0].(map[string]any)
				if env["name"] != "SIMWORKFLOW_POD_IP" || env["valueFrom"].(map[string]any)["fieldRef"].(map[string]any)["fieldPath"] != "status.podIP" {
					t.Fatal("collector must receive the current Pod IP from the Downward API")
				}
			}
			for _, role := range experiment.Roles() {
				if container["name"] != worker.AgentActor(role) {
					continue
				}
				seen[role] = true
				command := container["command"].([]string)
				if len(command) != 5 || command[3] != "--role" || command[4] != role {
					t.Fatal("missing role selector")
				}
				resources := container["resources"].(map[string]any)
				want := plan.Runs[0].Configuration.ResourcesForRole(role)
				for _, key := range []string{"requests", "limits"} {
					got := resources[key].(map[string]any)
					if got["cpu"] != string(want.CPU) || got["memory"] != want.Memory {
						t.Fatalf("wrong resources for %s: %v", role, got)
					}
				}
			}
			if container["name"] == "simulator" {
				resources := container["resources"].(map[string]any)
				requests, _ := json.Marshal(resources["requests"])
				limits, _ := json.Marshal(resources["limits"])
				if string(requests) != string(limits) {
					t.Fatal("limits and requests must match the declared quantities")
				}
			}
		}
		if len(seen) != 3 {
			t.Fatal("missing role containers")
		}
	}
	if stages != 12 || wrappers != 6 {
		t.Fatal("missing stage or run templates")
	}
	configs[0].Simulation.Run.ID = "mismatch"
	if _, err := Build(plan, configs, options); err == nil {
		t.Fatal("mismatched worker configs must be rejected")
	}
}
