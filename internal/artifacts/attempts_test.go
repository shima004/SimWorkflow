package artifacts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCombinedDownloadValidatesAttemptsAndProvenance(t *testing.T) {
	source, plan, output := fixture(t)
	const retryPrefix = "experiments/11111111111111111111111111111111"
	attempts := map[string]Attempt{
		firstRun:  {Workflow: "original", ResultPrefix: testPrefix, ExecutionID: strings.TrimPrefix(testPrefix, "experiments/"), Phase: "Succeeded"},
		secondRun: {Workflow: "retry", ResultPrefix: retryPrefix, ExecutionID: strings.TrimPrefix(retryPrefix, "experiments/"), Phase: "Succeeded"},
	}
	for _, stage := range []string{"precompute", "simulation"} {
		key := retryPrefix + "/" + secondRun
		if stage == "precompute" {
			key += "/precompute"
		}
		source.objects[key+"/summary.json"] = []byte(`{"attempt":"retry"}`)
	}
	o := Options{Workflow: "original", Phase: "Succeeded", Output: output, Include: []string{"summary"}, Stage: "all", WithRetries: true, Attempts: attempts}
	report, err := Download(context.Background(), source, plan, testPrefix, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Downloaded) != 4 {
		t.Fatal("wrong number of stage summaries")
	}
	for _, file := range report.Downloaded {
		a := attempts[file.RunID]
		if a.Workflow != file.Workflow || a.ExecutionID != file.ExecutionID || !strings.HasPrefix(file.Source, a.ResultPrefix+"/"+file.RunID+"/") {
			t.Fatal("inconsistent file provenance")
		}
	}
	report, err = Download(context.Background(), source, plan, testPrefix, o)
	if err != nil || len(report.Skipped) != 4 {
		t.Fatal("combined download could not resume", err)
	}
	// A forged file-to-attempt association is rejected before any source reads.
	report.Downloaded[0].Workflow = "different-workflow"
	manifest, _ := json.Marshal(report)
	if err := os.WriteFile(filepath.Join(output, "download.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	before := len(source.opened)
	if _, err := Download(context.Background(), source, plan, testPrefix, o); err == nil {
		t.Fatal("forged provenance accepted")
	}
	if len(source.opened) != before {
		t.Fatal("invalid manifest reached remote objects")
	}
}

func TestCombinedDownloadRequiresValidCompleteSelections(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attempts map[string]Attempt
	}{
		{"missing selection", nil},
		{"unsafe prefix", map[string]Attempt{firstRun: {Workflow: "original", ResultPrefix: "../other"}, secondRun: {}}},
		{"mismatched identity", map[string]Attempt{firstRun: {Workflow: "original", ResultPrefix: testPrefix, ExecutionID: "wrong"}, secondRun: {}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, plan, output := fixture(t)
			_, err := Download(context.Background(), source, plan, testPrefix, Options{Workflow: "original", Output: output, Include: []string{"summary"}, WithRetries: true, Attempts: tc.attempts})
			if err == nil {
				t.Fatal("invalid attempt selections accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("invalid selection created output")
			}
		})
	}
}
