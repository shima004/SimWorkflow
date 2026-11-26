package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"simworkflow/internal/reproduction/testfixture"
	"simworkflow/internal/worker"
)

func TestCompareGroupsUsesMatchingConditionsAndSuccessfulRetries(t *testing.T) {
	backend, original := newRetryBackend(t)
	if err := runRemote([]string{"retry", "original"}, backend.env.Config, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	backend.finish(t, "retry-1", []string{"Failed", "Succeeded", "Failed"})
	saveSummary := func(workflow string, i int, stage string, score float64) {
		prefix := backend.snapshots[workflow].Metadata.Annotations["simworkflow.io/result-prefix"]
		run := original.Runs[i]
		key := "/fixture/" + prefix + "/" + run.ID
		if stage == worker.Precompute {
			key += "/precompute"
		}
		summary := worker.Summary{
			Run: run, ExecutionID: strings.TrimPrefix(prefix, "experiments/"),
			Phase: "Succeeded", Stage: stage, Score: &score,
		}
		backend.env.Objects.Set(key+"/summary.json", testfixture.JSON(t, summary))
	}
	saveSummary("original", 0, worker.Simulation, 0)
	saveSummary("original", 2, worker.Simulation, 999) // Failed attempt with a score.
	saveSummary("original", 4, worker.Precompute, 888)
	saveSummary("original", 6, worker.Simulation, 20)
	saveSummary("retry-1", 2, worker.Simulation, 13)

	var out bytes.Buffer
	args := []string{"compare", "--group", "A", "--baseline", "a1", "--format", "csv"}
	for _, withRetries := range []bool{false, true} {
		suffix := []string{"original"}
		if withRetries {
			suffix = []string{"--with-retries", "retry-1"}
		}
		out.Reset()
		if err := runRemote(append(args, suffix...), backend.env.Config, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(&out).ReadAll()
		if err != nil || len(rows) != 5 {
			t.Fatalf("expected four separate conditions (two seeds and two B choices): %v %v", rows, err)
		}
		for _, record := range rows[1:] {
			cells := map[string]string{}
			for i, name := range rows[0] {
				cells[name] = record[i]
			}
			var reasons map[string]string
			if err := json.Unmarshal([]byte(cells["missing_reasons"]), &reasons); err != nil {
				t.Fatal(err)
			}
			if cells["seed"] == "1" && cells["other_groups"] == `{"B":"b1"}` {
				if cells["score.a1"] != "0" || cells["score.a2"] != "" || reasons["a2"] != "Unfinished" {
					t.Fatal("a real zero or precompute-only result was mishandled", cells)
				}
			}
			if cells["seed"] != "1" || cells["other_groups"] != `{"B":"b2"}` {
				continue
			}
			if cells["score.a2"] != "20" || cells["other_modules"] != `{"Fire.Clustering":"example.B2"}` {
				t.Fatal("matched the wrong conditions", cells)
			}
			if withRetries {
				if cells["score.a1"] != "13" || cells["delta.a2"] != "7" || cells["missing"] != "0" || !strings.Contains(cells["sources"], "retry-1") {
					t.Fatal("successful retry was not selected", cells)
				}
			} else if cells["score.a1"] != "" || cells["delta.a2"] != "" || reasons["a1"] != "Error" {
				t.Fatal("failed attempt should have no comparable score", cells)
			}
		}
	}
}
