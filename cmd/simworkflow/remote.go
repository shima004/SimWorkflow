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
	"maps"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"go.yaml.in/yaml/v3"
	"simworkflow/internal/argo"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/mapcatalog"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
	"simworkflow/internal/workflow"
)

var prepareReproduction = reproduction.Capture

// Release builds replace this with the tested helper digest from the same source.
var defaultHelperImage = "ghcr.io/shima004/simworkflow-worker@sha256:2e204474aa5c348c0d2196cc83adde038170fd9885bb09c2e83b4a4fd31cc027"

func validHelperImage(image string) bool {
	_, err := name.ParseReference(image)
	return err == nil && strings.LastIndex(image, ":") > strings.LastIndex(image, "/")
}

func remoteCommand(command string) bool {
	return command == "render" || command == "submit" || command == "status" || command == "results" || command == "download" || command == "list" || command == "stop" || command == "export" || command == "reproduce"
}

func runRemote(args []string, c config.Config, out, errOut io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	command := args[0]
	if command == "export" || command == "reproduce" {
		return runReproduction(ctx, args, c, out, errOut)
	}
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
					// Older helpers execute flattened modules correctly but do not
					// retain newly added selection labels. The saved plan is the
					// canonical source for those labels, independent of helper age.
					if len(run.Configuration.Groups) > 0 {
						if summary.Run.ID != run.ID || summary.Run.Configuration.ID != run.Configuration.ID {
							return fmt.Errorf("result for %s does not match the saved plan", run.ID)
						}
						summary.Run.Configuration.Groups = maps.Clone(run.Configuration.Groups)
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
			elapsed := workflowElapsed(snapshot.Status.StartedAt, snapshot.Status.FinishedAt, time.Now(), phase)
			if _, err := fmt.Fprintf(out, "%s: %s  Workflow Elapsed: %s\n", snapshot.Metadata.Name, phase, elapsed); err != nil {
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
	helperDefault := c.HelperImage
	if helperDefault == "" {
		helperDefault = defaultHelperImage
	}
	helper := flags.String("helper-image", helperDefault, "helper image (SIMWORKFLOW_HELPER_IMAGE / workflow.helperImage / bundled default)")
	if !validHelperImage(helperDefault) {
		// Help must not echo credentials accidentally entered as an image URL.
		flags.Lookup("helper-image").DefValue = "<invalid configured image>"
	}
	account := flags.String("service-account", "simworkflow", "workflow executor ServiceAccount")
	output := flags.String("output", "", "new private YAML file for render; contains temporary signed URLs")
	configStorage := flags.String("config-storage", "auto", "worker configuration: auto, s3-sequence, s3-bundle, s3 or inline (auto selects a compatible format)")
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
	if !validHelperImage(*helper) {
		return errors.New("helper-image must be a container image reference with a tag or digest, without a URL scheme or credentials")
	}
	if *configStorage != "inline" && *configStorage != "s3" && *configStorage != "s3-bundle" && *configStorage != "s3-sequence" && *configStorage != "auto" {
		return errors.New("config-storage must be auto, s3-sequence, s3-bundle, s3 or inline")
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
	e, meta, err := prepareReproduction(ctx, c, e, *helper, version, commit)
	if err != nil {
		return err
	}
	*helper = meta.HelperImage
	if *configStorage == "auto" && !meta.HelperConfigBundle {
		fmt.Fprintln(errOut, "Helper has no configuration-bundle capability; using individual S3 configurations. An updated helper enables stage bundles.")
	}
	plan, err := experiment.BuildPlan(e)
	if err != nil {
		return err
	}
	return submitPlan(ctx, c, plan, *helper, *account, *configStorage, command, *output, meta, out)
}

func submitPlan(ctx context.Context, c config.Config, plan *experiment.Plan, helper, account, configStorage, command, output string, meta reproduction.Metadata, out io.Writer) error {
	if configStorage == "auto" {
		configStorage = "s3"
		if meta.HelperConfigBundle {
			configStorage = "s3-bundle"
		}
		if meta.HelperConfigSequence {
			configStorage = "s3-sequence"
		}
	}
	if configStorage == "s3-sequence" && (!meta.HelperConfigSequence || !meta.HelperConfigBundle) {
		return errors.New("helper does not advertise sequence configuration support; use an updated helper or --config-storage auto")
	}
	if configStorage == "s3-bundle" && !meta.HelperConfigBundle {
		return errors.New("helper does not advertise configuration bundle v1 support; use an updated helper image or --config-storage s3")
	}
	meta.ConfigStorage = configStorage
	if err := reproduction.ValidatePlan(plan); err != nil {
		return err
	}
	store := storage.New(c)
	var err error
	timeout, _ := time.ParseDuration(plan.Timeout)
	if timeout > 20*time.Hour {
		return errors.New("execution timeout must be at most 20h for signed transfers")
	}
	waves := (plan.TotalRuns-1)/plan.MaxParallelRuns + 1
	if int64(waves) > int64(storage.SignedTransferBudget/(timeout+360*time.Second)) {
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
			if configStorage == "s3" {
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
	options := workflow.Options{Namespace: c.Argo.Namespace, HelperImage: helper, ServiceAccount: account, ExecutionID: executionID, ResultPrefix: prefix}
	options.Sequences = configStorage == "s3-sequence"
	if configStorage == "s3-bundle" || configStorage == "s3-sequence" {
		for _, stage := range []string{worker.Precompute, worker.Simulation} {
			stageConfigs := make([]worker.Config, len(configs))
			for i, pair := range configs {
				stageConfigs[i] = pair.Precompute
				if stage == worker.Simulation {
					stageConfigs[i] = pair.Simulation
				}
			}
			create := worker.CreateConfigBundle
			if options.Sequences {
				create = worker.CreateSequenceConfigBundle
			}
			path, sha, err := create(ctx, executionID, stage, stageConfigs)
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				os.Remove(path)
				return err
			}
			key := prefix + "/configs/" + stage + ".zip"
			err = store.PutImmutable(ctx, key, f, "application/zip", map[string]string{"sha256": sha})
			f.Close()
			os.Remove(path)
			if err != nil {
				return err
			}
			transfer, err := store.SignDownload(ctx, store.Bucket, key)
			if err != nil {
				return err
			}
			ref := &worker.ConfigBundleReference{URL: transfer.URL, SHA256: sha}
			if stage == worker.Precompute {
				options.PrecomputeBundle = ref
			} else {
				options.SimulationBundle = ref
			}
		}
	}
	wf, err := workflow.Build(plan, configs, options)
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
		if configStorage == "s3" || configStorage == "s3-bundle" || configStorage == "s3-sequence" {
			return fmt.Errorf("workflow definition is too large even with S3 configuration (%d bytes, limit %d bytes, %d runs); reduce runs or distinct image/resource configurations; node-status offloading does not shrink the definition", len(encodedWorkflow), 900*1024, plan.TotalRuns)
		}
		return errors.New("workflow definition is too large; use --config-storage s3 with a compatible helper image or split the experiment")
	}
	if err := store.PutJSON(ctx, prefix+"/plan.json", plan); err != nil {
		return err
	}
	meta.PlanSHA256 = reproduction.PlanSHA256(plan)
	if err := store.PutJSON(ctx, prefix+"/reproduction.json", meta); err != nil {
		return err
	}
	if command == "render" {
		data, err := yaml.Marshal(wf)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
		_, err = fmt.Fprintf(out, "Rendered %d runs to %s (private file; signed URLs expire after 168h).\n", plan.TotalRuns, output)
		return err
	}
	snapshot, err := argo.New(c).Submit(ctx, wf)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Submitted %s (%d runs). Results: s3://%s/%s/\n", snapshot.Metadata.Name, plan.TotalRuns, c.S3.Bucket, prefix)
	return err
}
