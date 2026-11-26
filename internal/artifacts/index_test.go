package artifacts

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
		run.Configuration.DevelopData = experiment.DevelopData{"Threshold": json.RawMessage([]string{"10", "15"}[i])}
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
	if len(rows) != 2 || rows[0]["run_id"] != firstRun || rows[1]["run_id"] != secondRun || rows[1]["group.planning"] != "dijkstra" || rows[0]["developData.Threshold"] != "10" || rows[1]["developData.Threshold"] != "15" {
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

func TestRunCSVDevelopDataTypesAndAbsentValues(t *testing.T) {
	entries := []RunIndexEntry{{Run: experiment.Run{Configuration: experiment.Configuration{DevelopData: experiment.DevelopData{
		"Threshold": json.RawMessage("10"),
		"Large":     json.RawMessage("9007199254740993"),
		"Ratio":     json.RawMessage("1.0"),
		"Enabled":   json.RawMessage("false"),
		"Label":     json.RawMessage(`"true"`),
		"Empty":     json.RawMessage(`""`),
		"Options":   json.RawMessage("[\n true, \"true\", 2.5\n]"),
		"Text":      json.RawMessage(`"comma, quote\" and newline\n"`),
	}}}}, {Run: experiment.Run{Configuration: experiment.Configuration{DevelopData: experiment.DevelopData{"Threshold": json.RawMessage("15"), "OnlySecond": json.RawMessage("0")}}}}}
	var out bytes.Buffer
	if err := writeRunCSV(&out, entries); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(&out).ReadAll()
	if err != nil || len(records) != 3 {
		t.Fatal(err, records)
	}
	var developHeaders []string
	columns := map[string]int{}
	for i, header := range records[0] {
		columns[header] = i
		if strings.HasPrefix(header, "developData.") {
			developHeaders = append(developHeaders, header)
		}
	}
	if len(developHeaders) != 9 || !slices.IsSorted(developHeaders) {
		t.Fatal("missing or unordered DevelopData columns", developHeaders)
	}
	for key, value := range map[string]string{
		"Threshold": "10", "Large": "9007199254740993", "Ratio": "1.0", "Enabled": "false",
		"Label": `"true"`, "Empty": `""`, "Options": `[true,"true",2.5]`,
		"Text": `"comma, quote\" and newline\n"`, "OnlySecond": "",
	} {
		if got := records[1][columns["developData."+key]]; got != value {
			t.Errorf("%s: got %q want %q", key, got, value)
		}
	}
	if records[2][columns["developData.Empty"]] != "" || records[2][columns["developData.OnlySecond"]] != "0" || records[2][columns["developData.Threshold"]] != "15" {
		t.Fatal("absent values and overrides were confused", records[2])
	}
	out.Reset()
	if err := writeRunCSV(&out, []RunIndexEntry{{}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "developData.") {
		t.Fatal("unexpected columns without DevelopData", out.String())
	}
}
