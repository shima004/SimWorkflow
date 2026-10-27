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

func workflowElapsed(started, finished, now time.Time, phase string) string {
	if started.IsZero() {
		return "-"
	}
	if !finished.IsZero() {
		now = finished
	} else if phase == "Succeeded" || phase == "Failed" || phase == "Error" {
		return "-"
	}
	return max(time.Duration(0), now.Sub(started)).Truncate(time.Second).String()
}

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
	return writeProgressRows(out, readPlannedRunProgress(ctx, store, snapshot, plan), time.Now())
}

func readPlannedRunProgress(ctx context.Context, store *storage.Store, snapshot *argo.Snapshot, plan *experiment.Plan) []runProgressRow {
	attachSequenceNodes(snapshot, plan)
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
	return rows
}

func writeProgressRows(out io.Writer, rows []runProgressRow, now time.Time) (bool, error) {
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "RUN\tMAP\tSEED\tSTAGE\tSTATE\tELAPSED\tSTEP\tSCORE\tUPDATED")
	retry := false
	for _, row := range rows {
		if row.runID != "" {
			elapsed := workflowElapsed(row.startedAt, row.finishedAt, now, row.phase)
			fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", row.runID, row.mapName, row.seed, row.stage, row.phase, elapsed, row.steps, row.score, row.updated)
		}
		retry = retry || row.retry
	}
	return retry, writer.Flush()
}

type runProgressRow struct {
	runID, mapName, stage, phase, steps, score, updated string
	seed                                                int64
	startedAt, finishedAt                               time.Time
	retry                                               bool
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
	var started, finished time.Time
	// Prefer simulation once scheduled. Within a stage, choose the newest
	// Pod so a failed first attempt does not hide the retry's state.
	for _, candidate := range []string{worker.Precompute, worker.Simulation} {
		if node, found := latestStageNode(snapshot, run.ID, candidate); found {
			stage, phase, started, finished = candidate, node.Phase, node.StartedAt, node.FinishedAt
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
	return runProgressRow{runID: run.ID, mapName: run.Map.Name, seed: run.Seed, stage: stage, phase: phase, steps: steps, score: score, updated: updated, startedAt: started, finishedAt: finished, retry: retry}
}

// Prefer the highest retry attempt, including Pending retries with no start time.
func latestStageNode(snapshot *argo.Snapshot, runID, stage string) (argo.Node, bool) {
	var latest argo.Node
	latestAttempt, found := -1, false
	consider := func(node argo.Node) {
		if node.Type != "Pod" || !matchesStageNode(snapshot, node, runID, stage) {
			return
		}
		attempt := 0
		if i := strings.LastIndex(node.DisplayName, "("); i >= 0 {
			attempt, _ = strconv.Atoi(strings.TrimSuffix(node.DisplayName[i+1:], ")"))
		}
		if !found || attempt > latestAttempt || (attempt == latestAttempt && !node.StartedAt.Before(latest.StartedAt)) {
			latest, latestAttempt, found = node, attempt, true
		}
	}
	if snapshot.RunStageNodes != nil {
		for _, node := range snapshot.RunStageNodes[runID+"-"+stage] {
			consider(node)
		}
	} else {
		for _, node := range snapshot.Status.Nodes {
			consider(node)
		}
	}
	return latest, found
}

func matchesStageNode(snapshot *argo.Snapshot, node argo.Node, runID, stage string) bool {
	if node.TemplateName == runID+"-"+stage {
		return true // Workflows submitted before shared templates were introduced.
	}
	if snapshot.Metadata.Name == "" || !strings.HasPrefix(node.TemplateName, "shared-"+stage+"-") {
		return false
	}
	index := 0
	if stage == worker.Simulation {
		index = 1
	} else if stage != worker.Precompute {
		return false
	}
	root := runID
	if snapshot.RunNodeNames != nil {
		root = snapshot.RunNodeNames[runID]
		if root == "" {
			return false
		}
	}
	expected := fmt.Sprintf("%s.%s[%d].%s", snapshot.Metadata.Name, root, index, stage)
	if node.Name == expected {
		return true
	}
	// Retry Pod names append the numeric attempt to the step's node name.
	suffix, ok := strings.CutPrefix(node.Name, expected)
	if !ok {
		return false
	}
	if !strings.HasPrefix(suffix, "(") || !strings.HasSuffix(suffix, ")") {
		return false
	}
	_, err := strconv.ParseUint(suffix[1:len(suffix)-1], 10, 32)
	return err == nil
}

// Derive loop positions from the exact saved plan before concurrent row reads.
// Index Pods once so a large sequence does not require a full node scan per row.
func attachSequenceNodes(snapshot *argo.Snapshot, plan *experiment.Plan) {
	snapshot.RunNodeNames = nil
	snapshot.RunStageNodes = nil
	if snapshot.Metadata.Annotations["simworkflow.io/run-sequences"] != "v1" {
		return
	}
	snapshot.RunNodeNames = map[string]string{}
	snapshot.RunStageNodes = map[string][]argo.Node{}
	paths := map[string]string{}
	for _, group := range experiment.RunSequences(plan.Runs) {
		for index, id := range group.Runs {
			root := fmt.Sprintf("%s(%d:%d)", group.Name, index, index)
			snapshot.RunNodeNames[id] = root
			for i, stage := range []string{worker.Precompute, worker.Simulation} {
				paths[fmt.Sprintf("%s.%s[%d].%s", snapshot.Metadata.Name, root, i, stage)] = id + "-" + stage
			}
		}
	}
	for _, node := range snapshot.Status.Nodes {
		if node.Type != "Pod" {
			continue
		}
		path := node.Name
		if i := strings.LastIndex(path, "("); i >= 0 && strings.HasSuffix(path, ")") {
			if _, err := strconv.ParseUint(path[i+1:len(path)-1], 10, 32); err == nil {
				path = path[:i]
			}
		}
		if key, ok := paths[path]; ok {
			snapshot.RunStageNodes[key] = append(snapshot.RunStageNodes[key], node)
		}
	}
}
