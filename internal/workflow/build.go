// Package workflow builds Argo workflows from resolved experiment runs.
package workflow

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

type Options struct {
	Namespace        string
	HelperImage      string
	ServiceAccount   string
	ExecutionID      string
	ResultPrefix     string
	PrecomputeBundle *worker.ConfigBundleReference
	SimulationBundle *worker.ConfigBundleReference
	Sequences        bool
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
	bundled := options.PrecomputeBundle != nil || options.SimulationBundle != nil
	if options.Sequences && !bundled {
		return nil, errors.New("sequence workflows require verified stage bundles")
	}
	if bundled && (options.PrecomputeBundle == nil || options.SimulationBundle == nil || options.PrecomputeBundle.URL == "" || options.SimulationBundle.URL == "" || !experiment.ValidSHA256(options.PrecomputeBundle.SHA256) || !experiment.ValidSHA256(options.SimulationBundle.SHA256) || options.ExecutionID == "") {
		return nil, errors.New("workflow requires both stage bundle URLs, SHA-256 hashes and execution identity")
	}
	tasks := []any{}
	sequenceTasks := map[string]map[string]any{}
	sequenceCounts := map[string]int{}
	templates := []any{}
	sharedTemplates := map[string]bool{}
	addShared := func(template map[string]any, prefix string) (string, error) {
		delete(template, "name")
		data, err := json.Marshal(template)
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256(data)
		name := fmt.Sprintf("shared-%s-%x", prefix, digest[:16])
		if !sharedTemplates[name] {
			template["name"] = name
			templates = append(templates, template)
			sharedTemplates[name] = true
		}
		return name, nil
	}
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
		if bundled && (pair.PrecomputeURL != "" || pair.SimulationURL != "") {
			return nil, errors.New("bundle and individual configuration URLs cannot be mixed")
		}
		shared := bundled || pair.PrecomputeURL != "" && pair.SimulationURL != ""
		steps := []any{}
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
			if shared {
				env["value"] = "{{inputs.parameters.config-url}}"
			}
			envs := []any{env}
			if bundled {
				envs = []any{
					map[string]any{"name": "SIMWORKFLOW_CONFIG_BUNDLE_URL", "value": "{{workflow.parameters." + c.Stage + "-bundle-url}}"},
					map[string]any{"name": "SIMWORKFLOW_CONFIG_BUNDLE_SHA256", "value": "{{workflow.parameters." + c.Stage + "-bundle-sha256}}"},
					map[string]any{"name": "SIMWORKFLOW_RUN_ID", "value": "{{inputs.parameters.run-id}}"},
					map[string]any{"name": "SIMWORKFLOW_STAGE", "value": c.Stage},
					map[string]any{"name": "SIMWORKFLOW_EXECUTION_ID", "value": options.ExecutionID},
				}
				if options.Sequences {
					envs[2] = map[string]any{"name": "SIMWORKFLOW_SEQUENCE", "value": "{{inputs.parameters.sequence}}"}
					envs = append(envs, map[string]any{"name": "SIMWORKFLOW_RUN_INDEX", "value": "{{inputs.parameters.run-index}}"})
				}
			}
			containers := []any{
				map[string]any{"name": "simulator", "image": run.Simulator.Image, "command": []string{"/tools/simworkflow", "worker", "server"}, "resources": resource(run.Simulator.Resources)},
				map[string]any{"name": "main", "image": options.HelperImage, "command": []string{"/tools/simworkflow", "worker", "collect"}, "env": []any{map[string]any{"name": "SIMWORKFLOW_POD_IP", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "status.podIP"}}}}, "resources": resource(experiment.EffectiveCollectorResources(plan.CollectorResources))},
			}
			for _, role := range experiment.Roles() {
				containers = append(containers, map[string]any{"name": worker.AgentActor(role), "image": run.Configuration.Image, "command": []string{"/tools/simworkflow", "worker", "agent", "--role", role}, "resources": resource(run.Configuration.ResourcesForRole(role))})
			}
			template := map[string]any{
				"name": run.ID + "-" + c.Stage, "activeDeadlineSeconds": c.TimeoutSeconds + 180,
				"initContainers": []any{map[string]any{"name": "prepare-experiment", "image": options.HelperImage, "command": []string{"/usr/local/bin/simworkflow", "worker", "prepare"}, "env": envs, "volumeMounts": mounts, "resources": resource(experiment.Resources{CPU: "0.1", Memory: "256Mi"})}},
				"containerSet":   map[string]any{"volumeMounts": mounts, "containers": containers},
			}
			// Retry simulation once without recomputing or modifying its input data.
			if c.Stage == worker.Simulation {
				template["retryStrategy"] = map[string]any{"limit": "1", "retryPolicy": "Always"}
			}
			step := map[string]any{"name": c.Stage, "template": template["name"]}
			if shared {
				parameter, value := "config-url", "{{inputs.parameters."+c.Stage+"-url}}"
				if bundled {
					parameter, value = "run-id", "{{inputs.parameters.run-id}}"
				}
				template["inputs"] = map[string]any{"parameters": []any{map[string]any{"name": parameter}}}
				parameters := []any{map[string]any{"name": parameter, "value": value}}
				if options.Sequences {
					template["inputs"] = map[string]any{"parameters": []any{map[string]any{"name": "sequence"}, map[string]any{"name": "run-index"}}}
					parameters = []any{map[string]any{"name": "sequence", "value": "{{inputs.parameters.sequence}}"}, map[string]any{"name": "run-index", "value": "{{inputs.parameters.run-index}}"}}
				}
				name, err := addShared(template, c.Stage)
				if err != nil {
					return nil, err
				}
				step["template"] = name
				step["arguments"] = map[string]any{"parameters": parameters}
			} else {
				templates = append(templates, template)
			}
			steps = append(steps, []any{step})
		}
		wrapper := map[string]any{"name": run.ID, "steps": steps}
		task := map[string]any{"name": run.ID, "template": run.ID}
		if shared {
			wrapper["inputs"] = map[string]any{"parameters": []any{map[string]any{"name": "precompute-url"}, map[string]any{"name": "simulation-url"}}}
			if bundled {
				wrapper["inputs"] = map[string]any{"parameters": []any{map[string]any{"name": "run-id"}}}
			}
			if options.Sequences {
				wrapper["inputs"] = map[string]any{"parameters": []any{map[string]any{"name": "sequence"}, map[string]any{"name": "run-index"}}}
			}
			name, err := addShared(wrapper, "run")
			if err != nil {
				return nil, err
			}
			task["template"] = name
			task["arguments"] = map[string]any{"parameters": []any{
				map[string]any{"name": "precompute-url", "value": pair.PrecomputeURL},
				map[string]any{"name": "simulation-url", "value": pair.SimulationURL},
			}}
			if bundled {
				task["arguments"] = map[string]any{"parameters": []any{map[string]any{"name": "run-id", "value": run.ID}}}
			}
		} else {
			templates = append(templates, wrapper)
		}
		if options.Sequences {
			group := experiment.SequenceName(run)
			sequenceCounts[group]++
			if previous, ok := sequenceTasks[group]; ok {
				if previous["template"] != task["template"] {
					return nil, errors.New("sequence members have inconsistent pod settings or deadlines")
				}
				previous["withSequence"].(map[string]any)["count"] = strconv.Itoa(sequenceCounts[group])
			} else {
				task["name"] = group
				task["arguments"] = map[string]any{"parameters": []any{map[string]any{"name": "sequence", "value": group}, map[string]any{"name": "run-index", "value": "{{item}}"}}}
				task["withSequence"] = map[string]any{"start": "0", "count": "1"}
				sequenceTasks[group] = task
				tasks = append(tasks, task)
			}
		} else {
			tasks = append(tasks, task)
		}
	}
	templates = append([]any{map[string]any{"name": "experiment", "dag": map[string]any{"failFast": false, "tasks": tasks}}}, templates...)
	name := plan.Experiment.Name
	if len(name) > 48 {
		name = strings.TrimRight(name[:48], "-")
	}
	waves := (len(configs)-1)/plan.MaxParallelRuns + 1
	deadline := int64(waves) * (configs[0].Precompute.TimeoutSeconds + 360)
	spec := map[string]any{"entrypoint": "experiment", "activeDeadlineSeconds": deadline, "serviceAccountName": options.ServiceAccount, "parallelism": plan.MaxParallelRuns, "nodeSelector": map[string]string{"kubernetes.io/arch": "amd64"}, "volumes": []any{map[string]any{"name": "work", "emptyDir": map[string]any{}}, map[string]any{"name": "tools", "emptyDir": map[string]any{}}}, "templates": templates}
	if bundled {
		spec["arguments"] = map[string]any{"parameters": []any{
			map[string]any{"name": "precompute-bundle-url", "value": options.PrecomputeBundle.URL},
			map[string]any{"name": "precompute-bundle-sha256", "value": options.PrecomputeBundle.SHA256},
			map[string]any{"name": "simulation-bundle-url", "value": options.SimulationBundle.URL},
			map[string]any{"name": "simulation-bundle-sha256", "value": options.SimulationBundle.SHA256},
		}}
	}
	annotations := map[string]string{"simworkflow.io/execution-id": options.ExecutionID, "simworkflow.io/result-prefix": options.ResultPrefix}
	if options.Sequences {
		annotations["simworkflow.io/run-sequences"] = "v1"
	}
	return map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow",
		"metadata": map[string]any{"generateName": name + "-", "namespace": options.Namespace, "labels": map[string]string{"app.kubernetes.io/managed-by": "simworkflow"}, "annotations": annotations},
		"spec":     spec,
	}, nil
}
