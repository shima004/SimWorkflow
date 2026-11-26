package experiment

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDevelopDataPlanAndRoundTrip(t *testing.T) {
	e := groupedSample(t)
	a := &e.Spec.Agents[0]
	var settings Agent
	if err := yaml.Unmarshal([]byte(`
developData:
  Large: 9007199254740993
  Ratio: 1.0
  Options: [true, "true", 2.5]
matrix:
  groups:
    threshold:
      - name: low
        developData: {Threshold: 10}
      - name: high
        developData: {Threshold: 15}
`), &settings); err != nil {
		t.Fatal(err)
	}
	a.DevelopData = settings.DevelopData
	a.Matrix.Groups["threshold"] = settings.Matrix.Groups["threshold"]

	plan, err := BuildPlan(e)
	if err != nil || plan.TotalRuns != 8 {
		t.Fatalf("expected 8 runs with parameter-only group: %v", err)
	}
	for _, run := range plan.Runs {
		cfg := run.Configuration
		want := map[string]string{"low": "10", "high": "15"}[cfg.Groups["threshold"]]
		if string(cfg.DevelopData["Threshold"]) != want ||
			string(cfg.DevelopData["Large"]) != "9007199254740993" ||
			string(cfg.DevelopData["Ratio"]) != "1.0" ||
			string(cfg.DevelopData["Options"]) != `[true,"true",2.5]` {
			t.Fatalf("run has wrong DevelopData: %s", cfg.DevelopData)
		}
	}
	originalID := plan.Runs[0].ID

	for _, format := range []struct {
		name      string
		marshal   func(any) ([]byte, error)
		unmarshal func([]byte, any) error
	}{{"json", json.Marshal, json.Unmarshal}, {"yaml", yaml.Marshal, yaml.Unmarshal}} {
		data, err := format.marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var copy Experiment
		if err := format.unmarshal(data, &copy); err != nil {
			t.Fatal(err)
		}
		copied, err := BuildPlan(&copy)
		if err != nil || copied.Runs[0].ID != originalID {
			t.Fatalf("%s round trip changed run identity: %v", format.name, err)
		}
	}

	a.DevelopData["Large"] = json.RawMessage("9007199254740994")
	changed, err := BuildPlan(e)
	if err != nil || changed.Runs[0].ID == originalID {
		t.Fatalf("changing only a parameter must change run identity: %v", err)
	}
	a.DevelopData["Threshold"] = json.RawMessage("12")
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "fixed developData") {
		t.Fatalf("fixed value and group must not own the same key: %v", err)
	}
}
