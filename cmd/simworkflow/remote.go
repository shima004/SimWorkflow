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
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
	"simworkflow/internal/workflow"
)

func remoteCommand(command string) bool {
	return command == "render" || command == "submit" || command == "status" || command == "results"
}

func runRemote(args []string, c config.Config, out, errOut io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	command := args[0]
	flags := flag.NewFlagSet("experiment "+command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	if command == "status" || command == "results" {
		watch := flags.Bool("watch", false, "wait until workflow finishes (status only)")
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
						return fmt.Errorf("result for %s is not available: %w", run.ID, err)
					}
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
			if !*watch {
				return nil
			}
			if phase == "Succeeded" {
				return nil
			}
			if phase == "Failed" || phase == "Error" {
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
	helper := flags.String("helper-image", "", "published SimWorkflow helper image (required)")
	account := flags.String("service-account", "simworkflow", "workflow executor ServiceAccount")
	maxRuns := flags.Int("max-runs", experiment.DefaultMaxRuns, "maximum expanded runs")
	output := flags.String("output", "", "new private YAML file for render; contains temporary signed URLs")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *file == "" || *helper == "" {
		return errors.New("specify -f FILE and --helper-image IMAGE")
	}
	if command == "render" && *output == "" {
		return errors.New("render requires --output FILE; temporary signed URLs are never printed")
	}
	e, err := experiment.ReadFile(*file)
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
	if int64(waves) > int64((23*time.Hour)/timeout) {
		return errors.New("estimated experiment duration exceeds signed URL validity; split the experiment")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	executionID := hex.EncodeToString(random[:])
	prefix := "experiments/" + executionID
	store := storage.New(c)
	configs := make([]worker.Config, 0, len(plan.Runs))
	downloads := map[string]storage.Transfer{}
	for _, run := range plan.Runs {
		mapTransfer, ok := downloads[run.Map.Source]
		if !ok {
			mapTransfer, err = store.Download(ctx, run.Map.Source)
			if err != nil {
				return err
			}
			downloads[run.Map.Source] = mapTransfer
		}
		resultPrefix := prefix + "/" + run.ID
		bundle, err := store.Upload(ctx, resultPrefix+"/bundle.tar.gz")
		if err != nil {
			return err
		}
		summary, err := store.Upload(ctx, resultPrefix+"/summary.json")
		if err != nil {
			return err
		}
		configs = append(configs, worker.Config{ExecutionID: executionID, Experiment: plan.Experiment.Name, Run: run, TimeoutSeconds: int64(timeout / time.Second), Map: mapTransfer, Bundle: bundle, Summary: summary, ResultPrefix: resultPrefix, Collect: plan.Collect})
	}
	wf, err := workflow.Build(plan, configs, workflow.Options{Namespace: c.Argo.Namespace, HelperImage: *helper, ServiceAccount: *account, ExecutionID: executionID, ResultPrefix: prefix})
	if err != nil {
		return err
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
