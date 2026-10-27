package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"simworkflow/internal/argo"
	"simworkflow/internal/artifacts"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/reproduction"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

type retryRecord struct {
	Workflow    string                   `json:"workflow"`
	Prefix      string                   `json:"prefix"`
	Origin      reproduction.RetryOrigin `json:"origin"`
	SubmittedAt time.Time                `json:"submittedAt"`
}

type savedExecution struct {
	snapshot    *argo.Snapshot
	plan        *experiment.Plan
	meta        reproduction.Metadata
	prefix      string
	submittedAt time.Time
	runOutcomes map[string]string
}

func completedPhase(phase string) bool {
	return phase == "Succeeded" || phase == "Failed" || phase == "Error"
}

func loadSavedExecution(ctx context.Context, c config.Config, name string) (*savedExecution, error) {
	snapshot, err := argo.New(c).Get(ctx, name)
	if err != nil {
		return nil, err
	}
	prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
	if !artifacts.ValidPrefix(prefix) {
		return nil, errors.New("workflow has no valid SimWorkflow result prefix")
	}
	store := storage.New(c)
	var plan experiment.Plan
	if err := store.GetJSON(ctx, prefix+"/plan.json", &plan); err != nil {
		return nil, err
	}
	if err := reproduction.ValidatePlan(&plan); err != nil {
		return nil, err
	}
	var meta reproduction.Metadata
	if err := store.GetJSON(ctx, prefix+"/reproduction.json", &meta); err != nil && !storage.IsNotFound(err) {
		return nil, err
	}
	if meta.PlanSHA256 != "" && meta.PlanSHA256 != reproduction.PlanSHA256(&plan) {
		return nil, errors.New("saved plan does not match reproduction metadata")
	}
	if snapshot.Metadata.Name != name {
		return nil, errors.New("Argo returned a different workflow")
	}
	if snapshot.Metadata.Namespace != c.Argo.Namespace {
		return nil, errors.New("workflow does not belong to the configured namespace")
	}
	if id := snapshot.Metadata.Annotations["simworkflow.io/execution-id"]; id != "" && prefix != "experiments/"+id {
		return nil, errors.New("workflow execution ID does not match its result prefix")
	}
	attachSequenceNodes(snapshot, &plan)
	return &savedExecution{snapshot: snapshot, plan: &plan, meta: meta, prefix: prefix, submittedAt: snapshot.Metadata.CreatedAt, runOutcomes: indexRunOutcomes(snapshot, &plan)}, nil
}

// Template evaluation can fail before Argo creates a Pod. Index the run and
// stage wrappers once, so these errors are retried without scanning all nodes
// separately for every member of a large sequence.
func indexRunOutcomes(snapshot *argo.Snapshot, plan *experiment.Plan) map[string]string {
	nodes := map[string]argo.Node{}
	for _, node := range snapshot.Status.Nodes {
		nodes[node.Name] = node
	}
	outcomes := map[string]string{}
	for _, run := range plan.Runs {
		root := run.ID
		if snapshot.RunNodeNames != nil {
			root = snapshot.RunNodeNames[run.ID]
		}
		path := snapshot.Metadata.Name + "." + root
		if node, ok := nodes[path]; ok && completedPhase(node.Phase) {
			outcomes[run.ID] = node.Phase
			continue
		}
		for i, stage := range []string{worker.Precompute, worker.Simulation} {
			if node, ok := nodes[fmt.Sprintf("%s[%d].%s", path, i, stage)]; ok && (node.Phase == "Failed" || node.Phase == "Error") {
				outcomes[run.ID] = node.Phase
			}
		}
	}
	return outcomes
}

// A successful precompute alone is not a successful simulation. Prefer the final
// simulation Pod (including its newest retry), otherwise the precompute Pod.
func executionRunPhase(e *savedExecution, runID string) string {
	if node, ok := latestStageNode(e.snapshot, runID, worker.Simulation); ok {
		return node.Phase
	}
	if node, ok := latestStageNode(e.snapshot, runID, worker.Precompute); ok {
		if node.Phase == "Failed" || node.Phase == "Error" {
			return node.Phase
		}
	}
	if phase := e.runOutcomes[runID]; phase != "" {
		return phase
	}
	if e.snapshot.Status.Phase == "Succeeded" {
		return "Succeeded"
	}
	return "Unfinished"
}

