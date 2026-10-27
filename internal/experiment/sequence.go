package experiment

// RunSequence lists runs sharing the same effective container images/resources.
// Member order follows the saved plan, independently of seed values.
type RunSequence struct {
	Name string   `json:"name"`
	Runs []string `json:"runs"`
}

func SequenceName(run Run) string {
	roles := map[string]Resources{}
	for _, role := range Roles() {
		roles[role] = run.Configuration.ResourcesForRole(role)
	}
	profile := struct {
		SimulatorImage     string
		SimulatorResources Resources
		AgentImage         string
		AgentResources     map[string]Resources
	}{run.Simulator.Image, run.Simulator.Resources, run.Configuration.Image, roles}
	return "runs-" + digest(profile)
}

func RunSequences(runs []Run) []RunSequence {
	groups := []RunSequence{}
	byName := map[string]int{}
	for _, run := range runs {
		name := SequenceName(run)
		i, ok := byName[name]
		if !ok {
			i = len(groups)
			byName[name] = i
			groups = append(groups, RunSequence{Name: name})
		}
		groups[i].Runs = append(groups[i].Runs, run.ID)
	}
	return groups
}
