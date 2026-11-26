package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"simworkflow/internal/artifacts"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/operation"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

type comparisonRow struct {
	id        string
	condition experiment.Run
	runs      map[string]int
}

type comparisonPlan struct {
	runs       []experiment.Run
	rows       []comparisonRow
	candidates []string
	byAgent    map[string]map[string]bool
	first      map[string]string
}

type comparisonResult struct {
	score   *float64
	missing string
}

func runCompare(ctx context.Context, args []string, c config.Config, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("experiment compare", flag.ContinueOnError)
	flags.SetOutput(errOut)
	group := flags.String("group", "", "matrix group to compare (required)")
	baseline := flags.String("baseline", "", "candidate for deltas (default: first planned candidate per agent)")
	agent := flags.String("agent", "", "limit comparison to this agent")
	format := flags.String("format", "text", "output format: text or csv")
	withRetries := flags.Bool("with-retries", false, "include retries, preferring the latest successful attempt per run")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 1 || *group == "" {
		return errors.New("usage: experiment compare --group GROUP [options] WORKFLOW_NAME")
	}
	if *format != "text" && *format != "csv" {
		return errors.New("format must be text or csv")
	}
	requested, err := loadSavedExecution(ctx, c, flags.Arg(0))
	if err != nil {
		return err
	}
	family := []*savedExecution{requested}
	if *withRetries {
		family, err = retryFamily(ctx, c, requested)
		if err != nil {
			return err
		}
	}
	root := family[0]
	comparison, err := buildComparison(root.plan, root.meta.Definition, *group, *agent)
	if err != nil {
		return err
	}
	if *baseline != "" {
		for _, name := range slices.Sorted(maps.Keys(comparison.byAgent)) {
			if !comparison.byAgent[name][*baseline] {
				return fmt.Errorf("baseline %q is not a planned candidate for agent %q", *baseline, name)
			}
		}
	}
	attempts := combinedAttempts(family)
	store := storage.New(c)
	results := make([]comparisonResult, len(comparison.runs))
	err = operation.For(ctx, "Read simulation scores", len(comparison.runs), func(ctx context.Context, i int) error {
		run := comparison.runs[i]
		attempt := attempts[run.ID]
		if attempt.Phase != "Succeeded" {
			results[i].missing = attempt.Phase
			return nil
		}
		var summary worker.Summary
		err := store.GetJSON(ctx, attempt.ResultPrefix+"/"+run.ID+"/summary.json", &summary)
		if storage.IsNotFound(err) {
			results[i].missing = "summary missing"
			return nil
		}
		if err != nil {
			return fmt.Errorf("read score for %s: %w", run.ID, err)
		}
		if summary.Run.ID != run.ID || summary.Run.Configuration.ID != run.Configuration.ID ||
			summary.ExecutionID != attempt.ExecutionID || summary.Stage != worker.Simulation {
			return fmt.Errorf("summary for %s does not match the selected simulation and saved plan", run.ID)
		}
		if summary.Phase != "Succeeded" {
			results[i].missing = "summary reports failure"
		} else if summary.Score == nil {
			results[i].missing = "score missing"
		} else {
			results[i].score = summary.Score
		}
		return nil
	})
	if err != nil {
		return err
	}
	return comparison.write(out, *format, *baseline, results, attempts)
}