func subsetPlan(source *experiment.Plan, runs []experiment.Run) *experiment.Plan {
	p := *source
	p.Runs = slices.Clone(runs)
	p.TotalRuns = len(runs)
	p.Agents = nil
	configs := map[string]map[string]bool{}
	counts := map[string]int{}
	for _, run := range runs {
		name := run.Configuration.Agent
		if configs[name] == nil {
			configs[name] = map[string]bool{}
		}
		configs[name][run.Configuration.ID] = true
		counts[name]++
	}
	for _, agent := range source.Agents {
		if counts[agent.Name] > 0 {
			p.Agents = append(p.Agents, experiment.AgentSummary{Name: agent.Name, Configurations: len(configs[agent.Name]), Runs: counts[agent.Name]})
		}
	}
	return &p
}

func validRetryOrigin(origin *reproduction.RetryOrigin) bool {
	return origin != nil && artifacts.ValidPrefix(origin.RootPrefix) && artifacts.ValidPrefix(origin.ParentPrefix) && validWorkflowName(origin.RootWorkflow) && validWorkflowName(origin.ParentWorkflow)
}

func validWorkflowName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\?#")
}

func runRetry(ctx context.Context, args []string, c config.Config, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("experiment retry", flag.ContinueOnError)
	flags.SetOutput(errOut)
	dryRun := flags.Bool("dry-run", false, "show selected runs without writing or submitting")
	unfinished := flags.Bool("include-unfinished", false, "also retry runs that did not complete, including skipped runs")
	parallel := flags.Int("max-parallel-runs", 0, "override parallel runs (default: original value)")
	timeout := flags.String("timeout", "", "override timeout per run (default: original value)")
	account := flags.String("service-account", "", "override executor ServiceAccount (default: original value)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("specify one workflow name after retry options")
	}
	if *parallel < 0 {
		return errors.New("max-parallel-runs must be positive")
	}
	var explicitParallel bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "max-parallel-runs" {
			explicitParallel = true
		}
	})
	if explicitParallel && *parallel == 0 {
		return errors.New("max-parallel-runs must be positive")
	}
	source, err := loadSavedExecution(ctx, c, flags.Arg(0))
	if err != nil {
		return err
	}
	if !completedPhase(source.snapshot.Status.Phase) {
		return errors.New("only completed workflows can be retried; wait for completion or stop the workflow first")
	}
	var selected []experiment.Run
	for _, run := range source.plan.Runs {
		phase := executionRunPhase(source, run.ID)
		if phase == "Failed" || phase == "Error" || (*unfinished && phase != "Succeeded") {
			selected = append(selected, run)
		}
	}
	if len(selected) == 0 {
		_, err := fmt.Fprintln(out, "No matching failed runs; nothing submitted.")
		return err
	}
	plan := subsetPlan(source.plan, selected)
	if *parallel != 0 {
		plan.MaxParallelRuns = *parallel
	}
	if *timeout != "" {
		plan.Timeout = *timeout
	}
	if err := reproduction.ValidatePlan(plan); err != nil {
		return err
	}
	if *dryRun {
		return printPlan(out, plan)
	}
	meta := source.meta
	if meta.Schema != reproduction.Schema || meta.PlanSHA256 == "" || !reproduction.Pinned(meta.HelperImage) {
		return errors.New("retry requires saved reproduction metadata with a pinned helper and verified plan")
	}
	for _, run := range selected {
		if !reproduction.Pinned(run.Configuration.Image) || !reproduction.Pinned(run.Simulator.Image) || !experiment.ValidSHA256(run.Map.ExpectedSHA256()) {
			return errors.New("retry requires pinned agent/simulator images and map checksums; resubmit legacy experiments from YAML")
		}
	}
	origin := &reproduction.RetryOrigin{RootWorkflow: source.snapshot.Metadata.Name, RootPrefix: source.prefix, ParentWorkflow: source.snapshot.Metadata.Name, ParentPrefix: source.prefix}
	if meta.Retry != nil {
		if !validRetryOrigin(meta.Retry) {
			return errors.New("invalid saved retry origin")
		}
		origin.RootWorkflow, origin.RootPrefix = meta.Retry.RootWorkflow, meta.Retry.RootPrefix
	}
	meta.Retry = origin
	// A subset can be non-Cartesian, so the original YAML must not claim to
	// describe this retry. The exact saved plan remains the reproduction input.
	meta.Definition = nil
	meta.CLIVersion, meta.CLICommit = version, commit
	if *account == "" {
		*account = source.snapshot.Spec.ServiceAccountName
	}
	if *account == "" {
		*account = "simworkflow"
	}
	_, err = fmt.Fprintf(out, "Retrying %d of %d runs from %s (precompute and simulation).\n", len(selected), source.plan.TotalRuns, source.snapshot.Metadata.Name)
	if err != nil {
		return err
	}
	mode := meta.ConfigStorage
	if mode == "" {
		mode = "auto"
	}
	return submitPlan(ctx, c, plan, meta.HelperImage, *account, mode, "submit", "", meta, out)
}

