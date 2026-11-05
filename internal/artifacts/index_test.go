package artifacts

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"simworkflow/internal/experiment"
)

func readRunCSV(t *testing.T, output string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(output, "runs.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) == 0 {
		t.Fatal(err, rows)
	}
	var result []map[string]string
	for _, row := range rows[1:] {
		values := map[string]string{}
		for i, header := range rows[0] {
			values[header] = row[i]
		}
		result = append(result, values)
	}
	return result
}

func TestRunIndexesExplainCombinationsAndPreservePreviousDownloads(t *testing.T) {
	source, plan, output := fixture(t)
	for i := range plan.Runs {
		run := &plan.Runs[i]
		run.Configuration.Agent = "distributed"
		run.Configuration.ID = "config-test"
		run.Configuration.Groups = map[string]string{"planning": []string{"astar", "dijkstra"}[i]}
		run.Configuration.Modules = map[string]string{"Move.PathPlanning": "example.Class,With\"Quote"}
		run.Configuration.Resources = experiment.Resources{CPU: "4", Memory: "8Gi"}
		run.Map.Name, run.Map.Version, run.Seed = "america03", "type0", int64(i+1)
	}
	options := Options{Workflow: "experiment", Phase: "Succeeded", Output: output, Stage: "simulation", Runs: []string{firstRun}, Include: []string{"summary"}}
	report, err := Download(context.Background(), source, plan, testPrefix, options)
	if err != nil || report.Added != 1 || len(report.IndexFiles) != 2 || len(source.opened) != 1 {
		t.Fatal("metadata must not count as downloaded files or require extra requests", err, report, source.opened)
	}
	rows := readRunCSV(t, output)
	if len(rows) != 1 || rows[0]["run_id"] != firstRun || rows[0]["agent"] != "distributed" || rows[0]["map"] != "america03" || rows[0]["map_version"] != "type0" || rows[0]["seed"] != "1" || rows[0]["group.planning"] != "astar" || rows[0]["module.Move.PathPlanning"] != "example.Class,With\"Quote" {
		t.Fatal("run conditions are missing or incorrectly CSV-escaped", rows)
	}
	if rows[0]["files"] != `["`+firstRun+`/simulation/summary.json"]` {
		t.Fatal("missing link to downloaded artifact", rows)
	}
	options.Runs = []string{secondRun}
	if _, err := Download(context.Background(), source, plan, testPrefix, options); err != nil {
		t.Fatal(err)
	}
	rows = readRunCSV(t, output)
	if len(rows) != 2 || rows[0]["run_id"] != firstRun || rows[1]["run_id"] != secondRun || rows[1]["group.planning"] != "dijkstra" {
		t.Fatal("append must retain conditions for previously downloaded runs", rows)
	}
	data, err := os.ReadFile(filepath.Join(output, "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index RunIndex
	if err := json.Unmarshal(data, &index); err != nil || len(index.Runs) != 2 || index.Runs[0].Configuration.Resources.Memory != "8Gi" || index.Runs[0].Source.Workflow != "experiment" || index.Runs[0].Source.ExecutionID != strings.TrimPrefix(testPrefix, "experiments/") {
		t.Fatal("full context is missing from JSON index", err, index)
	}
	source.opened = nil
	report, err = Download(context.Background(), source, plan, testPrefix, options)
	if err != nil || report.Added != 0 || len(report.Skipped) != 1 || len(source.opened) != 0 {
		t.Fatal("generated indexes must not break verified resume", err, report)
	}
}

func TestRunIndexesPreserveLocalEditsAndRejectLinks(t *testing.T) {
	for _, mode := range []string{"edited", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			source, plan, output := fixture(t)
			options := Options{Workflow: "experiment", Output: output, Stage: "simulation", Include: []string{"summary"}}
			if _, err := Download(context.Background(), source, plan, testPrefix, options); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(output, "runs.csv")
			outside := filepath.Join(t.TempDir(), "private.csv")
			if err := os.WriteFile(outside, []byte("local content"), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "edited" {
				if err := os.WriteFile(path, []byte("local content"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				os.Remove(path)
				if err := os.Symlink(outside, path); err != nil {
					t.Skip("symlinks unavailable", err)
				}
			}
			source.opened = nil
			if _, err := Download(context.Background(), source, plan, testPrefix, options); err == nil || len(source.opened) != 0 {
				t.Fatal("local index changes must be rejected before source reads", err, source.opened)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "local content" {
				t.Fatal("local content was overwritten", err, string(data))
			}
		})
	}
}
