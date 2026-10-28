package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

type Plan struct {
	CollectorResources *Resources     `json:"collectorResources,omitempty"`
	UnresolvedMaps     bool           `json:"unresolvedMaps,omitempty"`
	APIVersion         string         `json:"apiVersion"`
	Experiment         Metadata       `json:"experiment"`
	TotalRuns          int            `json:"totalRuns"`
	MaxParallelRuns    int            `json:"maxParallelRuns"`
	Timeout            string         `json:"timeout"`
	Collect            []string       `json:"collect"`
	Agents             []AgentSummary `json:"agents"`
	Runs               []Run          `json:"runs"`
}

type AgentSummary struct {
	Name           string `json:"name"`
	Configurations int    `json:"configurations"`
	Runs           int    `json:"runs"`
}

type Configuration struct {
	ID              string               `json:"id"`
	Agent           string               `json:"agent"`
	Runtime         string               `json:"runtime"`
	Image           string               `json:"image"`
	Resources       Resources            `json:"resources"`
	ResourcesByRole map[string]Resources `json:"resourcesByRole,omitempty"`
	Modules         map[string]string    `json:"modules"`
	Groups          map[string]string    `json:"groups,omitempty"`
}

type Run struct {
	ID            string        `json:"id"`
	Configuration Configuration `json:"configuration"`
	Simulator     Container     `json:"simulator"`
	Map           Map           `json:"map"`
	Seed          int64         `json:"seed"`
}

// BuildPlan orders group axes, then module axes by key, candidates by input order, then maps and seeds
// by input order. IDs describe resolved inputs and do not depend on map order.
// They identify a planned configuration/run, not an execution attempt.
func BuildPlan(e *Experiment) (*Plan, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	conditionMaps := ExpandMaps(e.Spec.Conditions.Maps)
	// Only guard native integer overflow; there is no configurable run-count cap.
	maxInt := int(^uint(0) >> 1)
	if len(conditionMaps) > maxInt/len(e.Spec.Conditions.Seeds) {
		return nil, errors.New("map/seed combinations exceed the addressable plan size")
	}
	runsPerConfig := len(conditionMaps) * len(e.Spec.Conditions.Seeds)
	plan := &Plan{
		APIVersion: APIVersion, Experiment: e.Metadata,
		MaxParallelRuns: e.Spec.Execution.MaxParallelRuns, Timeout: e.Spec.Execution.Timeout,
		Collect: append([]string(nil), e.Spec.Outputs.Collect...),
		Agents:  []AgentSummary{}, Runs: []Run{},
	}
	if r := e.Spec.Execution.CollectorResources; r != nil {
		copy := *r
		plan.CollectorResources = &copy
	}
	for _, m := range conditionMaps {
		if m.Version != "" && m.Resolved == nil {
			plan.UnresolvedMaps = true
		}
	}
	for _, agent := range e.Spec.Agents {
		count := 0
		err := expand(agent, func(modules, groups map[string]string) error {
			if len(plan.Runs) > maxInt-runsPerConfig {
				return errors.New("experiment exceeds the addressable plan size")
			}
			runtime := agent.Runtime
			if runtime == "" {
				runtime = "java"
			}
			configuration := Configuration{Agent: agent.Name, Runtime: runtime, Image: agent.Image, Resources: agent.Resources, ResourcesByRole: maps.Clone(agent.ResourcesByRole), Modules: maps.Clone(modules), Groups: maps.Clone(groups)}
			configuration.ID = "config-" + digest(configuration)
			count++
			for _, m := range conditionMaps {
				for _, seed := range e.Spec.Conditions.Seeds {
					run := Run{Configuration: configuration, Simulator: e.Spec.Simulator, Map: m, Seed: seed}
					run.ID = "run-" + digest(run)
					plan.Runs = append(plan.Runs, run)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, fmt.Errorf("agent %s: exclusions remove all configurations", agent.Name)
		}
		plan.Agents = append(plan.Agents, AgentSummary{Name: agent.Name, Configurations: count, Runs: count * runsPerConfig})
	}
	plan.TotalRuns = len(plan.Runs)
	return plan, nil
}

func digest(value any) string {
	// All values passed here are closed, JSON-encodable structs.
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

func ConfigurationIdentity(c Configuration) string {
	c.ID = ""
	return "config-" + digest(c)
}

func RunIdentity(r Run) string {
	r.ID = ""
	return "run-" + digest(r)
}

func expand(agent Agent, emit func(map[string]string, map[string]string) error) error {
	modules := maps.Clone(agent.Modules)
	if modules == nil {
		modules = map[string]string{}
	}
	if agent.Matrix == nil {
		return emit(modules, nil)
	}
	type axis struct {
		group   string
		options []ModuleSet
	}
	axes := []axis{}
	for _, name := range sortedKeys(agent.Matrix.Groups) {
		axes = append(axes, axis{group: name, options: agent.Matrix.Groups[name]})
	}
	for _, key := range sortedKeys(agent.Matrix.Modules) {
		options := []ModuleSet{}
		for _, value := range agent.Matrix.Modules[key] {
			options = append(options, ModuleSet{Modules: map[string]string{key: value}})
		}
		axes = append(axes, axis{options: options})
	}
	groups := map[string]string{}
	var visit func(int) error
	visit = func(index int) error {
		// Prune a branch as soon as an exclusion fully matches the assigned
		// axes. Large excluded subtrees never need to be materialized.
		for _, exclusion := range agent.Matrix.Exclude {
			matched := true
			for key, value := range exclusion.Modules {
				if modules[key] != value {
					matched = false
					break
				}
			}
			for key, value := range exclusion.Groups {
				if groups[key] != value {
					matched = false
					break
				}
			}
			if matched {
				return nil
			}
		}
		if index == len(axes) {
			return emit(modules, groups)
		}
		current := axes[index]
		for _, option := range current.options {
			for key, value := range option.Modules {
				modules[key] = value
			}
			if current.group != "" {
				groups[current.group] = option.Name
			}
			if err := visit(index + 1); err != nil {
				return err
			}
			for key := range option.Modules {
				delete(modules, key)
			}
			delete(groups, current.group)
		}
		return nil
	}
	return visit(0)
}
