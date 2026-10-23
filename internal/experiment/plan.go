package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

// DefaultMaxRuns prevents accidental allocation of enormous Cartesian products.
// It can be raised explicitly by a local planning command.
const DefaultMaxRuns = 10000

type Plan struct {
	APIVersion      string         `json:"apiVersion"`
	Experiment      Metadata       `json:"experiment"`
	TotalRuns       int            `json:"totalRuns"`
	MaxParallelRuns int            `json:"maxParallelRuns"`
	Timeout         string         `json:"timeout"`
	Collect         []string       `json:"collect"`
	Agents          []AgentSummary `json:"agents"`
	Runs            []Run          `json:"runs"`
}

type AgentSummary struct {
	Name           string `json:"name"`
	Configurations int    `json:"configurations"`
	Runs           int    `json:"runs"`
}

type Configuration struct {
	ID        string            `json:"id"`
	Agent     string            `json:"agent"`
	Runtime   string            `json:"runtime"`
	Image     string            `json:"image"`
	Resources Resources         `json:"resources"`
	Modules   map[string]string `json:"modules"`
}

type Run struct {
	ID            string        `json:"id"`
	Configuration Configuration `json:"configuration"`
	Simulator     Container     `json:"simulator"`
	Map           Map           `json:"map"`
	Seed          int64         `json:"seed"`
}

// BuildPlan orders axes by key, candidates by input order, then maps and seeds
// by input order. IDs describe resolved inputs and do not depend on map order.
// They identify a planned configuration/run, not an execution attempt.
func BuildPlan(e *Experiment, maxRuns int) (*Plan, error) {
	if maxRuns <= 0 {
		return nil, errors.New("max runs must be a positive integer")
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	// Division avoids overflow in len(maps)*len(seeds).
	if len(e.Spec.Conditions.Maps) > maxRuns/len(e.Spec.Conditions.Seeds) {
		return nil, fmt.Errorf("map/seed combinations exceed the limit of %d runs", maxRuns)
	}
	runsPerConfig := len(e.Spec.Conditions.Maps) * len(e.Spec.Conditions.Seeds)
	plan := &Plan{
		APIVersion: APIVersion, Experiment: e.Metadata,
		MaxParallelRuns: e.Spec.Execution.MaxParallelRuns, Timeout: e.Spec.Execution.Timeout,
		Collect: append([]string(nil), e.Spec.Outputs.Collect...),
		Agents:  []AgentSummary{}, Runs: []Run{},
	}
	for _, agent := range e.Spec.Agents {
		count := 0
		err := expand(agent, func(modules map[string]string) error {
			if len(plan.Runs) > maxRuns-runsPerConfig {
				return fmt.Errorf("experiment exceeds the limit of %d runs; raise --max-runs explicitly if intended", maxRuns)
			}
			runtime := agent.Runtime
			if runtime == "" {
				runtime = "java"
			}
			configuration := Configuration{Agent: agent.Name, Runtime: runtime, Image: agent.Image, Resources: agent.Resources, Modules: maps.Clone(modules)}
			configuration.ID = "config-" + digest(configuration)
			count++
			for _, m := range e.Spec.Conditions.Maps {
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

func expand(agent Agent, emit func(map[string]string) error) error {
	modules := maps.Clone(agent.Modules)
	if modules == nil {
		modules = map[string]string{}
	}
	if agent.Matrix == nil {
		return emit(modules)
	}
	keys := sortedKeys(agent.Matrix.Modules)
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
			if matched {
				return nil
			}
		}
		if index == len(keys) {
			return emit(modules)
		}
		key := keys[index]
		for _, value := range agent.Matrix.Modules[key] {
			modules[key] = value
			if err := visit(index + 1); err != nil {
				return err
			}
		}
		delete(modules, key)
		return nil
	}
	return visit(0)
}
