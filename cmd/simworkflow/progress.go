package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"simworkflow/internal/argo"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

var statusRunID = regexp.MustCompile(`^run-[a-f0-9]{32}$`)

func writeRunProgress(ctx context.Context, store *storage.Store, snapshot *argo.Snapshot, out io.Writer) (bool, error) {
	prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
	if !strings.HasPrefix(prefix, "experiments/") || strings.Contains(prefix, "..") {
		return false, nil
	}
	planCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	var plan experiment.Plan
	err := store.GetJSON(planCtx, prefix+"/plan.json", &plan)
	cancel()
	if err != nil {
		_, writeErr := fmt.Fprintf(out, "Run details unavailable: %v\n", err)
		return true, writeErr
	}
	return writePlannedRunProgress(ctx, store, snapshot, &plan, out)
}

func writePlannedRunProgress(ctx context.Context, store *storage.Store, snapshot *argo.Snapshot, plan *experiment.Plan, out io.Writer) (bool, error) {
	// Each row has independent request deadlines. A slow object must not
	// consume the budget of later runs; bound S3 concurrency and retain plan order.
	rows := make([]runProgressRow, len(plan.Runs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(8, len(plan.Runs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				rows[i] = readRunProgress(ctx, store, snapshot, plan.Runs[i])
			}
		}()
	}
	for i, run := range plan.Runs {
		if statusRunID.MatchString(run.ID) {
			jobs <- i
		}
	}
	close(jobs)
	wg.Wait()
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "RUN\tMAP\tSEED\tSTAGE\tSTATE\tSTEP\tSCORE\tUPDATED")
	retry := false
	for _, row := range rows {
		if row.line != "" {
			fmt.Fprint(writer, row.line)
		}
		retry = retry || row.retry
	}
	return retry, writer.Flush()
}

type runProgressRow struct {
	line  string
	retry bool
}

func readProgressJSON(ctx context.Context, store *storage.Store, key string, value any) error {
	requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return store.GetJSON(requestCtx, key, value)
}

func readRunProgress(ctx context.Context, store *storage.Store, snapshot *argo.Snapshot, run experiment.Run) runProgressRow {
	prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
	retry := false
	stage, phase := "-", "Pending"
	var started time.Time
	// Prefer simulation once scheduled. Within a stage, choose the newest
	// Pod so a failed first attempt does not hide the retry's state.
	for _, candidate := range []string{worker.Precompute, worker.Simulation} {
		if node, found := latestStageNode(snapshot, run.ID, candidate); found {
			stage, phase, started = candidate, node.Phase, node.StartedAt
		}
	}
	steps, score, updated := "-", "-", "-"
	stagePrefix := prefix + "/" + run.ID
	if stage == worker.Precompute {
		stagePrefix += "/precompute"
	}
	if stage != "-" {
		var progress worker.Progress
		err := readProgressJSON(ctx, store, stagePrefix+"/progress.json", &progress)
		if phase != "Pending" && err == nil && progress.ExecutionID == snapshot.Metadata.Annotations["simworkflow.io/execution-id"] && progress.RunID == run.ID && progress.Stage == stage && !progress.UpdatedAt.Before(started) {
			if stage == worker.Simulation {
				steps = fmt.Sprint(progress.CompletedSteps)
				if progress.TotalSteps > 0 {
					steps += fmt.Sprintf("/%d (%.0f%%)", progress.TotalSteps, 100*float64(progress.CompletedSteps)/float64(progress.TotalSteps))
				}
			}
			if progress.Score != nil {
				score = fmt.Sprintf("%.6g", *progress.Score)
			}
			age := time.Since(progress.UpdatedAt).Round(time.Second)
			if age < 0 {
				age = 0
			}
			updated = age.String() + " ago"
		} else if err != nil && !storage.IsNotFound(err) {
			updated = "unavailable"
			retry = true
		}
		// Older workers have no progress object. Finished summaries still
		// provide scores, without inventing a completed-step count.
		if phase == "Succeeded" || phase == "Failed" || phase == "Error" {
			var summary worker.Summary
			summaryErr := readProgressJSON(ctx, store, stagePrefix+"/summary.json", &summary)
			if summaryErr != nil && !storage.IsNotFound(summaryErr) {
				retry = true
			}
			if summaryErr == nil && summary.Run.ID == run.ID && summary.ExecutionID == snapshot.Metadata.Annotations["simworkflow.io/execution-id"] && summary.Stage == stage && summary.Score != nil {
				score = fmt.Sprintf("%.6g", *summary.Score)
			}
		}
	}
	if (snapshot.Status.Phase == "Failed" || snapshot.Status.Phase == "Error") && phase == "Pending" {
		phase = "Not run"
	}
	return runProgressRow{line: fmt.Sprintf("%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", run.ID, run.Map.Name, run.Seed, stage, phase, steps, score, updated), retry: retry}
}

// Prefer the highest retry attempt, including Pending retries with no start time.
func latestStageNode(snapshot *argo.Snapshot, runID, stage string) (argo.Node, bool) {
	var latest argo.Node
	latestAttempt, found := -1, false
	for _, node := range snapshot.Status.Nodes {
		if node.Type != "Pod" || node.TemplateName != runID+"-"+stage {
			continue
		}
		attempt := 0
		if i := strings.LastIndex(node.DisplayName, "("); i >= 0 {
			attempt, _ = strconv.Atoi(strings.TrimSuffix(node.DisplayName[i+1:], ")"))
		}
		if !found || attempt > latestAttempt || (attempt == latestAttempt && !node.StartedAt.Before(latest.StartedAt)) {
			latest, latestAttempt, found = node, attempt, true
		}
	}
	return latest, found
}
