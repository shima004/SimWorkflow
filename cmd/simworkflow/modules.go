package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"simworkflow/internal/experiment"
	"simworkflow/internal/moduleconfig"
)

type moduleManifest struct {
	Experiment string       `json:"experiment"`
	Agent      string       `json:"agent"`
	Format     string       `json:"format"`
	Files      []moduleFile `json:"files"`
}

type moduleFile struct {
	Configuration experiment.Configuration `json:"configuration"`
	File          string                   `json:"file"`
}

func runModules(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("experiment modules", flag.ContinueOnError)
	flags.SetOutput(errOut)
	file := flags.String("f", "", "experiment YAML file (required)")
	agentName := flags.String("agent", "", "agent name in experiment (required)")
	base := flags.String("base", "", "base module configuration file (required)")
	format := flags.String("format", "", "module format: java or python (required)")
	destination := flags.String("output-dir", "", "new output directory; must not already exist (required)")
	maxRuns := flags.Int("max-runs", experiment.DefaultMaxRuns, "maximum number of expanded runs")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *file == "" || *agentName == "" || *base == "" || *destination == "" {
		return errors.New("specify -f FILE, --agent NAME, --base FILE, --format java|python and --output-dir DIR")
	}
	if *format != "java" && *format != "python" {
		return errors.New("module format must be java or python")
	}
	e, err := experiment.ReadFile(*file)
	if err != nil {
		return err
	}
	plan, err := experiment.BuildPlan(e, *maxRuns)
	if err != nil {
		return err
	}
	baseData, err := os.ReadFile(*base)
	if err != nil {
		return err
	}
	manifest := moduleManifest{Experiment: e.Metadata.Name, Agent: *agentName, Format: *format, Files: []moduleFile{}}
	data := map[string][]byte{}
	extension := ".cfg"
	if *format == "python" {
		extension = ".yaml"
	}
	seen := map[string]bool{}
	for _, run := range plan.Runs {
		c := run.Configuration
		if c.Agent != *agentName || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		filename := c.ID + extension
		rendered, err := moduleconfig.Render(baseData, *format, c.Modules)
		if err != nil {
			return fmt.Errorf("configuration %s: %w", c.ID, err)
		}
		data[filename] = rendered
		manifest.Files = append(manifest.Files, moduleFile{Configuration: c, File: filename})
	}
	if len(manifest.Files) == 0 {
		return fmt.Errorf("agent %q does not exist in experiment", *agentName)
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data["manifest.json"] = append(manifestData, '\n')
	// Render everything before creating output. Reserve a new directory to
	// avoid overwriting prior experiment inputs or results, including symlinks.
	if err := os.Mkdir(*destination, 0700); err != nil {
		return fmt.Errorf("create output directory (must not already exist): %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(*destination)
		}
	}()
	for filename, contents := range data {
		if err := os.WriteFile(filepath.Join(*destination, filename), contents, 0600); err != nil {
			return err
		}
	}
	complete = true
	_, err = fmt.Fprintf(out, "Generated %d %s module configurations and manifest.json in %s.\n", len(manifest.Files), *format, *destination)
	return err
}
