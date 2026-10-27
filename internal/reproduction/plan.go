package reproduction

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"simworkflow/internal/experiment"
)

func CloneExperiment(e *experiment.Experiment) (*experiment.Experiment, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	var copy experiment.Experiment
	err = json.Unmarshal(b, &copy)
	return &copy, err
}
func ValidatePlan(p *experiment.Plan) error {
	if p == nil || p.APIVersion != experiment.APIVersion || len(p.Runs) == 0 || p.TotalRuns != len(p.Runs) {
		return errors.New("invalid reproduction plan")
	}
	seen := map[string]bool{}
	for _, r := range p.Runs {
		if r.ID != experiment.RunIdentity(r) || seen[r.ID] {
			return errors.New("invalid or duplicate run identity in plan")
		}
		seen[r.ID] = true
		cfg := r.Configuration
		if cfg.ID != experiment.ConfigurationIdentity(cfg) {
			return errors.New("configuration identity mismatch")
		}
		e := &experiment.Experiment{APIVersion: experiment.APIVersion, Kind: "Experiment", Metadata: p.Experiment, Spec: experiment.Spec{Simulator: r.Simulator, Agents: []experiment.Agent{{Name: cfg.Agent, Runtime: cfg.Runtime, Image: cfg.Image, Resources: cfg.Resources, ResourcesByRole: cfg.ResourcesByRole, Modules: cfg.Modules}}, Conditions: experiment.Conditions{Maps: []experiment.Map{r.Map}, Seeds: []int64{r.Seed}}, Execution: experiment.Execution{MaxParallelRuns: p.MaxParallelRuns, Timeout: p.Timeout}, Outputs: experiment.Outputs{Collect: p.Collect}}}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("invalid reproduction run: %w", err)
		}
	}
	duration, err := time.ParseDuration(p.Timeout)
	if err != nil || duration > 20*time.Hour {
		return errors.New("reproduction timeout must be at most 20h")
	}
	waves := (len(p.Runs)-1)/p.MaxParallelRuns + 1
	if int64(waves) > int64((23*time.Hour)/(duration+360*time.Second)) {
		return errors.New("reproduction duration exceeds signed transfer validity")
	}
	return nil
}
func Pinned(ref string) bool { _, err := name.NewDigest(ref); return err == nil }

func PlanSHA256(plan *experiment.Plan) string { data, _ := json.Marshal(plan); return fmtHash(data) }

func ManifestSHA256(manifest Manifest) string {
	data, _ := json.Marshal(manifest)
	return fmtHash(data)
}
