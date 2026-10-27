package workflow

import (
	"encoding/json"
	"strconv"
	"strings"
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

func TestHundredRunRemoteConfigurationsKeepWorkflowBelowSizeLimit(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/python-module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Two independent module axes and 25 seeds form 100 synthetic runs.
	e.Spec.Agents[0].Matrix.Modules["SampleSearch.PathPlanning"] = []string{
		"my_team.path_planning.FirstPathPlanning",
		"my_team.path_planning.SecondPathPlanning",
	}
	e.Spec.Conditions.Seeds = make([]int64, 25)
	for i := range e.Spec.Conditions.Seeds {
		e.Spec.Conditions.Seeds[i] = int64(i + 1)
	}
	e.Spec.Execution.MaxParallelRuns = 10
	plan, err := experiment.BuildPlan(e, 100)
	if err != nil {
		t.Fatal(err)
	}
	var configs []RunConfig
	secret := strings.Repeat("embedded-capability-", 200)
	for _, run := range plan.Runs {
		configs = append(configs, RunConfig{
			Precompute:    worker.Config{Stage: worker.Precompute, Run: run, TimeoutSeconds: 2700, PrecomputeUpload: &storage.Transfer{URL: secret}},
			Simulation:    worker.Config{Stage: worker.Simulation, Run: run, TimeoutSeconds: 2700, PrecomputeDownload: &storage.Transfer{URL: secret}},
			PrecomputeURL: "https://s3.example/config/" + run.ID + "/precompute.json?signature=" + strings.Repeat("a", 300),
			SimulationURL: "https://s3.example/config/" + run.ID + "/simulation.json?signature=" + strings.Repeat("b", 300),
		})
	}
	wf, err := Build(plan, configs, Options{Namespace: "argo", HelperImage: "worker:test", ServiceAccount: "runner", ExecutionID: "test", ResultPrefix: "experiments/test"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(wf)
	if len(data) > 900*1024 || strings.Contains(string(data), secret) {
		t.Fatalf("remote-config Workflow must stay below size limit: %d bytes", len(data))
	}
	spec := wf["spec"].(map[string]any)
	if spec["parallelism"] != 10 {
		t.Fatal("parallelism must remain 10")
	}
	count := 0
	for _, raw := range spec["templates"].([]any) {
		template := raw.(map[string]any)
		if init, ok := template["initContainers"]; ok {
			env := init.([]any)[0].(map[string]any)["env"].([]any)[0].(map[string]any)
			if env["name"] != "SIMWORKFLOW_RUN_URL" {
				t.Fatal("configuration must be a URL reference")
			}
			count++
		}
	}
	if count != 2 {
		t.Fatal("all 100 runs must share the two stage templates")
	}
}

func TestFourHundredRunsShareTemplatesAndKeepEachConfigurationScoped(t *testing.T) {
	e, err := experiment.ReadFile("../../examples/experiments/python-module-comparison.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e.Spec.Agents[0].Matrix.Modules["SampleSearch.PathPlanning"] = []string{"example.First", "example.Second"}
	e.Spec.Conditions.Seeds = make([]int64, 100)
	for i := range e.Spec.Conditions.Seeds {
		e.Spec.Conditions.Seeds[i] = int64(i + 1)
	}
	e.Spec.Execution.MaxParallelRuns = 12
	plan, err := experiment.BuildPlan(e, 400)
	if err != nil {
		t.Fatal(err)
	}
	var configs []RunConfig
	for _, run := range plan.Runs {
		configs = append(configs, RunConfig{
			Precompute:    worker.Config{Stage: worker.Precompute, Run: run, TimeoutSeconds: 1800, PrecomputeUpload: &storage.Transfer{URL: "https://example.com/private-put"}},
			Simulation:    worker.Config{Stage: worker.Simulation, Run: run, TimeoutSeconds: 1800, PrecomputeDownload: &storage.Transfer{URL: "https://example.com/private-get"}},
			PrecomputeURL: "https://s3.example/experiments/execution/configs/" + run.ID + "/precompute.json?signature=" + strings.Repeat("a", 600),
			SimulationURL: "https://s3.example/experiments/execution/configs/" + run.ID + "/simulation.json?signature=" + strings.Repeat("b", 600),
		})
	}
	options := Options{Namespace: "argo", HelperImage: "worker:test", ServiceAccount: "runner"}
	wf, err := Build(plan, configs, options)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(wf)
	t.Logf("400-run definition: %d bytes", len(data))
	if len(data) >= 900*1024 || strings.Contains(string(data), "private-put") || strings.Contains(string(data), "private-get") {
		t.Fatal("workflow must remain small and contain only scoped configuration URLs")
	}
	spec := wf["spec"].(map[string]any)
	templates := spec["templates"].([]any)
	if spec["parallelism"] != 12 || len(templates) != 4 {
		t.Fatal("expected one DAG, one shared run wrapper and two shared stages")
	}
	byName := map[string]map[string]any{}
	for _, raw := range templates {
		template := raw.(map[string]any)
		byName[template["name"].(string)] = template
	}
	tasks := byName["experiment"]["dag"].(map[string]any)["tasks"].([]any)
	if len(tasks) != 400 || byName["experiment"]["dag"].(map[string]any)["failFast"] != false {
		t.Fatal("runs must remain independent DAG tasks")
	}
	for i, raw := range tasks {
		task := raw.(map[string]any)
		if task["name"] != plan.Runs[i].ID {
			t.Fatal("task identities must match the saved plan")
		}
		arguments := task["arguments"].(map[string]any)["parameters"].([]any)
		if arguments[0].(map[string]any)["value"] != configs[i].PrecomputeURL || arguments[1].(map[string]any)["value"] != configs[i].SimulationURL {
			t.Fatal("run must receive its own scoped stage configurations")
		}
		steps := byName[task["template"].(string)]["steps"].([]any)
		for j, stage := range []string{worker.Precompute, worker.Simulation} {
			step := steps[j].([]any)[0].(map[string]any)
			if step["name"] != stage || step["arguments"].(map[string]any)["parameters"].([]any)[0].(map[string]any)["value"] != "{{inputs.parameters."+stage+"-url}}" {
				t.Fatal("stages must run in order with separate configurations")
			}
			pod := byName[step["template"].(string)]
			if pod["activeDeadlineSeconds"] != int64(1980) || (pod["retryStrategy"] != nil) != (stage == worker.Simulation) {
				t.Fatal("stage deadline and simulation-only retry must be preserved")
			}
		}
	}
	// Only container/pod settings affect sharing. Module and seed variations
	// share templates, but changing an image or any role's resources must not.
	for _, change := range []func(*experiment.Run){
		func(r *experiment.Run) {
			r.Configuration.ResourcesByRole = map[string]experiment.Resources{"policeForce": {CPU: "2", Memory: "2Gi"}}
		},
		func(r *experiment.Run) { r.Simulator.Image = "different-simulator:v1" },
		func(r *experiment.Run) { r.Configuration.Image = "different-agent:v1" },
		func(r *experiment.Run) { r.Simulator.Resources.Memory = "12Gi" },
	} {
		original := plan.Runs[399]
		modified := original
		change(&modified)
		plan.Runs[399] = modified
		configs[399].Precompute.Run, configs[399].Simulation.Run = modified, modified
		wf, err := Build(plan, configs, options)
		if err != nil || len(wf["spec"].(map[string]any)["templates"].([]any)) != 7 {
			t.Fatal("different container settings need distinct stage templates and wrapper", err)
		}
		plan.Runs[399] = original
		configs[399].Precompute.Run, configs[399].Simulation.Run = original, original
	}
	bundledConfigs := append([]RunConfig(nil), configs...)
	for i := range bundledConfigs {
		bundledConfigs[i].PrecomputeURL, bundledConfigs[i].SimulationURL = "", ""
	}
	bundleOptions := options
	bundleOptions.ExecutionID = "execution"
	bundleOptions.PrecomputeBundle = &worker.ConfigBundleReference{URL: "https://s3.example/precompute.zip?signature=precompute-only", SHA256: strings.Repeat("a", 64)}
	bundleOptions.SimulationBundle = &worker.ConfigBundleReference{URL: "https://s3.example/simulation.zip?signature=simulation-only", SHA256: strings.Repeat("b", 64)}
	wf, err = Build(plan, bundledConfigs, bundleOptions)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(wf)
	t.Logf("400-run bundled definition: %d bytes", len(data))
	if len(data) > 100*1024 || strings.Count(string(data), "signature=precompute-only") != 1 || strings.Count(string(data), "signature=simulation-only") != 1 {
		t.Fatal("stage capabilities must occur once each, independently of run count")
	}
	spec = wf["spec"].(map[string]any)
	if len(spec["templates"].([]any)) != 4 || spec["parallelism"] != 12 {
		t.Fatal("bundling must preserve templates and concurrency")
	}
	for _, raw := range spec["templates"].([]any) {
		template := raw.(map[string]any)
		if dag, ok := template["dag"].(map[string]any); ok {
			for i, raw := range dag["tasks"].([]any) {
				task := raw.(map[string]any)
				parameters := task["arguments"].(map[string]any)["parameters"].([]any)
				if len(parameters) != 1 || parameters[0].(map[string]any)["name"] != "run-id" || parameters[0].(map[string]any)["value"] != plan.Runs[i].ID {
					t.Fatal("bundled tasks must carry only their own run ID")
				}
			}
		}
		if init, ok := template["initContainers"].([]any); ok {
			stage := worker.Precompute
			if strings.HasPrefix(template["name"].(string), "shared-simulation-") {
				stage = worker.Simulation
			}
			envs := map[string]any{}
			for _, raw := range init[0].(map[string]any)["env"].([]any) {
				e := raw.(map[string]any)
				envs[e["name"].(string)] = e["value"]
			}
			if len(envs) != 5 || envs["SIMWORKFLOW_CONFIG_BUNDLE_URL"] != "{{workflow.parameters."+stage+"-bundle-url}}" || envs["SIMWORKFLOW_CONFIG_BUNDLE_SHA256"] != "{{workflow.parameters."+stage+"-bundle-sha256}}" || envs["SIMWORKFLOW_STAGE"] != stage || envs["SIMWORKFLOW_EXECUTION_ID"] != "execution" {
				t.Fatal("stage must receive only its own verified bundle")
			}
		}
	}
	bad := bundleOptions
	bad.SimulationBundle = nil
	if _, err := Build(plan, bundledConfigs, bad); err == nil {
		t.Fatal("missing simulation bundle accepted")
	}
	if _, err := Build(plan, configs, bundleOptions); err == nil {
		t.Fatal("mixed configuration sources accepted")
	}
	bundleOptions.Sequences = true
	sequenceWorkflow, err := Build(plan, bundledConfigs, bundleOptions)
	if err != nil {
		t.Fatal(err)
	}
	sequenceData, _ := json.Marshal(sequenceWorkflow)
	t.Logf("400-run withSequence definition: %d bytes", len(sequenceData))
	if len(sequenceData) > 12*1024 || strings.Contains(string(sequenceData), plan.Runs[0].ID) {
		t.Fatal("sequence definition must replace per-run task enumeration")
	}
	checkSequences := func(wf map[string]any, runs []experiment.Run) {
		t.Helper()
		groups := experiment.RunSequences(runs)
		templates := wf["spec"].(map[string]any)["templates"].([]any)
		var tasks []any
		for _, raw := range templates {
			template := raw.(map[string]any)
			if dag, ok := template["dag"].(map[string]any); ok {
				if dag["failFast"] != false {
					t.Fatal("failure isolation lost")
				}
				tasks = dag["tasks"].([]any)
			}
		}
		if len(tasks) != len(groups) {
			t.Fatal("one loop is required per container profile")
		}
		for i, raw := range tasks {
			task := raw.(map[string]any)
			seq := task["withSequence"].(map[string]any)
			if task["name"] != groups[i].Name || seq["start"] != "0" || seq["count"] != strconv.Itoa(len(groups[i].Runs)) {
				t.Fatal("wrong loop extent or identity", task)
			}
			params := task["arguments"].(map[string]any)["parameters"].([]any)
			if params[0].(map[string]any)["value"] != groups[i].Name || params[1].(map[string]any)["value"] != "{{item}}" {
				t.Fatal("loop must pass sequence and local index")
			}
		}
	}
	checkSequences(sequenceWorkflow, plan.Runs)
	modified := plan.Runs[399]
	modified.Configuration.ResourcesByRole = map[string]experiment.Resources{"policeForce": {CPU: "2", Memory: "2Gi"}}
	plan.Runs[399] = modified
	bundledConfigs[399].Precompute.Run, bundledConfigs[399].Simulation.Run = modified, modified
	sequenceWorkflow, err = Build(plan, bundledConfigs, bundleOptions)
	if err != nil {
		t.Fatal(err)
	}
	checkSequences(sequenceWorkflow, plan.Runs)
	if len(sequenceWorkflow["spec"].(map[string]any)["templates"].([]any)) != 7 {
		t.Fatal("resource changes must retain distinct Pods")
	}
}