// The definition identifies which overrides belong to the chosen axis. The
// remaining fields in the saved run form the comparison key.
func buildComparison(plan *experiment.Plan, definition *experiment.Experiment, group, agent string) (*comparisonPlan, error) {
	if definition == nil {
		return nil, errors.New("comparison requires the saved experiment definition to identify which overrides belong to the group; for retries use --with-retries")
	}
	axes := map[string][]experiment.ModuleSet{}
	for _, entry := range definition.Spec.Agents {
		if entry.Matrix != nil {
			axes[entry.Name] = entry.Matrix.Groups[group]
		}
	}
	comparison := &comparisonPlan{byAgent: map[string]map[string]bool{}, first: map[string]string{}}
	rowIndex := map[string]int{}
	candidates := map[string]bool{}
	for _, run := range plan.Runs {
		name := run.Configuration.Agent
		candidate := run.Configuration.Groups[group]
		if candidate == "" || agent != "" && name != agent {
			continue
		}
		condition := run
		cfg := &condition.Configuration
		cfg.ID = ""
		cfg.Groups = maps.Clone(cfg.Groups)
		delete(cfg.Groups, group)
		cfg.Modules = maps.Clone(cfg.Modules)
		cfg.DevelopData = maps.Clone(cfg.DevelopData)
		found := false
		for _, option := range axes[name] {
			if option.Name != candidate {
				continue
			}
			found = true
			for key, value := range option.Modules {
				if cfg.Modules[key] != value {
					return nil, fmt.Errorf("module override for %s differs from the saved group definition", run.ID)
				}
				delete(cfg.Modules, key)
			}
			for key, value := range option.DevelopData {
				actual, _ := json.Marshal(cfg.DevelopData[key])
				expected, _ := json.Marshal(value)
				if string(actual) != string(expected) {
					return nil, fmt.Errorf("DevelopData override for %s differs from the saved group definition", run.ID)
				}
				delete(cfg.DevelopData, key)
			}
			break
		}
		if !found {
			return nil, fmt.Errorf("saved group definition does not contain candidate %q for agent %q", candidate, name)
		}
		id := experiment.RunIdentity(condition)
		index, exists := rowIndex[id]
		if !exists {
			index = len(comparison.rows)
			rowIndex[id] = index
			comparison.rows = append(comparison.rows, comparisonRow{id: id, condition: condition, runs: map[string]int{}})
		}
		if _, duplicate := comparison.rows[index].runs[candidate]; duplicate {
			return nil, fmt.Errorf("candidate %q is ambiguous for condition %s", candidate, id)
		}
		comparison.rows[index].runs[candidate] = len(comparison.runs)
		comparison.runs = append(comparison.runs, run)
		if !candidates[candidate] {
			comparison.candidates = append(comparison.candidates, candidate)
			candidates[candidate] = true
		}
		if comparison.byAgent[name] == nil {
			comparison.byAgent[name] = map[string]bool{}
			comparison.first[name] = candidate
		}
		comparison.byAgent[name][candidate] = true
	}
	if len(comparison.rows) == 0 {
		return nil, fmt.Errorf("no planned runs for group %q under the selected agent filter", group)
	}
	return comparison, nil
}

func (comparison *comparisonPlan) write(out io.Writer, format, baseline string, results []comparisonResult, attempts map[string]artifacts.Attempt) error {
	candidates := comparison.candidates
	header := []string{"agent", "map", "map_version", "seed", "other_groups", "other_modules", "other_develop_data", "baseline"}
	for _, candidate := range candidates {
		header = append(header, "score."+candidate, "delta."+candidate)
	}
	header = append(header, "missing", "missing_reasons")
	if format == "csv" {
		header = append(header, "condition_id", "sources")
	}
	write := func(row []string) error {
		_, err := fmt.Fprintln(out, strings.Join(row, "\t"))
		return err
	}
	var flush func() error
	if format == "csv" {
		cw := csv.NewWriter(out)
		write = cw.Write
		flush = func() error { cw.Flush(); return cw.Error() }
	} else {
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		out = tw
		flush = tw.Flush
	}
	if err := write(header); err != nil {
		return err
	}
	for _, row := range comparison.rows {
		run := row.condition
		cfg := run.Configuration
		version := run.Map.Version
		if run.Map.Resolved != nil {
			version = run.Map.Resolved.Version
		}
		base := baseline
		if base == "" {
			base = comparison.first[cfg.Agent]
		}
		var baseScore *float64
		if index, ok := row.runs[base]; ok {
			baseScore = results[index].score
		}
		groups, _ := json.Marshal(cfg.Groups)
		modules, _ := json.Marshal(cfg.Modules)
		developData, _ := json.Marshal(cfg.DevelopData)
		fields := []string{cfg.Agent, run.Map.Name, version, strconv.FormatInt(run.Seed, 10), string(groups), string(modules), string(developData), base}
		reasons := map[string]string{}
		type source struct {
			RunID    string `json:"runId"`
			Workflow string `json:"workflow"`
		}
		sources := map[string]source{}
		for _, candidate := range candidates {
			var score, delta string
			if index, ok := row.runs[candidate]; ok {
				id := comparison.runs[index].ID
				sources[candidate] = source{RunID: id, Workflow: attempts[id].Workflow}
				result := results[index]
				if result.score == nil {
					reasons[candidate] = result.missing
				} else {
					score = strconv.FormatFloat(*result.score, 'g', -1, 64)
					if baseScore != nil {
						delta = strconv.FormatFloat(*result.score-*baseScore, 'g', 12, 64)
					}
				}
			} else if comparison.byAgent[cfg.Agent][candidate] {
				reasons[candidate] = "not planned"
			}
			fields = append(fields, score, delta)
		}
		missing, _ := json.Marshal(reasons)
		fields = append(fields, strconv.Itoa(len(reasons)), string(missing))
		if format == "csv" {
			provenance, _ := json.Marshal(sources)
			fields = append(fields, row.id, string(provenance))
		}
		if err := write(fields); err != nil {
			return err
		}
	}
	return flush()
}
