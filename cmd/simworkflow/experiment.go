package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"

	"simworkflow/internal/experiment"
)

func runExperiment(args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected experiment validate, plan or modules")
	}
	command := args[0]
	if command == "modules" {
		return runModules(args[1:], out, errOut)
	}
	if command != "validate" && command != "plan" {
		return fmt.Errorf("unknown experiment command %q (expected validate, plan or modules)", command)
	}
	flags := flag.NewFlagSet("experiment "+command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	file := flags.String("f", "", "experiment YAML file (required)")
	maxRuns := flags.Int("max-runs", experiment.DefaultMaxRuns, "maximum number of expanded runs")
	var format string
	if command == "plan" {
		flags.StringVar(&format, "format", "text", "output format: text or json")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *file == "" || flags.NArg() != 0 {
		return errors.New("specify an experiment with -f FILE; positional arguments are not supported")
	}
	if command == "plan" && format != "text" && format != "json" {
		return errors.New("format must be text or json")
	}
	e, err := experiment.ReadFile(*file)
	if err != nil {
		return err
	}
	plan, err := experiment.BuildPlan(e, *maxRuns)
	if err != nil {
		return err
	}
	if command == "validate" {
		_, err = fmt.Fprintf(out, "Experiment %s is valid: %d runs (no network requests made).\n", e.Metadata.Name, plan.TotalRuns)
		return err
	}
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(plan)
	}
	return printPlan(out, plan)
}

func printPlan(out io.Writer, plan *experiment.Plan) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Experiment:\t%s\nTotal runs:\t%d\nMax parallel runs:\t%d\nTimeout per run:\t%s\n", plan.Experiment.Name, plan.TotalRuns, plan.MaxParallelRuns, plan.Timeout)
	fmt.Fprintln(w, "\nAGENT\tCONFIGURATIONS\tRUNS")
	for _, agent := range plan.Agents {
		fmt.Fprintf(w, "%s\t%d\t%d\n", agent.Name, agent.Configurations, agent.Runs)
	}
	fmt.Fprintln(w, "\nRUN\tAGENT\tMAP\tSEED\tSIMULATOR CPU / MEMORY\tDEFAULT PER-ROLE CPU / MEMORY")
	for _, run := range plan.Runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s / %s\t%s / %s\n", run.ID, run.Configuration.Agent, run.Map.Name, run.Seed, run.Simulator.Resources.CPU, run.Simulator.Resources.Memory, run.Configuration.Resources.CPU, run.Configuration.Resources.Memory)
		for _, role := range experiment.Roles() {
			resources := run.Configuration.ResourcesForRole(role)
			fmt.Fprintf(w, "  %s (+ centre): %s CPU / %s\n", role, resources.CPU, resources.Memory)
		}
		keys := sortedModuleKeys(run.Configuration.Modules)
		for _, key := range keys {
			fmt.Fprintf(w, "  %s = %s\n", key, run.Configuration.Modules[key])
		}
	}
	fmt.Fprintln(w, "\nNo network requests made. No workflows submitted.")
	return w.Flush()
}

func sortedModuleKeys(modules map[string]string) []string {
	// JSON output already sorts map keys. Keep human output deterministic too.
	keys := make([]string, 0, len(modules))
	for key := range modules {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
