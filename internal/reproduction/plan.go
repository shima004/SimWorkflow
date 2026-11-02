package reproduction

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
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
		e := &experiment.Experiment{APIVersion: experiment.APIVersion, Kind: "Experiment", Metadata: p.Experiment, Spec: experiment.Spec{Simulator: r.Simulator, Agents: []experiment.Agent{{Name: cfg.Agent, Runtime: cfg.Runtime, Image: cfg.Image, Resources: cfg.Resources, ResourcesByRole: cfg.ResourcesByRole, Modules: cfg.Modules}}, Conditions: experiment.Conditions{Maps: []experiment.Map{r.Map}, Seeds: []int64{r.Seed}}, Execution: experiment.Execution{MaxParallelRuns: p.MaxParallelRuns, Timeout: p.Timeout, CollectorResources: p.CollectorResources}, Outputs: experiment.Outputs{Collect: p.Collect}}}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("invalid reproduction run: %w", err)
		}
	}
	return ValidateTransferBudget(p.TotalRuns, p.MaxParallelRuns, p.Timeout)
}

// ValidateTransferBudget needs only the expanded run count and can run before
// catalog reads, image pinning and map snapshot transfers.
func ValidateTransferBudget(totalRuns, parallelism int, timeout string) error {
	if totalRuns <= 0 || parallelism <= 0 {
		return errors.New("invalid execution size or parallelism")
	}
	duration, err := time.ParseDuration(timeout)
	if err != nil || duration <= 0 {
		return errors.New("invalid reproduction timeout")
	}
	const allowance = 6 * time.Minute
	const prefix = "reproduction duration exceeds signed transfer validity"
	// Check before adding the per-wave allowance to avoid duration overflow.
	if duration > storage.SignedTransferBudget {
		return fmt.Errorf("%s: timeout %s exceeds the %s execution budget (signed URLs expire after %s); reduce timeout or split the experiment", prefix, duration, storage.SignedTransferBudget, storage.SignedURLValidity)
	}
	waves := (totalRuns-1)/parallelism + 1
	perWave := duration + allowance
	maxWaves := int(storage.SignedTransferBudget / perWave)
	if waves > maxWaves {
		if maxWaves == 0 {
			return fmt.Errorf("%s: timeout %s + %s transfer allowance exceeds the %s execution budget; reduce timeout", prefix, duration, allowance, storage.SignedTransferBudget)
		}
		minimumParallel := (totalRuns-1)/maxWaves + 1
		return fmt.Errorf("%s: %d runs at parallelism %d require %d waves of (%s timeout + %s transfers), but only %d waves fit in the %s budget (signed URLs expire after %s); reduce timeout or split the experiment; calculated minimum parallelism is %d, subject to cluster capacity", prefix, totalRuns, parallelism, waves, duration, allowance, maxWaves, storage.SignedTransferBudget, storage.SignedURLValidity, minimumParallel)
	}
	return nil
}
func Pinned(ref string) bool { _, err := name.NewDigest(ref); return err == nil }

func PlanSHA256(plan *experiment.Plan) string { data, _ := json.Marshal(plan); return fmtHash(data) }

func ManifestSHA256(manifest Manifest) string {
	data, _ := json.Marshal(manifest)
	return fmtHash(data)
}
