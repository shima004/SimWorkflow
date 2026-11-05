package artifacts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"simworkflow/internal/experiment"
)

// IndexFile tracks generated metadata separately from downloaded artifacts.
type IndexFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"sizeBytes"`
	SHA256 string `json:"sha256"`
}

type RunIndexEntry struct {
	experiment.Run
	Directory string   `json:"directory"`
	Files     []string `json:"files"`
	Source    Attempt  `json:"source"`
}

type RunIndex struct {
	Schema       string          `json:"schema"`
	Workflow     string          `json:"workflow"`
	ResultPrefix string          `json:"resultPrefix"`
	Runs         []RunIndexEntry `json:"runs"`
}

func checkIndexFiles(root *os.Root, records []IndexFile) error {
	tracked := map[string]IndexFile{}
	for _, file := range records {
		if (file.Path != "runs.csv" && file.Path != "runs.json") || file.Size < 0 || !experiment.ValidSHA256(file.SHA256) {
			return errors.New("invalid generated index record in download.json")
		}
		if _, duplicate := tracked[file.Path]; duplicate {
			return errors.New("duplicate generated index record in download.json")
		}
		tracked[file.Path] = file
	}
	for _, name := range []string{"runs.csv", "runs.json"} {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		record, known := tracked[name]
		if !known || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace untracked index file or link: %s", name)
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if n != record.Size || hex.EncodeToString(h.Sum(nil)) != record.SHA256 {
			return fmt.Errorf("local index differs from download manifest: %s; choose another --output to preserve local changes", name)
		}
	}
	return nil
}

func (d *downloader) writeRunIndexes(plan *experiment.Plan) error {
	if err := checkIndexFiles(d.root, d.report.IndexFiles); err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, id := range d.report.Runs {
		selected[id] = true
	}
	files := map[string][]string{}
	for _, file := range d.report.Downloaded {
		id := strings.SplitN(file.Path, "/", 2)[0]
		if runPattern.MatchString(id) {
			selected[id] = true
			files[id] = append(files[id], file.Path)
		}
	}
	index := RunIndex{Schema: "simworkflow/download-runs/v1", Workflow: d.report.Workflow, ResultPrefix: d.report.ResultPrefix, Runs: []RunIndexEntry{}}
	for _, run := range plan.Runs {
		if !selected[run.ID] {
			continue
		}
		source := Attempt{Workflow: d.report.Workflow, ResultPrefix: d.report.ResultPrefix, ExecutionID: strings.TrimPrefix(d.report.ResultPrefix, "experiments/"), Phase: d.report.WorkflowPhase}
		if d.report.WithRetries {
			source = d.report.Attempts[run.ID]
		}
		slices.Sort(files[run.ID])
		paths := files[run.ID]
		if paths == nil {
			paths = []string{}
		}
		index.Runs = append(index.Runs, RunIndexEntry{Run: run, Directory: run.ID, Files: paths, Source: source})
	}
	if err := d.writeIndex("runs.csv", func(w io.Writer) error { return writeRunCSV(w, index.Runs) }); err != nil {
		return err
	}
	return d.writeIndex("runs.json", func(w io.Writer) error {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(index)
	})
}

func writeRunCSV(out io.Writer, entries []RunIndexEntry) error {
	groups, modules := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		for key := range entry.Configuration.Groups {
			groups[key] = true
		}
		for key := range entry.Configuration.Modules {
			modules[key] = true
		}
	}
	keys := func(values map[string]bool) []string {
		var result []string
		for key := range values {
			result = append(result, key)
		}
		slices.Sort(result)
		return result
	}
	groupKeys, moduleKeys := keys(groups), keys(modules)
	header := []string{"run_id", "directory", "agent", "map", "map_version", "seed", "configuration_id", "source_workflow", "source_execution_id"}
	for _, key := range groupKeys {
		header = append(header, "group."+key)
	}
	for _, key := range moduleKeys {
		header = append(header, "module."+key)
	}
	header = append(header, "files")
	w := csv.NewWriter(out)
	if err := w.Write(header); err != nil {
		return err
	}
	for _, entry := range entries {
		name, version := entry.Map.Name, entry.Map.Version
		if entry.Map.Resolved != nil {
			if name == "" {
				name = entry.Map.Resolved.Name
			}
			if version == "" {
				version = entry.Map.Resolved.Version
			}
		}
		row := []string{entry.ID, entry.Directory, entry.Configuration.Agent, name, version, strconv.FormatInt(entry.Seed, 10), entry.Configuration.ID, entry.Source.Workflow, entry.Source.ExecutionID}
		for _, key := range groupKeys {
			row = append(row, entry.Configuration.Groups[key])
		}
		for _, key := range moduleKeys {
			row = append(row, entry.Configuration.Modules[key])
		}
		paths, err := json.Marshal(entry.Files)
		if err != nil {
			return err
		}
		row = append(row, string(paths))
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func (d *downloader) writeIndex(name string, encode func(io.Writer) error) (err error) {
	temporary := ".index-" + rand.Text()
	f, err := d.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer d.root.Remove(temporary)
	h := sha256.New()
	encodeErr := encode(io.MultiWriter(f, h))
	info, statErr := f.Stat()
	closeErr := f.Close()
	if err := errors.Join(encodeErr, statErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(d.output, temporary), filepath.Join(d.output, name)); err != nil {
		return err
	}
	record := IndexFile{Path: name, Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}
	for i, old := range d.report.IndexFiles {
		if old.Path == name {
			d.report.IndexFiles[i] = record
			return nil
		}
	}
	d.report.IndexFiles = append(d.report.IndexFiles, record)
	return nil
}
