// Package mapcatalog stores immutable, validated map versions in S3.
package mapcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/maparchive"
	"simworkflow/internal/operation"
	"simworkflow/internal/storage"
)

const Schema = "simworkflow/map/v1alpha1"

type Metadata struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Origin      string `json:"origin,omitempty"`
	Source      string `json:"source"`
	maparchive.Info
	RegisteredAt time.Time `json:"registeredAt"`
}

type RegisterOptions struct{ Name, Version, Description, Origin string }
type Catalog struct{ Store *storage.Store }

func key(name, version string) string { return "maps/" + name + "/" + version + "/" }
func validateIdentity(name, version string) error {
	if !experiment.ValidMapName(name) {
		return errors.New("map name must use 1-63 lowercase letters, digits or hyphens")
	}
	if !experiment.ValidMapVersion(version) {
		return errors.New("version must use 1-63 lowercase letters, digits, dots, underscores or hyphens")
	}
	return nil
}

func (c *Catalog) Register(ctx context.Context, path string, options RegisterOptions) (Metadata, error) {
	var result Metadata
	if err := validateIdentity(options.Name, options.Version); err != nil {
		return result, err
	}
	if options.Version == "latest" {
		return result, errors.New("latest is a reference and cannot be registered as a version")
	}
	if len(options.Description) > 4096 || len(options.Origin) > 2048 {
		return result, errors.New("description or origin is too long")
	}
	prefix := key(options.Name, options.Version)
	var existing Metadata
	err := c.Store.GetJSON(ctx, prefix+"metadata.json", &existing)
	if err == nil {
		return result, errors.New("map version is already registered; choose a new version")
	}
	if !storage.IsNotFound(err) {
		return result, err
	}
	// Snapshot input before inspecting or uploading it, including ZIP inputs.
	snapshot, err := os.CreateTemp("", "simworkflow-register-*.zip")
	if err != nil {
		return result, err
	}
	snapshotPath := snapshot.Name()
	snapshot.Close()
	defer os.Remove(snapshotPath)
	stat, err := os.Stat(path)
	if err != nil {
		return result, err
	}
	if stat.IsDir() {
		err = maparchive.PackageDirectory(path, snapshotPath)
	} else {
		source, e := os.Open(path)
		if e != nil {
			return result, e
		}
		defer source.Close()
		if !stat.Mode().IsRegular() {
			return result, errors.New("map input must be a directory or regular ZIP file")
		}
		destination, e := os.OpenFile(snapshotPath, os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			return result, e
		}
		n, copyErr := io.Copy(destination, io.LimitReader(source, maparchive.MaxBytes+1))
		closeErr := destination.Close()
		if copyErr != nil {
			err = copyErr
		} else if closeErr != nil {
			err = closeErr
		} else if n > maparchive.MaxBytes {
			err = errors.New("map ZIP exceeds 2GiB limit")
		}
	}
	if err != nil {
		return result, err
	}
	info, err := maparchive.Inspect(snapshotPath)
	if err != nil {
		return result, err
	}
	f, err := os.Open(snapshotPath)
	if err != nil {
		return result, err
	}
	defer f.Close()
	err = c.Store.PutImmutable(ctx, prefix+"map.zip", f, "application/zip", map[string]string{"sha256": info.SHA256})
	if errors.Is(err, storage.ErrAlreadyExists) {
		// A failed metadata publication can be resumed with identical ZIP bytes.
		head, headErr := c.Store.Head(ctx, prefix+"map.zip")
		if headErr != nil {
			return result, headErr
		}
		if head.Metadata["sha256"] != info.SHA256 || head.SizeBytes != info.SizeBytes {
			return result, errors.New("version already contains different map data; choose a new version")
		}
	} else if err != nil {
		return result, err
	}
	result = Metadata{Schema: Schema, Name: options.Name, Version: options.Version, Description: options.Description, Origin: options.Origin, Source: "s3://" + c.Store.Bucket + "/" + prefix + "map.zip", Info: info, RegisteredAt: time.Now().UTC()}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return Metadata{}, err
	}
	if err = c.Store.PutImmutable(ctx, prefix+"metadata.json", bytes.NewReader(data), "application/json", nil); err != nil {
		if errors.Is(err, storage.ErrAlreadyExists) {
			return Metadata{}, errors.New("map version was registered concurrently; choose a new version")
		}
		return Metadata{}, err
	}
	return result, nil
}

