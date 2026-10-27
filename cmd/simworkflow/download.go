package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"simworkflow/internal/argo"
	"simworkflow/internal/artifacts"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

type bundleFiles []string

func (f *bundleFiles) String() string         { return strings.Join(*f, ",") }
func (f *bundleFiles) Set(value string) error { *f = append(*f, value); return nil }

func csvOptions(value string) []string {
	if value == "" {
		return nil
	}
	values := strings.Split(value, ",")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return values
}

func runDownload(ctx context.Context, args []string, c config.Config, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("experiment download", flag.ContinueOnError)
	flags.SetOutput(errOut)
	include := flags.String("include", "all", "comma-separated: all, plan, summary (or score), progress, simulatorLogs, agentLogs, precompute, modules, config, bundle")
	runs := flags.String("run", "", "comma-separated run IDs; default: all runs")
	stage := flags.String("stage", "all", "precompute, simulation or all (precompute data.zip is selected independently)")
	output := flags.String("output", "", "output directory; supports adding to this experiment; default: artifacts/WORKFLOW_NAME")
	withRetries := flags.Bool("with-retries", false, "combine the original workflow and retries, preferring the latest successful attempt per run")
	var files bundleFiles
	flags.Var(&files, "file", "exact file path inside a bundle, e.g. logs/simulation/rescue.log.7z; repeat to select multiple files")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: simworkflow experiment download [options] WORKFLOW_NAME")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("specify one workflow name after download options")
	}
	name := flags.Arg(0)
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return errors.New("invalid workflow name")
	}
	if *output == "" {
		*output = filepath.Join("artifacts", name)
		if *withRetries {
			*output += "-with-retries"
		}
	}
	explicitInclude := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "include" {
			explicitInclude = true
		}
	})
	if len(files) > 0 && !explicitInclude {
		*include = ""
	}
	var attempts map[string]artifacts.Attempt
	var combinedPlan *experiment.Plan
	var combinedSnapshot *argo.Snapshot
	if *withRetries {
		requested, err := loadSavedExecution(ctx, c, name)
		if err != nil {
			return err
		}
		family, err := retryFamily(ctx, c, requested)
		if err != nil {
			return err
		}
		combinedPlan, combinedSnapshot = family[0].plan, family[0].snapshot
		attempts = combinedAttempts(family)
	}
	snapshot := combinedSnapshot
	var err error
	if snapshot == nil {
		snapshot, err = argo.New(c).Get(ctx, name)
	}
	if err != nil {
		return err
	}
	prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
	if !artifacts.ValidPrefix(prefix) {
		return errors.New("workflow has no valid SimWorkflow result prefix")
	}
	store := storage.New(c)
	var plan experiment.Plan
	if combinedPlan != nil {
		plan = *combinedPlan
	} else if err = store.GetJSON(ctx, prefix+"/plan.json", &plan); err != nil {
		return err
	}
	workflowName, phase := name, snapshot.Status.Phase
	if *withRetries {
		workflowName, phase = snapshot.Metadata.Name, combinedPhase(attempts)
	}
	report, err := artifacts.Download(ctx, store, &plan, prefix, artifacts.Options{Workflow: workflowName, Phase: phase, Output: *output, Include: csvOptions(*include), Runs: csvOptions(*runs), Stage: *stage, Files: files, WithRetries: *withRetries, Attempts: attempts})
	if report.Added > 0 || len(report.Skipped) > 0 {
		if _, writeErr := fmt.Fprintf(out, "Downloaded %d files to %s (%s). Manifest: download.json\n", report.Added, *output, phase); writeErr != nil {
			return errors.Join(err, writeErr)
		}
	}
	if len(report.Skipped) > 0 {
		if _, e := fmt.Fprintf(out, "Skipped %d verified existing files.\n", len(report.Skipped)); e != nil {
			return errors.Join(err, e)
		}
	}
	if len(report.Unavailable) > 0 {
		if _, writeErr := fmt.Fprintf(out, "Unavailable: %d requested objects or selections; see download.json.\n", len(report.Unavailable)); writeErr != nil {
			return errors.Join(err, writeErr)
		}
	}
	return err
}
