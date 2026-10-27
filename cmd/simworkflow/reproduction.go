package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"simworkflow/internal/argo"
	"simworkflow/internal/artifacts"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/storage"
)

func runReproduction(ctx context.Context, args []string, c config.Config, out, errOut io.Writer) error {
	if args[0] == "export" {
		flags := flag.NewFlagSet("experiment export", flag.ContinueOnError)
		flags.SetOutput(errOut)
		output := flags.String("output", "", "new ZIP file (default: WORKFLOW.zip)")
		logs := flags.Bool("include-logs", false, "also archive stored simulator and agent logs")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("specify one workflow name after export options")
		}
		snapshot, err := argo.New(c).Get(ctx, flags.Arg(0))
		if err != nil {
			return err
		}
		prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
		if !artifacts.ValidPrefix(prefix) {
			return errors.New("workflow has no valid SimWorkflow result prefix")
		}
		if *output == "" {
			*output = flags.Arg(0) + ".zip"
		}
		store := storage.New(c)
		var plan experiment.Plan
		if err := store.GetJSON(ctx, prefix+"/plan.json", &plan); err != nil {
			return err
		}
		var meta reproduction.Metadata
		err = store.GetJSON(ctx, prefix+"/reproduction.json", &meta)
		if err != nil && !storage.IsNotFound(err) {
			return err
		}
		helper := ""
		configStorage := "inline"
		for _, template := range snapshot.Spec.Templates {
			for _, container := range template.InitContainers {
				if container.Name == "prepare-experiment" {
					for _, env := range container.Env {
						if env.Name == "SIMWORKFLOW_RUN_URL" {
							configStorage = "s3"
						}
						if env.Name == "SIMWORKFLOW_CONFIG_BUNDLE_URL" {
							configStorage = "s3-bundle"
						}
					}
					if helper != "" && helper != container.Image {
						return errors.New("workflow uses inconsistent helper images")
					}
					helper = container.Image
				}
			}
		}
		if meta.Schema != "" && helper != "" && helper != meta.HelperImage {
			return errors.New("saved helper differs from actual workflow")
		}
		err = reproduction.Export(ctx, c, &plan, meta, reproduction.ExportOptions{Workflow: flags.Arg(0), Phase: snapshot.Status.Phase, Prefix: prefix, ExecutionID: snapshot.Metadata.Annotations["simworkflow.io/execution-id"], Helper: helper, ConfigStorage: configStorage, Output: *output, CLIVersion: version, IncludeLogs: *logs}, errOut)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Exported reproduction bundle to %s.\n", *output)
		return err
	}
	flags := flag.NewFlagSet("experiment reproduce", flag.ContinueOnError)
	flags.SetOutput(errOut)
	bundle := flags.String("bundle", "", "reproduction ZIP (required)")
	name := flags.String("name", "", "new experiment name (required)")
	account := flags.String("service-account", "simworkflow", "workflow executor ServiceAccount")
	repository := flags.String("image-repository", "simworkflow/reproduced", "repository path under REGISTRY_URL for imported images")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *bundle == "" || *name == "" {
		return errors.New("specify --bundle ZIP and --name NAME")
	}
	archive, err := reproduction.OpenContext(ctx, *bundle)
	if err != nil {
		return err
	}
	defer archive.Close()
	plan, meta, mapping, err := archive.ImportTo(ctx, c, *name, *repository, errOut)
	if err != nil {
		return err
	}
	meta.CLIVersion, meta.CLICommit = version, commit
	meta.Origin = &reproduction.Origin{Workflow: archive.Manifest.Workflow, ManifestSHA256: reproduction.ManifestSHA256(archive.Manifest), Runs: mapping}
	return submitPlan(ctx, c, plan, meta.HelperImage, *account, meta.ConfigStorage, "submit", "", meta, out)
}
