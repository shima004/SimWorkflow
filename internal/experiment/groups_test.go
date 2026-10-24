package experiment

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"
)

func groupedSample(t *testing.T) *Experiment {
	t.Helper()
	e, err := ReadFile("../../examples/experiments/grouped-modules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGroupCartesianProductAndAtomicSets(t *testing.T) {
	e := groupedSample(t)
	a := &e.Spec.Agents[0]
	a.Modules = map[string]string{"Fixed.Module": "impl.Fixed"}
	a.Matrix.Modules = map[string][]string{"Extra.Module": {"impl.One", "impl.Two"}}
	e.Spec.Conditions.Seeds = []int64{1, 2}
	before, _ := json.Marshal(e)
	plan, err := BuildPlan(e, 16)
	if err != nil || plan.TotalRuns != 16 || plan.Agents[0].Configurations != 8 {
		t.Fatalf("expected 2x2 groups x2 module candidates x2 seeds: %v", err)
	}
	ids := map[string]bool{}
	for _, run := range plan.Runs {
		if ids[run.ID] {
			t.Fatal("group choices must produce unique run identities")
		}
		ids[run.ID] = true
		c := run.Configuration
		if len(c.Groups) != 2 || len(c.Modules) != 6 || c.Modules["Fixed.Module"] != "impl.Fixed" {
			t.Fatal("fixed, grouped and standalone modules must be merged")
		}
		for group, name := range c.Groups {
			matched := false
			for _, option := range a.Matrix.Groups[group] {
				if option.Name != name {
					continue
				}
				matched = true
				for key, value := range option.Modules {
					if c.Modules[key] != value {
						t.Fatal("a group's module set must be selected atomically")
					}
				}
			}
			if !matched {
				t.Fatal("unknown selected group label")
			}
		}
	}
	after, _ := json.Marshal(e)
	if string(before) != string(after) {
		t.Fatal("planning/validation must not mutate the experiment")
	}
	first, _ := json.Marshal(plan)
	// YAML mapping insertion order must not affect expansion or identities.
	a.Matrix.Groups = map[string][]ModuleSet{"B": a.Matrix.Groups["B"], "A": a.Matrix.Groups["A"]}
	again, err := BuildPlan(e, 16)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := json.Marshal(again)
	if string(first) != string(second) {
		t.Fatal("group expansion must be deterministic")
	}
	if _, err := BuildPlan(e, 15); err == nil {
		t.Fatal("group products must obey the total run limit")
	}
	plan.Runs[0].Configuration.Groups["A"] = "modified"
	plan.Runs[0].Configuration.Modules["ExtendActionClear.PathPlanning"] = "impl.Modified"
	if a.Matrix.Groups["A"][0].Name != "astar" || a.Matrix.Groups["A"][0].Modules["ExtendActionClear.PathPlanning"] == "impl.Modified" {
		t.Fatal("expanded values must not alias input maps")
	}
}

func TestGroupExclusionsAndIdentity(t *testing.T) {
	e := groupedSample(t)
	base, err := BuildPlan(e, 10)
	if err != nil {
		t.Fatal(err)
	}
	e.Spec.Agents[0].Matrix.Exclude = []Exclusion{{Groups: map[string]string{"A": "astar", "B": "kmeanspp"}}}
	plan, err := BuildPlan(e, 10)
	if err != nil || plan.TotalRuns != 3 {
		t.Fatal("group-based exclusions must remove exactly the selected combination")
	}
	for _, run := range plan.Runs {
		if run.Configuration.Groups["A"] == "astar" && run.Configuration.Groups["B"] == "kmeanspp" {
			t.Fatal("excluded group product was emitted")
		}
	}
	key := "ExtendActionClear.PathPlanning"
	value := e.Spec.Agents[0].Matrix.Groups["A"][0].Modules[key]
	e.Spec.Agents[0].Matrix.Exclude = []Exclusion{{Modules: map[string]string{key: value}, Groups: map[string]string{"B": "kmeans"}}}
	plan, err = BuildPlan(e, 10)
	if err != nil || plan.TotalRuns != 3 {
		t.Fatal("module/group criteria in one exclusion must both match")
	}
	e.Spec.Agents[0].Matrix.Exclude = []Exclusion{{Modules: map[string]string{key: value}}}
	plan, err = BuildPlan(e, 10)
	if err != nil || plan.TotalRuns != 2 {
		t.Fatal("module exclusions must also recognize grouped module values")
	}
	e.Spec.Agents[0].Matrix.Exclude = nil
	e.Spec.Agents[0].Matrix.Groups["A"][0].Name = "renamed"
	renamed, err := BuildPlan(e, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(base.Runs[0].Configuration.Modules, renamed.Runs[0].Configuration.Modules) || base.Runs[0].ID == renamed.Runs[0].ID {
		t.Fatal("selected group labels must be part of the configuration/run identity")
	}
}

func TestGroupValidation(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		edit       func(*Agent)
	}{
		{"empty group", "candidate", func(a *Agent) { a.Matrix.Groups["A"] = nil }},
		{"invalid group name", "group names", func(a *Agent) { a.Matrix.Groups["bad.name"] = a.Matrix.Groups["A"]; delete(a.Matrix.Groups, "A") }},
		{"invalid candidate name", ".name", func(a *Agent) { a.Matrix.Groups["A"][0].Name = "bad.name" }},
		{"duplicate names", "unique", func(a *Agent) { a.Matrix.Groups["A"][1].Name = a.Matrix.Groups["A"][0].Name }},
		{"empty set", "at least one module", func(a *Agent) { a.Matrix.Groups["A"][0].Modules = nil }},
		{"different key sets", "same module keys", func(a *Agent) { delete(a.Matrix.Groups["A"][1].Modules, "ExtendActionClear.PathPlanning") }},
		{"duplicate sets", "distinct module sets", func(a *Agent) { a.Matrix.Groups["A"][1].Modules = maps.Clone(a.Matrix.Groups["A"][0].Modules) }},
		{"fixed conflict", "fixed modules", func(a *Agent) { a.Modules = map[string]string{"ExtendActionClear.PathPlanning": "impl.Fixed"} }},
		{"standalone conflict", "matrix.modules", func(a *Agent) {
			a.Matrix.Modules = map[string][]string{"ExtendActionClear.PathPlanning": {"impl.Other"}}
		}},
		{"group conflict", "more than one group", func(a *Agent) { a.Matrix.Groups["C"] = a.Matrix.Groups["A"] }},
		{"invalid implementation", "identifiers", func(a *Agent) { a.Matrix.Groups["A"][0].Modules["ExtendActionClear.PathPlanning"] = "bad value" }},
		{"unknown exclude group", "existing group", func(a *Agent) { a.Matrix.Exclude = []Exclusion{{Groups: map[string]string{"Z": "unknown"}}} }},
		{"unknown exclude name", "candidate name", func(a *Agent) { a.Matrix.Exclude = []Exclusion{{Groups: map[string]string{"A": "unknown"}}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := groupedSample(t)
			tc.edit(&e.Spec.Agents[0])
			if err := e.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s, got %v", tc.want, err)
			}
		})
	}
}

func TestExcludedGroupSubtreesArePruned(t *testing.T) {
	e := groupedSample(t)
	a := &e.Spec.Agents[0]
	a.Matrix.Exclude = []Exclusion{{Groups: map[string]string{"A": "astar"}}, {Groups: map[string]string{"A": "dstar"}}}
	for i := 0; i < 80; i++ {
		a.Matrix.Groups[fmt.Sprintf("Z%02d", i)] = []ModuleSet{{Name: "one", Modules: map[string]string{fmt.Sprintf("Module.K%02d", i): "impl.One"}}, {Name: "two", Modules: map[string]string{fmt.Sprintf("Module.K%02d", i): "impl.Two"}}}
	}
	if _, err := BuildPlan(e, 10); err == nil || !strings.Contains(err.Error(), "remove all") {
		t.Fatal("fully excluded branches must be pruned before enumerating their huge product")
	}
}

func TestLegacyIdentitiesRemainUnchanged(t *testing.T) {
	plan, err := BuildPlan(sample(t), 100)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Runs[0].Configuration.ID != "config-bbc9d119635779233138170548ccaa60" || plan.Runs[0].ID != "run-0bc42b35d7a42656ac200fb1f1c2ee52" {
		t.Fatal("adding optional groups must not change existing configuration/run IDs")
	}
}

func TestStrictGroupYAML(t *testing.T) {
	data, err := os.ReadFile("../../examples/experiments/grouped-modules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ from, to string }{
		{"- name: astar", "- unknownName: astar"},
		{"A:\n            - name: astar", "A: wrong-type\n          DuplicateA:\n            - name: astar"},
		{"name: astar", "name: astar\n              name: duplicate"},
	} {
		modified := strings.Replace(string(data), change.from, change.to, 1)
		if modified == string(data) {
			t.Fatal("test edit did not match")
		}
		if _, err := Decode(strings.NewReader(modified)); err == nil {
			t.Fatal("unknown fields, invalid types and duplicate keys must be rejected in groups")
		}
	}
}