// The append-only index is stored with the root experiment, independently of
// mutable tags and CLI working directories. Each submission owns a unique key.
func retryFamily(ctx context.Context, c config.Config, requested *savedExecution) ([]*savedExecution, error) {
	root := requested
	if requested.meta.Retry != nil {
		if !validRetryOrigin(requested.meta.Retry) {
			return nil, errors.New("invalid saved retry origin")
		}
		var err error
		root, err = loadSavedExecution(ctx, c, requested.meta.Retry.RootWorkflow)
		if err != nil {
			return nil, fmt.Errorf("load original experiment: %w", err)
		}
		if root.prefix != requested.meta.Retry.RootPrefix || root.meta.Retry != nil {
			return nil, errors.New("retry root does not match original experiment")
		}
	}
	store := storage.New(c)
	keys, err := store.ListKeys(ctx, root.prefix+"/retries/", ".json")
	if err != nil {
		return nil, err
	}
	family := []*savedExecution{root}
	seen := map[string]bool{root.prefix: true}
	for _, key := range keys {
		var record retryRecord
		if err := store.GetJSON(ctx, key, &record); err != nil {
			return nil, err
		}
		if !validRetryOrigin(&record.Origin) || record.Origin.RootPrefix != root.prefix || record.Origin.RootWorkflow != root.snapshot.Metadata.Name || !artifacts.ValidPrefix(record.Prefix) || record.SubmittedAt.IsZero() || key != root.prefix+"/retries/"+strings.TrimPrefix(record.Prefix, "experiments/")+".json" || !validWorkflowName(record.Workflow) || seen[record.Prefix] {
			return nil, errors.New("invalid retry index")
		}
		e, err := loadSavedExecution(ctx, c, record.Workflow)
		if err != nil {
			return nil, fmt.Errorf("load retry %s (retain related Argo workflows for combined downloads): %w", record.Workflow, err)
		}
		if e.prefix != record.Prefix || e.meta.Retry == nil || *e.meta.Retry != record.Origin {
			return nil, errors.New("retry index does not match saved execution")
		}
		e.submittedAt = record.SubmittedAt
		family = append(family, e)
		seen[e.prefix] = true
	}
	// Include the explicitly named retry even if its post-submit index write failed.
	if !seen[requested.prefix] {
		family = append(family, requested)
		seen[requested.prefix] = true
	}
	// An explicitly named descendant can recover its parent chain even when a
	// parent's post-submit index write failed. Never silently omit that parent.
	for i := 1; i < len(family); i++ {
		origin := family[i].meta.Retry
		if !validRetryOrigin(origin) {
			return nil, errors.New("invalid saved retry origin")
		}
		if !seen[origin.ParentPrefix] {
			parent, err := loadSavedExecution(ctx, c, origin.ParentWorkflow)
			if err != nil {
				return nil, fmt.Errorf("load retry parent: %w", err)
			}
			if parent.prefix != origin.ParentPrefix {
				return nil, errors.New("retry parent prefix does not match its workflow")
			}
			family = append(family, parent)
			seen[parent.prefix] = true
		}
	}
	slices.SortFunc(family[1:], func(a, b *savedExecution) int {
		if cmp := a.submittedAt.Compare(b.submittedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.prefix, b.prefix)
	})
	known := map[string]bool{}
	byPrefix := map[string]*savedExecution{}
	for _, r := range root.plan.Runs {
		known[r.ID] = true
	}
	for _, e := range family {
		byPrefix[e.prefix] = e
	}
	for _, e := range family[1:] {
		origin := e.meta.Retry
		if !validRetryOrigin(origin) || origin.RootPrefix != root.prefix || origin.RootWorkflow != root.snapshot.Metadata.Name {
			return nil, errors.New("retry does not belong to the original experiment")
		}
		parent := byPrefix[origin.ParentPrefix]
		if parent == nil || parent == e || parent.snapshot.Metadata.Name != origin.ParentWorkflow {
			return nil, errors.New("retry parent is missing or inconsistent")
		}
		parentRuns := map[string]bool{}
		for _, r := range parent.plan.Runs {
			parentRuns[r.ID] = true
		}
		for _, r := range e.plan.Runs {
			if !known[r.ID] || !parentRuns[r.ID] {
				return nil, errors.New("retry contains a run outside the original plan")
			}
		}
		// A malformed index must not create cycles in the retry ancestry.
		visited := map[string]bool{e.prefix: true}
		for ancestor := parent; ancestor != root; {
			if visited[ancestor.prefix] || ancestor.meta.Retry == nil {
				return nil, errors.New("invalid retry ancestry")
			}
			visited[ancestor.prefix] = true
			ancestor = byPrefix[ancestor.meta.Retry.ParentPrefix]
			if ancestor == nil {
				return nil, errors.New("retry ancestor is missing")
			}
		}
	}
	return family, nil
}

