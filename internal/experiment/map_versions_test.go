package experiment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestMapVersionsExpandInOrderWithoutChangingDefinition(t *testing.T) {
	e := sample(t)
	e.Spec.Conditions.Maps = []Map{{Name: "america04", Versions: []string{"v1", "v2.0", "v3_fast"}}}
	before, _ := json.Marshal(e)
	plan, err := BuildPlan(e)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalRuns != 27 || !plan.UnresolvedMaps {
		t.Fatal("versions must multiply configurations and seeds", plan.TotalRuns)
	}
	for i, version := range []string{"v1", "v2.0", "v3_fast"} {
		m := plan.Runs[i*len(e.Spec.Conditions.Seeds)].Map
		if m.Name != "america04" || m.Version != version || m.Versions != nil || m.DisplayName() != "america04@"+version {
			t.Fatal("wrong expanded map", m)
		}
	}
	after, _ := json.Marshal(e)
	if !bytes.Equal(before, after) {
		t.Fatal("planning mutated the definition")
	}
	// Normalized YAML from expanded plans uses the public list syntax too.
	copy := *e
	copy.Spec.Conditions.Maps = ExpandMaps(e.Spec.Conditions.Maps)
	data, err := yaml.Marshal(copy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("normalized versions YAML is invalid: %v", err)
	}
	again, err := BuildPlan(decoded)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(plan)
	second, _ := json.Marshal(again)
	if !bytes.Equal(first, second) {
		t.Fatal("normalized YAML changed the expanded plan or identities")
	}
}

func TestMapVersionsRejectAmbiguousDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name string
		maps []Map
	}{
		{"empty", []Map{{Name: "test", Versions: []string{}}}},
		{"duplicate", []Map{{Name: "test", Versions: []string{"v1", "v1"}}}},
		{"invalid", []Map{{Name: "test", Versions: []string{"v1", "bad/value"}}}},
		{"source", []Map{{Name: "test", Versions: []string{"v1"}, Source: "s3://maps/test.zip"}}},
		{"sha", []Map{{Name: "test", Versions: []string{"v1"}, SHA256: strings.Repeat("a", 64)}}},
		{"both", []Map{{Name: "test", Version: "v1", Versions: []string{"v2"}}}},
		{"overlap", []Map{{Name: "test", Versions: []string{"v1", "v2"}}, {Name: "test", Versions: []string{"v2"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := sample(t)
			e.Spec.Conditions.Maps = tc.maps
			if _, err := BuildPlan(e); err == nil {
				t.Fatal("invalid map versions accepted")
			}
		})
	}
}

func TestMapVersionsYAMLUsesListOnly(t *testing.T) {
	definition := `apiVersion: simworkflow/v1alpha1
kind: Experiment
metadata: {name: test}
spec:
  simulator: {image: simulator:v1, resources: {cpu: 1, memory: 1Gi}}
  agents:
    - {name: agent, image: agent:v1, resources: {cpu: 1, memory: 1Gi}}
  conditions:
    maps: [{name: test, versions: [v1, v2]}]
    seeds: [42, 7]
  execution: {maxParallelRuns: 2, timeout: 1m}
  outputs: {collect: [score]}
`
	e, err := Decode(strings.NewReader(definition))
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildPlan(e)
	if err != nil || p.TotalRuns != 4 {
		t.Fatal("YAML versions list did not expand", err)
	}
	old := strings.Replace(definition, "versions: [v1, v2]", "version: v1", 1)
	if _, err := Decode(strings.NewReader(old)); err == nil {
		t.Fatal("singular version YAML must not be accepted")
	}
}
