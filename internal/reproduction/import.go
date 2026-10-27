package reproduction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"simworkflow/internal/agentbuild"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/imageio"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

// Import uploads verified inputs into the current environment. It never submits
// a workflow; the CLI does that only after every upload has succeeded.
func (a *Archive) Import(ctx context.Context, c config.Config, name string, log io.Writer) (*experiment.Plan, Metadata, map[string]string, error) {
	return a.ImportTo(ctx, c, name, "simworkflow/reproduced", log)
}

func (a *Archive) ImportTo(ctx context.Context, c config.Config, name, repository string, log io.Writer) (*experiment.Plan, Metadata, map[string]string, error) {
	if !experiment.ValidMapName(name) {
		return nil, Metadata{}, nil, errors.New("reproduction name must use 1-63 lowercase letters, digits or hyphens")
	}
	if _, err := agentbuild.Reference(c.Registry.URL, repository, "reproduction"); err != nil {
		return nil, Metadata{}, nil, err
	}
	raw, err := json.Marshal(a.Plan)
	if err != nil {
		return nil, Metadata{}, nil, err
	}
	var plan experiment.Plan
	if err = json.Unmarshal(raw, &plan); err != nil {
		return nil, Metadata{}, nil, err
	}
	meta := Metadata{Schema: Schema, Platform: imageio.Platform, CLIVersion: a.Manifest.Metadata.CLIVersion, CLICommit: a.Manifest.Metadata.CLICommit, Images: map[string]string{}}
	meta.ConfigStorage = a.Manifest.Metadata.ConfigStorage
	helperImage, err := a.Image(a.Manifest.Metadata.HelperImage)
	if err != nil {
		return nil, meta, nil, err
	}
	helperConfig, err := helperImage.ConfigFile()
	if err != nil {
		return nil, meta, nil, errors.New("could not inspect bundled helper capabilities")
	}
	meta.HelperConfigBundle = helperConfig.Config.Labels[worker.ConfigBundleCapabilityLabel] == "v1"
	if meta.ConfigStorage == "s3-bundle" && !meta.HelperConfigBundle {
		return nil, meta, nil, errors.New("bundled helper does not support configuration bundles")
	}
	if meta.ConfigStorage == "" {
		meta.ConfigStorage = "inline"
	}
	required := map[string]bool{a.Manifest.Metadata.HelperImage: true}
	for _, run := range plan.Runs {
		required[run.Simulator.Image] = true
		required[run.Configuration.Image] = true
	}
	client := imageio.New(c)
	client.Repository = repository
	byDigest := map[string]string{}
	for ref := range required {
		image, err := a.Image(ref)
		if err != nil {
			return nil, meta, nil, err
		}
		digest, err := image.Digest()
		if err != nil {
			return nil, meta, nil, err
		}
		destination, ok := byDigest[digest.String()]
		if !ok {
			fmt.Fprintln(log, "Importing image", digest.String())
			destination, err = client.Push(ctx, image)
			if err != nil {
				return nil, meta, nil, err
			}
			byDigest[digest.String()] = destination
		}
		meta.Images[ref] = destination
	}
	meta.HelperImage = meta.Images[a.Manifest.Metadata.HelperImage]
	store := storage.New(c)
	mapLocations := map[string]string{}
	for sha, file := range a.Manifest.Maps {
		location, err := saveMap(ctx, store, filepath.Join(a.Directory, filepath.FromSlash(file)), sha)
		if err != nil {
			return nil, meta, nil, err
		}
		mapLocations[sha] = location
	}
	plan.Experiment.Name = name
	mapping := map[string]string{}
	for i, run := range plan.Runs {
		original := run.ID
		run.Configuration.Image = meta.Images[run.Configuration.Image]
		run.Simulator.Image = meta.Images[run.Simulator.Image]
		sha := run.Map.ExpectedSHA256()
		location := mapLocations[sha]
		if location == "" {
			return nil, meta, nil, errors.New("map not imported")
		}
		if run.Map.Resolved != nil {
			copy := *run.Map.Resolved
			copy.Source = location
			run.Map.Resolved = &copy
		} else {
			run.Map.Source = location
			run.Map.SHA256 = sha
		}
		run.Configuration.ID = experiment.ConfigurationIdentity(run.Configuration)
		run.ID = experiment.RunIdentity(run)
		mapping[original] = run.ID
		plan.Runs[i] = run
	}
	if err := ValidatePlan(&plan); err != nil {
		return nil, meta, nil, err
	}
	return &plan, meta, mapping, nil
}
