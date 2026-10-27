// Package reproduction saves and restores complete experiment inputs.
package reproduction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/imageio"
	"simworkflow/internal/maparchive"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

const Schema = "simworkflow/reproduction/v1alpha1"

type Origin struct {
	Workflow       string            `json:"workflow"`
	ManifestSHA256 string            `json:"manifestSHA256"`
	Runs           map[string]string `json:"runs"`
}
type Metadata struct {
	Origin             *Origin                `json:"origin,omitempty"`
	Schema             string                 `json:"schema"`
	PlanSHA256         string                 `json:"planSHA256,omitempty"`
	CLIVersion         string                 `json:"cliVersion"`
	CLICommit          string                 `json:"cliCommit"`
	Platform           string                 `json:"platform"`
	HelperImage        string                 `json:"helperImage"`
	ConfigStorage      string                 `json:"configStorage,omitempty"`
	HelperConfigBundle bool                   `json:"helperConfigBundle,omitempty"`
	Images             map[string]string      `json:"images"`
	Definition         *experiment.Experiment `json:"definition,omitempty"`
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
	e.Spec.Simulator.Image, err = pin(e.Spec.Simulator.Image)
	if err != nil {
		return nil, meta, fmt.Errorf("pin simulator: %w", err)
	}
	for i := range e.Spec.Agents {
		e.Spec.Agents[i].Image, err = pin(e.Spec.Agents[i].Image)
		if err != nil {
			return nil, meta, fmt.Errorf("pin agent: %w", err)
		}
	}
	store := storage.New(c)
	snapshots := map[string]experiment.Map{}
	for i, m := range e.Spec.Conditions.Maps {
		if saved, ok := snapshots[m.Location()]; ok {
			if m.ExpectedSHA256() != "" && m.ExpectedSHA256() != saved.ExpectedSHA256() {
				return nil, meta, errors.New("map aliases disagree on SHA-256")
			}
			if m.Resolved != nil {
				copy := *m.Resolved
				copy.Source = saved.Location()
				m.Resolved = &copy
				m.Version = copy.Version
			} else {
				m.Source = saved.Location()
				m.SHA256 = saved.ExpectedSHA256()
			}
			e.Spec.Conditions.Maps[i] = m
			continue
		}
		transfer, err := store.Download(ctx, m.Location())
		if err != nil {
			return nil, meta, err
		}
		response, err := storage.Request(ctx, "GET", transfer, nil)
		if err != nil {
			return nil, meta, err
		}
		file, err := os.CreateTemp("", "simworkflow-map-snapshot-*.zip")
		if err != nil {
			response.Body.Close()
			return nil, meta, err
		}
		path := file.Name()
		n, copyErr := io.Copy(file, io.LimitReader(response.Body, maparchive.MaxBytes+1))
		response.Body.Close()
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n > maparchive.MaxBytes {
			os.Remove(path)
			return nil, meta, errors.New("could not snapshot map within 2GiB limit")
		}
		info, err := maparchive.Inspect(path)
		if err != nil {
			os.Remove(path)
			return nil, meta, err
		}
		if expected := m.ExpectedSHA256(); expected != "" && expected != info.SHA256 {
			os.Remove(path)
			return nil, meta, errors.New("map does not match recorded SHA-256")
		}
		location, err := saveMap(ctx, store, path, info.SHA256)
		os.Remove(path)
		if err != nil {
			return nil, meta, err
		}
		original := m.Location()
		if m.Resolved != nil {
			copy := *m.Resolved
			copy.Source = location
			m.Resolved = &copy
			m.Version = copy.Version
		} else {
			m.Source = location
			m.SHA256 = info.SHA256
		}
		snapshots[original] = m
		e.Spec.Conditions.Maps[i] = m
	}
	meta.Definition = e
	return e, meta, nil
}
