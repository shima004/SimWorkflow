package reproduction

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"go.yaml.in/yaml/v3"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/imageio"
	"simworkflow/internal/maparchive"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

type ExportOptions struct {
	Workflow, Phase, Prefix, ExecutionID, Helper, ConfigStorage, Output, CLIVersion string
	IncludeLogs                                                                     bool
}

func Export(ctx context.Context, c config.Config, plan *experiment.Plan, meta Metadata, o ExportOptions, log io.Writer) error {
	if _, err := os.Lstat(o.Output); err == nil {
		return errors.New("output already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ValidatePlan(plan); err != nil {
		return err
	}
	if o.Phase != "Succeeded" && o.Phase != "Failed" && o.Phase != "Error" {
		return errors.New("export requires a finished workflow")
	}
	if meta.Schema == "" {
		meta = Metadata{Schema: Schema, CLIVersion: "unknown", CLICommit: "unknown", Platform: imageio.Platform, HelperImage: o.Helper}
		meta.ConfigStorage = o.ConfigStorage
	}
	if meta.ConfigStorage == "" {
		meta.ConfigStorage = "inline"
	}
	if meta.ConfigStorage != "s3" && meta.ConfigStorage != "s3-bundle" && meta.ConfigStorage != "inline" {
		return errors.New("unsupported worker configuration format")
	}
	if meta.PlanSHA256 != "" && meta.PlanSHA256 != PlanSHA256(plan) {
		return errors.New("saved reproduction metadata does not match execution plan")
	}
	if meta.Schema != Schema || meta.Platform != imageio.Platform {
		return errors.New("unsupported reproduction metadata")
	}
	if !Pinned(meta.HelperImage) {
		return errors.New("the original helper digest is unavailable; cannot create a complete reproduction ZIP")
	}
	required := map[string]bool{meta.HelperImage: true}
	for _, run := range plan.Runs {
		if !Pinned(run.Simulator.Image) || !Pinned(run.Configuration.Image) {
			return errors.New("original image digests are unavailable for this older experiment; current tags cannot prove past image contents")
		}
		if !experiment.ValidSHA256(run.Map.ExpectedSHA256()) {
			return errors.New("original map SHA-256 is unavailable; cannot create a complete reproduction ZIP")
		}
		required[run.Simulator.Image] = true
		required[run.Configuration.Image] = true
	}
	directory, err := os.MkdirTemp("", "simworkflow-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	manifest := Manifest{Schema: Schema, Workflow: o.Workflow, Phase: o.Phase, CreatedAt: time.Now().UTC(), Metadata: meta, Maps: map[string]string{}}
	if err := jsonFile(filepath.Join(directory, "plan.json"), plan); err != nil {
		return err
	}
	if meta.Definition != nil {
		data, err := yaml.Marshal(meta.Definition)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, "experiment.yaml"), data, 0600); err != nil {
			return err
		}
	}
	instructions := fmt.Sprintf("# Reproduction bundle\n\nExported by SimWorkflow %s. plan.json is the authoritative expanded execution plan.\n\nConfigure your own Argo, S3 and registry, then run:\n\n    simworkflow experiment reproduce --bundle BUNDLE.zip --name reproduced-experiment\n\nAll image blobs and maps are included. Inputs are verified before uploading. Precompute is rerun; archived precompute/data.zip is a reference copy bound to its old execution and deadline. Missing reference outputs are listed in manifest.json. Logs are included only when requested and available. The original YAML may be absent for older experiments. No service credentials, signed transfer URLs or original Workflow/worker configuration are included. Cluster configuration and credentials are supplied separately. Scores may vary with execution environment.\n", o.CLIVersion)
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte(instructions), 0600); err != nil {
		return err
	}
	images, err := layout.Write(filepath.Join(directory, "images"), empty.Index)
	if err != nil {
		return err
	}
	client := imageio.New(c)
	saved := map[string]bool{}
	for ref := range required {
		fmt.Fprintln(log, "Archiving image", ref)
		image, pinned, err := client.Fetch(ctx, ref)
		if err != nil {
			return err
		}
		if manifest.Metadata.Images == nil {
			manifest.Metadata.Images = map[string]string{}
		}
		manifest.Metadata.Images[ref] = pinned
		digest, err := image.Digest()
		if err != nil {
			return err
		}
		if !saved[digest.String()] {
			if err := images.AppendImage(image); err != nil {
				return errors.New("could not archive image blobs")
			}
			local, err := images.Image(digest)
			if err != nil {
				return errors.New("could not read archived image")
			}
			if err := ValidateImage(ctx, local); err != nil {
				return err
			}
			saved[digest.String()] = true
		}
	}
	store := storage.New(c)
	for _, run := range plan.Runs {
		sha := run.Map.ExpectedSHA256()
		if _, ok := manifest.Maps[sha]; ok {
			continue
		}
		transfer, err := store.Download(ctx, run.Map.Location())
		if err != nil {
			return err
		}
		response, err := storage.Request(ctx, "GET", transfer, nil)
		if err != nil {
			return err
		}
		file := "maps/" + sha + ".zip"
		target := filepath.Join(directory, filepath.FromSlash(file))
		err = copyFile(ctx, target, response.Body, maparchive.MaxBytes)
		response.Body.Close()
		if err != nil {
			return err
		}
		info, err := maparchive.Inspect(target)
		if err != nil || info.SHA256 != sha {
			return errors.New("map snapshot differs from the original experiment")
		}
		manifest.Maps[sha] = file
	}
	// Keep old run IDs in reference outputs even if an old index digest required
	// a platform-specific ID in the canonical plan above.
	for _, run := range plan.Runs {
		for _, stage := range []string{worker.Precompute, worker.Simulation} {
			key := o.Prefix + "/" + run.ID
			if stage == worker.Precompute {
				key += "/precompute"
			}
			local := "results/" + run.ID + "/" + stage
			var summary worker.Summary
			err := store.GetJSON(ctx, key+"/summary.json", &summary)
			if storage.IsNotFound(err) {
				manifest.MissingResults = append(manifest.MissingResults, local+"/summary.json")
			} else if err != nil {
				return err
			} else {
				if summary.Run.ID != run.ID || summary.Run.Configuration.ID != run.Configuration.ID || summary.Stage != stage || summary.ExecutionID != o.ExecutionID {
					return errors.New("reference summary does not match original execution")
				}
				summary.Run.Configuration.Groups = run.Configuration.Groups
				path := filepath.Join(directory, filepath.FromSlash(local), "summary.json")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					return err
				}
				if err := jsonFile(path, summary); err != nil {
					return err
				}
			}
			body, err := store.Open(ctx, key+"/bundle.tar.gz")
			if storage.IsNotFound(err) {
				manifest.MissingResults = append(manifest.MissingResults, local+"/modules-and-config")
			} else if err != nil {
				return err
			} else {
				err = extractReference(ctx, body, filepath.Join(directory, filepath.FromSlash(local)), o.IncludeLogs)
				body.Close()
				if err != nil {
					return err
				}
			}
		}
		body, err := store.Open(ctx, o.Prefix+"/"+run.ID+"/precompute/data.zip")
		local := "results/" + run.ID + "/precompute/data.zip"
		if storage.IsNotFound(err) {
			manifest.MissingResults = append(manifest.MissingResults, local)
		} else if err != nil {
			return err
		} else {
			err = copyFile(ctx, filepath.Join(directory, filepath.FromSlash(local)), body, maparchive.MaxBytes)
			body.Close()
			if err != nil {
				return err
			}
		}
	}
	if len(manifest.MissingResults) > 0 {
		fmt.Fprintf(log, "%d reference artifacts unavailable; recorded in manifest.json.\n", len(manifest.MissingResults))
	}
	return WriteZIP(ctx, directory, o.Output, &manifest)
}

func copyFile(ctx context.Context, path string, reader io.Reader, limit int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(contextReader{ctx, reader}, limit+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n > limit {
		os.Remove(path)
		return errors.New("file copy failed or exceeded size limit")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func extractReference(ctx context.Context, reader io.Reader, destination string, logs bool) error {
	g, err := gzip.NewReader(contextReader{ctx, reader})
	if err != nil {
		return errors.New("invalid reference result archive")
	}
	defer g.Close()
	t := tar.NewReader(g)
	var total int64
	count := 0
	for {
		header, err := t.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("invalid reference result archive")
		}
		count++
		if count > 100000 || header.Typeflag != tar.TypeReg || !safePath(header.Name) || header.Size < 0 || header.Size > maxFile || header.Size > maxFile-total {
			return errors.New("unsafe or oversized reference archive")
		}
		total += header.Size
		if strings.HasPrefix(header.Name, "modules/") || strings.HasPrefix(header.Name, "scenario/config/") || logs && strings.HasPrefix(header.Name, "logs/") {
			if err := copyFile(ctx, filepath.Join(destination, filepath.FromSlash(header.Name)), t, header.Size); err != nil {
				return err
			}
		}
	}
}
