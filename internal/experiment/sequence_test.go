package experiment

import "testing"

func TestRunSequencesGroupEffectiveContainerSettings(t *testing.T) {
	base := Run{ID: "first", Seed: 42, Simulator: Container{Image: "simulator:v1", Resources: Resources{CPU: "2", Memory: "8Gi"}}, Configuration: Configuration{Agent: "a", Image: "agent:v1", Resources: Resources{CPU: "4", Memory: "8Gi"}}}
	other := base
	other.ID, other.Seed = "second", 7
	other.Configuration.Agent = "b"
	other.Configuration.Modules = map[string]string{"Move.PathPlanning": "example.Other"}
	other.Map = Map{Name: "another-map"}
	// Explicit overrides equal to the effective defaults share the same Pods.
	other.Configuration.ResourcesByRole = map[string]Resources{"policeForce": base.Configuration.Resources}
	if SequenceName(base) != SequenceName(other) {
		t.Fatal("non-container settings must not split sequences")
	}
	for _, change := range []func(*Run){
		func(r *Run) { r.Configuration.Image = "agent:v2" },
		func(r *Run) { r.Simulator.Image = "simulator:v2" },
		func(r *Run) { r.Simulator.Resources.Memory = "12Gi" },
		func(r *Run) {
			r.Configuration.ResourcesByRole = map[string]Resources{"policeForce": {CPU: "2", Memory: "4Gi"}}
		},
	} {
		third := base
		third.ID = "third"
		change(&third)
		groups := RunSequences([]Run{base, third, other})
		if len(groups) != 2 || len(groups[0].Runs) != 2 || groups[0].Runs[0] != "first" || groups[0].Runs[1] != "second" || groups[1].Runs[0] != "third" {
			t.Fatal("group membership or per-group plan order changed", groups)
		}
	}
}
