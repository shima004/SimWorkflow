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

func Build(plan *experiment.Plan, configs []worker.Config, options Options) (map[string]any, error) {
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
		if configs[i].TimeoutSeconds <= 0 {
			return nil, errors.New("run timeout must be positive")
		}
		if configs[i].Run.ID != run.ID {
			return nil, errors.New("worker configuration does not match plan")
		}
		encoded, err := json.Marshal(configs[i])
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, map[string]any{"name": run.ID, "template": run.ID})
		containers := []any{
			map[string]any{"name": "simulator", "image": run.Simulator.Image, "command": []string{"/tools/simworkflow", "worker", "server"}, "resources": resource(run.Simulator.Resources)},
			map[string]any{"name": "main", "image": options.HelperImage, "command": []string{"/tools/simworkflow", "worker", "collect"}, "resources": resource(experiment.Resources{CPU: "0.1", Memory: "256Mi"})},
		}
		for _, role := range experiment.Roles() {
			containers = append(containers, map[string]any{"name": worker.AgentActor(role), "image": run.Configuration.Image, "command": []string{"/tools/simworkflow", "worker", "agent", "--role", role}, "resources": resource(run.Configuration.ResourcesForRole(role))})
		}
		templates = append(templates, map[string]any{
			"name": run.ID, "activeDeadlineSeconds": configs[i].TimeoutSeconds + 180,
			"initContainers": []any{map[string]any{"name": "prepare-experiment", "image": options.HelperImage, "command": []string{"/usr/local/bin/simworkflow", "worker", "prepare"}, "env": []any{map[string]any{"name": "SIMWORKFLOW_RUN", "value": string(encoded)}}, "volumeMounts": mounts, "resources": resource(experiment.Resources{CPU: "0.1", Memory: "256Mi"})}},
			"containerSet":   map[string]any{"volumeMounts": mounts, "containers": containers},
		})
	}
	templates = append([]any{map[string]any{"name": "experiment", "dag": map[string]any{"failFast": false, "tasks": tasks}}}, templates...)
	name := plan.Experiment.Name
	if len(name) > 48 {
		name = strings.TrimRight(name[:48], "-")
	}
	waves := (len(configs)-1)/plan.MaxParallelRuns + 1
	deadline := int64(waves) * (configs[0].TimeoutSeconds + 180)
	return map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow",
		"metadata": map[string]any{"generateName": name + "-", "namespace": options.Namespace, "labels": map[string]string{"app.kubernetes.io/managed-by": "simworkflow"}, "annotations": map[string]string{"simworkflow.io/execution-id": options.ExecutionID, "simworkflow.io/result-prefix": options.ResultPrefix}},
		"spec":     map[string]any{"entrypoint": "experiment", "activeDeadlineSeconds": deadline, "serviceAccountName": options.ServiceAccount, "parallelism": plan.MaxParallelRuns, "nodeSelector": map[string]string{"kubernetes.io/arch": "amd64"}, "volumes": []any{map[string]any{"name": "work", "emptyDir": map[string]any{}}, map[string]any{"name": "tools", "emptyDir": map[string]any{}}}, "templates": templates},
	}, nil
}
