package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"simworkflow/internal/argo"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/mapcatalog"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
	"simworkflow/internal/workflow"
)

const defaultHelperImage = "ghcr.io/shima004/simworkflow-worker@sha256:2e204474aa5c348c0d2196cc83adde038170fd9885bb09c2e83b4a4fd31cc027"

func remoteCommand(command string) bool {
	return command == "render" || command == "submit" || command == "status" || command == "results" || command == "download" || command == "list" || command == "stop"
}

func runRemote(args []string, c config.Config, out, errOut io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	command := args[0]
	if command == "list" || command == "stop" {
		return runLifecycle(ctx, args, c, out, errOut)
	}
	if command == "download" {
		return runDownload(ctx, args[1:], c, out, errOut)
	}
	flags := flag.NewFlagSet("experiment "+command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	if command == "status" || command == "results" {
		watch := flags.Bool("watch", false, "watch per-run progress every 5 seconds; interactive in a terminal (status only)")
		var plain bool
		if command == "status" {
			flags.BoolVar(&plain, "plain", false, "use text output instead of the interactive watch screen")
		}
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("specify one workflow name after command options")
		}
		client := argo.New(c)
		if command == "status" && *watch && !plain && interactiveOutput(os.Stdin, out) {
			return watchProgress(ctx, c, flags.Arg(0), os.Stdin, out)
		}
		for {
			snapshot, err := client.Get(ctx, flags.Arg(0))
			if err != nil {
				return err
			}
			if command == "results" {
				prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
				if prefix == "" || !strings.HasPrefix(prefix, "experiments/") || strings.Contains(prefix, "..") {
					return errors.New("workflow has no valid SimWorkflow result prefix")
				}
				store := storage.New(c)
				var plan experiment.Plan
				if err := store.GetJSON(ctx, prefix+"/plan.json", &plan); err != nil {
					return err
				}
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				summaries := make([]worker.Summary, 0, len(plan.Runs))
				for _, run := range plan.Runs {
					var summary worker.Summary
					if err := store.GetJSON(ctx, prefix+"/"+run.ID+"/summary.json", &summary); err != nil {
						if !storage.IsNotFound(err) {
							return fmt.Errorf("result for %s is not available: %w", run.ID, err)
						}
						if err := store.GetJSON(ctx, prefix+"/"+run.ID+"/precompute/summary.json", &summary); err != nil {
							return fmt.Errorf("result for %s is not available: %w", run.ID, err)
						}
					}
					summary.WorkflowPhase = snapshot.Status.Phase
					summaries = append(summaries, summary)
				}
				return encoder.Encode(summaries)
			}
			phase := snapshot.Status.Phase
			if phase == "" {
				phase = "Pending"
			}
			if _, err := fmt.Fprintf(out, "%s: %s\n", snapshot.Metadata.Name, phase); err != nil {
				return err
			}
			retryProgress, err := writeRunProgress(ctx, storage.New(c), snapshot, out)
			if err != nil {
				return err
			}
			if !*watch {
				return nil
			}
			if phase == "Succeeded" && !retryProgress {
				return nil
			}
			if (phase == "Failed" || phase == "Error") && !retryProgress {
				return errors.New("workflow did not succeed; inspect experiment results and Argo logs")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}
	file := flags.String("f", "", "experiment YAML (required)")
	helper := flags.String("helper-image", defaultHelperImage, "override the published SimWorkflow helper image")
	account := flags.String("service-account", "simworkflow", "workflow executor ServiceAccount")
	maxRuns := flags.Int("max-runs", experiment.DefaultMaxRuns, "maximum expanded runs")
	output := flags.String("output", "", "new private YAML file for render; contains temporary signed URLs")
	configStorage := flags.String("config-storage", "s3", "worker configuration: s3 or inline (inline supports older helper images)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *file == "" {
		return errors.New("specify -f FILE; positional arguments are not supported")
	}
	if strings.TrimSpace(*helper) == "" {
		return errors.New("helper-image must not be empty")
	}
	if *configStorage != "inline" && *configStorage != "s3" {
		return errors.New("config-storage must be inline or s3")
	}
	if command == "render" && *output == "" {
		return errors.New("render requires --output FILE; temporary signed URLs are never printed")
	}
	e, err := experiment.ReadFile(*file)
	if err != nil {
		return err
	}
	store := storage.New(c)
	e, err = (&mapcatalog.Catalog{Store: store}).Resolve(ctx, e)
	if err != nil {
		return err
	}
	plan, err := experiment.BuildPlan(e, *maxRuns)
	if err != nil {
		return err
	}
	timeout, _ := time.ParseDuration(plan.Timeout)
	if timeout > 20*time.Hour {
		return errors.New("execution timeout must be at most 20h for signed transfers")
	}
	waves := (plan.TotalRuns-1)/plan.MaxParallelRuns + 1
	if int64(waves) > int64((23*time.Hour)/(timeout+360*time.Second)) {
		return errors.New("estimated experiment duration exceeds signed URL validity; split the experiment")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	executionID := hex.EncodeToString(random[:])
	prefix := "experiments/" + executionID
	configs := make([]workflow.RunConfig, 0, len(plan.Runs))
	downloads := map[string]storage.Transfer{}
	for _, run := range plan.Runs {
		mapTransfer, ok := downloads[run.Map.Location()]
		if !ok {
			mapTransfer, err = store.Download(ctx, run.Map.Location())
			if err != nil {
				return err
			}
			downloads[run.Map.Location()] = mapTransfer
		}
		resultPrefix := prefix + "/" + run.ID
		pair := workflow.RunConfig{}
		for _, stage := range []string{worker.Precompute, worker.Simulation} {
			stagePrefix := resultPrefix
			if stage == worker.Precompute {
				stagePrefix += "/precompute"
			}
			bundle, err := store.Upload(ctx, stagePrefix+"/bundle.tar.gz")
			if err != nil {
				return err
			}
			summary, err := store.Upload(ctx, stagePrefix+"/summary.json")
			if err != nil {
				return err
			}
			progress, err := store.Upload(ctx, stagePrefix+"/progress.json")
			if err != nil {
				return err
			}
			c := worker.Config{ProgressUpload: &progress, Stage: stage, ExecutionID: executionID, Experiment: plan.Experiment.Name, Run: run, TimeoutSeconds: int64(timeout / time.Second), Map: mapTransfer, Bundle: bundle, Summary: summary, ResultPrefix: stagePrefix, Collect: plan.Collect}
			key := resultPrefix + "/precompute/data.zip"
			if stage == worker.Precompute {
				transfer, err := store.Upload(ctx, key)
				if err != nil {
					return err
				}
				c.PrecomputeUpload = &transfer
				pair.Precompute = c
			} else {
				transfer, err := store.SignDownload(ctx, store.Bucket, key)
				if err != nil {
					return err
				}
				c.PrecomputeDownload = &transfer
				pair.Simulation = c
			}
			if *configStorage == "s3" {
				configKey := prefix + "/configs/" + run.ID + "/" + stage + ".json"
				if err := store.PutJSON(ctx, configKey, c); err != nil {
					return err
				}
				transfer, err := store.SignDownload(ctx, store.Bucket, configKey)
				if err != nil {
					return err
				}
				if stage == worker.Precompute {
					pair.PrecomputeURL = transfer.URL
				} else {
					pair.SimulationURL = transfer.URL
				}
			}
		}
		configs = append(configs, pair)
	}
	wf, err := workflow.Build(plan, configs, workflow.Options{Namespace: c.Argo.Namespace, HelperImage: *helper, ServiceAccount: *account, ExecutionID: executionID, ResultPrefix: prefix})
	if err != nil {
		return err
	}
	// Leave room for node status under Argo's 1MiB Workflow limit. Reject before
	// submitting an oversized inline spec rather than creating a doomed run.
	encodedWorkflow, err := json.Marshal(wf)
	if err != nil {
		return err
	}
	if len(encodedWorkflow) > 900*1024 {
		if *configStorage == "s3" {
			return errors.New("workflow definition is too large even with S3 configuration; split the experiment")
		}
		return errors.New("workflow definition is too large; use --config-storage s3 with a compatible helper image or split the experiment")
	}
	if command == "render" {
		data, err := yaml.Marshal(wf)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := store.PutJSON(ctx, prefix+"/plan.json", plan); err != nil {
			os.Remove(*output)
			return err
		}
		_, err = fmt.Fprintf(out, "Rendered %d runs to %s (private file; signed URLs expire after 24h).\n", plan.TotalRuns, *output)
		return err
	}
	if err := store.PutJSON(ctx, prefix+"/plan.json", plan); err != nil {
		return err
	}
	snapshot, err := argo.New(c).Submit(ctx, wf)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Submitted %s (%d runs). Results: s3://%s/%s/\n", snapshot.Metadata.Name, plan.TotalRuns, c.S3.Bucket, prefix)
	return err
}
