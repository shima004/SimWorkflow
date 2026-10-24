// Package workflow builds Argo workflows from resolved experiment runs.
package workflow

import (
	"encoding/json"
	"errors"
	"strings"

	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

type Options struct {
	Namespace      string
	HelperImage    string
	ServiceAccount string
	ExecutionID    string
	ResultPrefix   string
}

type RunConfig struct {
	Precompute    worker.Config
	Simulation    worker.Config
	PrecomputeURL string
	SimulationURL string
}

func Build(plan *experiment.Plan, configs []RunConfig, options Options) (map[string]any, error) {
	if plan == nil || len(plan.Runs) == 0 || plan.MaxParallelRuns <= 0 {
		return nil, errors.New("workflow requires a nonempty validated plan")
	}
	if options.HelperImage == "" || options.Namespace == "" || options.ServiceAccount == "" || len(configs) != len(plan.Runs) {
		return nil, errors.New("workflow requires helper image, namespace, service account and one configuration per run")
	}
	tasks := []any{}
	templates := []any{}
	mounts := []any{map[string]any{"name": "work", "mountPath": "/work"}, map[string]any{"name": "tools", "mountPath": "/tools"}}
	resource := func(r experiment.Resources) map[string]any {
		q := map[string]any{"cpu": string(r.CPU), "memory": r.Memory}
		return map[string]any{"requests": q, "limits": q}
	}
	for i, run := range plan.Runs {
		pair := configs[i]
		if pair.Precompute.Stage != worker.Precompute || pair.Simulation.Stage != worker.Simulation || pair.Precompute.PrecomputeUpload == nil || pair.Precompute.PrecomputeDownload != nil || pair.Simulation.PrecomputeDownload == nil || pair.Simulation.PrecomputeUpload != nil {
			return nil, errors.New("workflow requires scoped precompute upload and simulation download configurations")
		}
		if pair.Precompute.TimeoutSeconds <= 0 || pair.Precompute.TimeoutSeconds != pair.Simulation.TimeoutSeconds {
			return nil, errors.New("stage timeouts must be equal and positive")
		}
		tasks = append(tasks, map[string]any{"name": run.ID, "template": run.ID})
		templates = append(templates, map[string]any{"name": run.ID, "steps": []any{
			[]any{map[string]any{"name": "precompute", "template": run.ID + "-precompute"}},
			[]any{map[string]any{"name": "simulation", "template": run.ID + "-simulation"}},
		}})
		for _, c := range []worker.Config{pair.Precompute, pair.Simulation} {
			if c.Run.ID != run.ID {
				return nil, errors.New("worker configuration does not match plan")
			}
			encoded, err := json.Marshal(c)
			if err != nil {
				return nil, err
			}
			env := map[string]any{"name": "SIMWORKFLOW_RUN", "value": string(encoded)}
			configURL := pair.PrecomputeURL
			if c.Stage == worker.Simulation {
				configURL = pair.SimulationURL
			}
			if configURL != "" {
				env = map[string]any{"name": "SIMWORKFLOW_RUN_URL", "value": configURL}
			}
			containers := []any{
				map[string]any{"name": "simulator", "image": run.Simulator.Image, "command": []string{"/tools/simworkflow", "worker", "server"}, "resources": resource(run.Simulator.Resources)},
				map[string]any{"name": "main", "image": options.HelperImage, "command": []string{"/tools/simworkflow", "worker", "collect"}, "env": []any{map[string]any{"name": "SIMWORKFLOW_POD_IP", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "status.podIP"}}}}, "resources": resource(experiment.Resources{CPU: "0.1", Memory: "256Mi"})},
			}
			for _, role := range experiment.Roles() {
				containers = append(containers, map[string]any{"name": worker.AgentActor(role), "image": run.Configuration.Image, "command": []string{"/tools/simworkflow", "worker", "agent", "--role", role}, "resources": resource(run.Configuration.ResourcesForRole(role))})
			}
			template := map[string]any{
				"name": run.ID + "-" + c.Stage, "activeDeadlineSeconds": c.TimeoutSeconds + 180,
				"initContainers": []any{map[string]any{"name": "prepare-experiment", "image": options.HelperImage, "command": []string{"/usr/local/bin/simworkflow", "worker", "prepare"}, "env": []any{env}, "volumeMounts": mounts, "resources": resource(experiment.Resources{CPU: "0.1", Memory: "256Mi"})}},
				"containerSet":   map[string]any{"volumeMounts": mounts, "containers": containers},
			}
			// Retry simulation once without recomputing or modifying its input data.
			if c.Stage == worker.Simulation {
				template["retryStrategy"] = map[string]any{"limit": "1", "retryPolicy": "Always"}
			}
			templates = append(templates, template)
		}
	}
	templates = append([]any{map[string]any{"name": "experiment", "dag": map[string]any{"failFast": false, "tasks": tasks}}}, templates...)
	name := plan.Experiment.Name
	if len(name) > 48 {
		name = strings.TrimRight(name[:48], "-")
	}
	waves := (len(configs)-1)/plan.MaxParallelRuns + 1
	deadline := int64(waves) * (configs[0].Precompute.TimeoutSeconds + 360)
	return map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow",
		"metadata": map[string]any{"generateName": name + "-", "namespace": options.Namespace, "labels": map[string]string{"app.kubernetes.io/managed-by": "simworkflow"}, "annotations": map[string]string{"simworkflow.io/execution-id": options.ExecutionID, "simworkflow.io/result-prefix": options.ResultPrefix}},
		"spec":     map[string]any{"entrypoint": "experiment", "activeDeadlineSeconds": deadline, "serviceAccountName": options.ServiceAccount, "parallelism": plan.MaxParallelRuns, "nodeSelector": map[string]string{"kubernetes.io/arch": "amd64"}, "volumes": []any{map[string]any{"name": "work", "emptyDir": map[string]any{}}, map[string]any{"name": "tools", "emptyDir": map[string]any{}}}, "templates": templates},
	}, nil
}