func (c *Catalog) validate(metadata Metadata) error {
	if err := validateIdentity(metadata.Name, metadata.Version); err != nil {
		return err
	}
	counts := metadata.Agents
	values := []int{counts.FireBrigade, counts.FireStation, counts.PoliceForce, counts.PoliceOffice, counts.AmbulanceTeam, counts.AmbulanceCentre}
	present := false
	for _, n := range values {
		if n < 0 {
			return errors.New("invalid map agent counts")
		}
		present = present || n > 0
	}
	if metadata.Schema != Schema || metadata.Version == "latest" || !experiment.ValidSHA256(metadata.SHA256) || metadata.SizeBytes <= 0 || metadata.SizeBytes > maparchive.MaxBytes || metadata.RegisteredAt.IsZero() || !present || metadata.Source != "s3://"+c.Store.Bucket+"/"+key(metadata.Name, metadata.Version)+"map.zip" {
		return errors.New("invalid registered map metadata")
	}
	return nil
}

func (c *Catalog) Inspect(ctx context.Context, name, version string) (Metadata, error) {
	if err := validateIdentity(name, version); err != nil {
		return Metadata{}, err
	}
	if version == "latest" {
		entries, err := c.List(ctx, name)
		if err != nil {
			return Metadata{}, err
		}
		if len(entries) == 0 {
			return Metadata{}, errors.New("registered map has no published versions")
		}
		// List is ordered by name, registration time, then version for ties.
		return entries[len(entries)-1], nil
	}
	var metadata Metadata
	if err := c.Store.GetJSON(ctx, key(name, version)+"metadata.json", &metadata); err != nil {
		return metadata, err
	}
	if err := c.validate(metadata); err != nil {
		return Metadata{}, err
	}
	if metadata.Name != name || metadata.Version != version {
		return Metadata{}, errors.New("map metadata identity mismatch")
	}
	return metadata, nil
}

func (c *Catalog) List(ctx context.Context, name string) ([]Metadata, error) {
	if name != "" && !experiment.ValidMapName(name) {
		return nil, errors.New("invalid map name")
	}
	prefix := "maps/"
	if name != "" {
		prefix += name + "/"
	}
	keys, err := c.Store.ListKeys(ctx, prefix, "/metadata.json")
	if err != nil {
		return nil, err
	}
	entries := []Metadata{}
	for _, path := range keys {
		parts := strings.Split(path, "/")
		if len(parts) != 4 || parts[0] != "maps" || parts[3] != "metadata.json" {
			continue
		}
		if parts[2] == "latest" {
			return nil, errors.New("latest cannot be a published map version")
		}
		entry, err := c.Inspect(ctx, parts[1], parts[2])
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if !a.RegisteredAt.Equal(b.RegisteredAt) {
			return a.RegisteredAt.Before(b.RegisteredAt)
		}
		return a.Version < b.Version
	})
	return entries, nil
}

func (c *Catalog) Resolve(ctx context.Context, input *experiment.Experiment) (*experiment.Experiment, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	resolved := *input
	resolved.Spec.Conditions.Maps = experiment.ExpandMaps(input.Spec.Conditions.Maps)
	type lookup struct{ name, version, display string }
	jobs := []lookup{}
	indexes := map[string]int{}
	for _, m := range resolved.Spec.Conditions.Maps {
		if m.Version == "" {
			continue
		}
		cacheKey := m.RegistryName() + ":" + m.Version
		if _, ok := indexes[cacheKey]; !ok {
			indexes[cacheKey] = len(jobs)
			jobs = append(jobs, lookup{m.RegistryName(), m.Version, m.Name})
		}
	}
	results := make([]Metadata, len(jobs))
	if err := operation.For(ctx, "Resolve map catalog", len(jobs), func(ctx context.Context, i int) error {
		job := jobs[i]
		metadata, err := c.Inspect(ctx, job.name, job.version)
		if err != nil {
			return fmt.Errorf("resolve map %s: %w", job.display, err)
		}
		results[i] = metadata
		return nil
	}); err != nil {
		return nil, err
	}
	for i, m := range resolved.Spec.Conditions.Maps {
		if m.Version == "" {
			continue
		}
		metadata := results[indexes[m.RegistryName()+":"+m.Version]]
		m.Resolved = &experiment.ResolvedMap{Name: metadata.Name, Version: metadata.Version, Source: metadata.Source, SHA256: metadata.SHA256}
		resolved.Spec.Conditions.Maps[i] = m
	}

	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	return &resolved, nil
}