func combinedAttempts(family []*savedExecution) map[string]artifacts.Attempt {
	selected := map[string]artifacts.Attempt{}
	for _, e := range family {
		for _, run := range e.plan.Runs {
			phase := executionRunPhase(e, run.ID)
			old, exists := selected[run.ID]
			// Family order is oldest first. Retain a successful attempt when a
			// newer attempt failed or is still running; newer successes replace it.
			if exists && old.Phase == "Succeeded" && phase != "Succeeded" {
				continue
			}
			selected[run.ID] = artifacts.Attempt{Workflow: e.snapshot.Metadata.Name, ResultPrefix: e.prefix, ExecutionID: strings.TrimPrefix(e.prefix, "experiments/"), Phase: phase}
		}
	}
	return selected
}

func combinedPhase(attempts map[string]artifacts.Attempt) string {
	phase := "Succeeded"
	for _, a := range attempts {
		if !completedPhase(a.Phase) {
			return "Incomplete"
		}
		if a.Phase != "Succeeded" {
			phase = "Failed"
		}
	}
	return phase
}

func runCombinedResults(ctx context.Context, c config.Config, name string, out io.Writer) error {
	requested, err := loadSavedExecution(ctx, c, name)
	if err != nil {
		return err
	}
	family, err := retryFamily(ctx, c, requested)
	if err != nil {
		return err
	}
	selected := combinedAttempts(family)
	phase := combinedPhase(selected)
	type result struct {
		worker.Summary
		SourceWorkflow    string `json:"sourceWorkflow"`
		SourceExecutionID string `json:"sourceExecutionId"`
		AttemptPhase      string `json:"attemptPhase"`
	}
	results := make([]result, 0, len(family[0].plan.Runs))
	store := storage.New(c)
	for _, run := range family[0].plan.Runs {
		a := selected[run.ID]
		var summary worker.Summary
		stage := worker.Simulation
		err := store.GetJSON(ctx, a.ResultPrefix+"/"+run.ID+"/summary.json", &summary)
		if storage.IsNotFound(err) {
			stage = worker.Precompute
			err = store.GetJSON(ctx, a.ResultPrefix+"/"+run.ID+"/precompute/summary.json", &summary)
		}
		if err != nil {
			return fmt.Errorf("result for %s from %s is unavailable: %w", run.ID, a.Workflow, err)
		}
		if summary.Run.ID != run.ID || summary.Run.Configuration.ID != run.Configuration.ID || summary.ExecutionID != a.ExecutionID || summary.Stage != stage {
			return errors.New("selected result does not match its execution and saved plan")
		}
		summary.Run = run
		summary.WorkflowPhase = phase
		results = append(results, result{Summary: summary, SourceWorkflow: a.Workflow, SourceExecutionID: a.ExecutionID, AttemptPhase: a.Phase})
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(results)
}
