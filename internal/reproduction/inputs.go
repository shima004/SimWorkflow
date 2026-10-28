// Package reproduction saves and restores complete experiment inputs.
package reproduction

import (
	"context"
	"errors"
	"fmt"

	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/imageio"
	"simworkflow/internal/operation"
	"simworkflow/internal/worker"
)

const Schema = "simworkflow/reproduction/v1alpha1"

type Origin struct {
	Workflow       string            `json:"workflow"`
	ManifestSHA256 string            `json:"manifestSHA256"`
	Runs           map[string]string `json:"runs"`
}

// RetryOrigin links a fresh execution to the original experiment and its parent.
// Run identities are preserved across retries; execution IDs and transfer URLs are new.
type RetryOrigin struct {
	RootWorkflow   string `json:"rootWorkflow"`
	RootPrefix     string `json:"rootPrefix"`
	ParentWorkflow string `json:"parentWorkflow"`
	ParentPrefix   string `json:"parentPrefix"`
}
type Metadata struct {
	Retry                *RetryOrigin           `json:"retry,omitempty"`
	Origin               *Origin                `json:"origin,omitempty"`
	Schema               string                 `json:"schema"`
	PlanSHA256           string                 `json:"planSHA256,omitempty"`
	CLIVersion           string                 `json:"cliVersion"`
	CLICommit            string                 `json:"cliCommit"`
	Platform             string                 `json:"platform"`
	HelperImage          string                 `json:"helperImage"`
	ConfigStorage        string                 `json:"configStorage,omitempty"`
	HelperConfigBundle   bool                   `json:"helperConfigBundle,omitempty"`
	HelperConfigSequence bool                   `json:"helperConfigSequence,omitempty"`
	Images               map[string]string      `json:"images"`
	Definition           *experiment.Experiment `json:"definition,omitempty"`
}

// Capture pins image contents and stores immutable map snapshots before submission.
func Capture(ctx context.Context, c config.Config, input *experiment.Experiment, helper, version, commit string) (*experiment.Experiment, Metadata, error) {
	// JSON cloning avoids aliasing nested candidate/resource maps in caller data.
	e, err := CloneExperiment(input)
	if err != nil {
		return nil, Metadata{}, err
	}
	meta := Metadata{Schema: Schema, CLIVersion: version, CLICommit: commit, Platform: imageio.Platform, ConfigStorage: "s3", Images: map[string]string{}}
	images := imageio.New(c)
	pin := func(ref string) (string, error) {
		if value, ok := meta.Images[ref]; ok {
			return value, nil
		}
		value, err := images.Resolve(ctx, ref)
		if err == nil {
			meta.Images[ref] = value
		}
		return value, err
	}
	operation.Report(ctx, operation.Event{Stage: "Resolve container images", Total: len(e.Spec.Agents) + 2})
	helperImage, pinnedHelper, err := images.Fetch(ctx, helper)
	if err != nil {
		return nil, meta, fmt.Errorf("pin helper: %w", err)
	}
	meta.HelperImage = pinnedHelper
	meta.Images[helper] = pinnedHelper
	helperConfig, err := helperImage.ConfigFile()
	if err != nil {
		return nil, meta, errors.New("could not inspect helper capabilities")
	}
	meta.HelperConfigBundle = helperConfig.Config.Labels[worker.ConfigBundleCapabilityLabel] == "v1"
	meta.HelperConfigSequence = meta.HelperConfigBundle && helperConfig.Config.Labels[worker.ConfigSequenceCapabilityLabel] == "v1"
	mode := "s3"
	if meta.HelperConfigBundle {
		mode = "s3-bundle"
	}
	if meta.HelperConfigSequence {
		mode = "s3-sequence"
	}
	operation.Report(ctx, operation.Event{Stage: "Helper configuration capability", Detail: mode})
	operation.Report(ctx, operation.Event{Stage: "Resolve container images", Completed: 1, Total: len(e.Spec.Agents) + 2})
	e.Spec.Simulator.Image, err = pin(e.Spec.Simulator.Image)
	if err != nil {
		return nil, meta, fmt.Errorf("pin simulator: %w", err)
	}
	operation.Report(ctx, operation.Event{Stage: "Resolve container images", Completed: 2, Total: len(e.Spec.Agents) + 2})
	for i := range e.Spec.Agents {
		e.Spec.Agents[i].Image, err = pin(e.Spec.Agents[i].Image)
		if err != nil {
			return nil, meta, fmt.Errorf("pin agent: %w", err)
		}
		operation.Report(ctx, operation.Event{Stage: "Resolve container images", Completed: i + 3, Total: len(e.Spec.Agents) + 2})
	}
	if err := snapshotInputs(ctx, c, e); err != nil {
		return nil, meta, err
	}

	meta.Definition = e
	return e, meta, nil
}
