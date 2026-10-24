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
	snapshot, err := argo.New(c).Get(ctx, name)
	if err != nil {
		return err
	}
	prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
	if !artifacts.ValidPrefix(prefix) {
		return errors.New("workflow has no valid SimWorkflow result prefix")
	}
	store := storage.New(c)
	var plan experiment.Plan
	if err = store.GetJSON(ctx, prefix+"/plan.json", &plan); err != nil {
		return err
	}
	report, err := artifacts.Download(ctx, store, &plan, prefix, artifacts.Options{Workflow: name, Phase: snapshot.Status.Phase, Output: *output, Include: csvOptions(*include), Runs: csvOptions(*runs), Stage: *stage, Files: files})
	if report.Added > 0 || len(report.Skipped) > 0 {
		if _, writeErr := fmt.Fprintf(out, "Downloaded %d files to %s (%s). Manifest: download.json\n", report.Added, *output, snapshot.Status.Phase); writeErr != nil {
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
