package main

import (
	"fmt"
	"strings"

	"simworkflow/internal/experiment"
)

// Use the submitted, expanded plan, so matrix choices and resolved map versions
// describe this run rather than a subsequently edited local YAML file.
func runDetails(plan *experiment.Plan, run experiment.Run) []string {
	var b strings.Builder
	fmt.Fprintf(&b, "Experiment: %s\nDescription: %s\nRun: %s\nConfiguration: %s\nSeed: %d\n", plan.Experiment.Name, plan.Experiment.Description, run.ID, run.Configuration.ID, run.Seed)
	fmt.Fprintf(&b, "Max parallel runs: %d\nTimeout per run: %s\nCollect: %s\n", plan.MaxParallelRuns, plan.Timeout, strings.Join(plan.Collect, ", "))
	fmt.Fprintf(&b, "\nMap\n  Name: %s\n  Version: %s\n  Reference: %s\n  Source: %s\n  SHA256: %s\n", run.Map.Name, run.Map.Version, run.Map.Ref, run.Map.Location(), run.Map.ExpectedSHA256())
	if run.Map.Resolved != nil {
		fmt.Fprintf(&b, "  Registered name: %s\n  Resolved version: %s\n", run.Map.Resolved.Name, run.Map.Resolved.Version)
	}
	fmt.Fprintf(&b, "\nSimulator\n  Image: %s\n  CPU: %s cores\n  Memory: %s\n", run.Simulator.Image, run.Simulator.Resources.CPU, run.Simulator.Resources.Memory)
	if p := run.Simulator.Parameters; p != nil && p.Timesteps != nil {
		fmt.Fprintf(&b, "  Timesteps: %d\n", *p.Timesteps)
	} else {
		fmt.Fprintln(&b, "  Timesteps: map default")
	}
	c := run.Configuration
	fmt.Fprintf(&b, "\nAgent\n  Name: %s\n  Runtime: %s\n  Image: %s\n  Default CPU: %s cores\n  Default memory: %s\n", c.Agent, c.Runtime, c.Image, c.Resources.CPU, c.Resources.Memory)
	fmt.Fprintln(&b, "\nEffective resources by role (including corresponding centre)")
	for _, role := range experiment.Roles() {
		r := c.ResourcesForRole(role)
		fmt.Fprintf(&b, "  %s: CPU %s cores / Memory %s\n", role, r.CPU, r.Memory)
	}
	fmt.Fprintln(&b, "\nModules (expanded configuration)")
	for _, key := range sortedModuleKeys(c.Modules) {
		fmt.Fprintf(&b, "  %s\n    %s\n", key, c.Modules[key])
	}
	if len(c.Modules) == 0 {
		fmt.Fprintln(&b, "  No overrides (agent image defaults)")
	}
	return strings.Split(strings.TrimSuffix(screenText(b.String()), "\n"), "\n")
}
