// Package artifacts downloads selected experiment artifacts without credentials
// or signed capabilities in the resulting manifest.
package artifacts

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

var prefixPattern = regexp.MustCompile(`^experiments/[a-f0-9]{32}$`)
var runPattern = regexp.MustCompile(`^run-[a-f0-9]{32}$`)
var categories = []string{"plan", "summary", "progress", "simulatorLogs", "agentLogs", "precompute", "modules", "config", "bundle"}

func ValidPrefix(prefix string) bool { return prefixPattern.MatchString(prefix) }

type Source interface {
	Open(context.Context, string) (io.ReadCloser, error)
}
type Options struct {
	Workflow, Phase, Output string
	Include, Runs, Files    []string
	Stage                   string
}
type File struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Size   int64  `json:"sizeBytes"`
	SHA256 string `json:"sha256"`
}
type Report struct {
	Workflow       string    `json:"workflow"`
	WorkflowPhase  string    `json:"workflowPhase"`
	ResultPrefix   string    `json:"resultPrefix"`
	Include        []string  `json:"include"`
	Stage          string    `json:"stage"`
	Runs           []string  `json:"runs"`
	RequestedFiles []string  `json:"requestedFiles,omitempty"`
	Downloaded     []File    `json:"downloaded"`
	Unavailable    []string  `json:"unavailable"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type downloader struct {
	source          Source
	ctx             context.Context
	output          string
	selected        map[string]bool
	report          *Report
	requiredMissing bool
}

func safePath(name string) bool {
	return fs.ValidPath(name) && filepath.IsLocal(name) && name != "." && !strings.Contains(name, "\\")
}

// Download uses a new output directory, never overwriting existing files.
// Completed files and a manifest remain available if a later object fails.
func Download(ctx context.Context, source Source, plan *experiment.Plan, prefix string, o Options) (report Report, err error) {
	if !ValidPrefix(prefix) {
		return report, errors.New("invalid experiment result prefix")
	}
	if o.Output == "" {
		return report, errors.New("specify an output directory")
	}
	if o.Stage == "" {
		o.Stage = "all"
	}
	if o.Stage != "all" && o.Stage != "precompute" && o.Stage != "simulation" {
		return report, errors.New("stage must be all, precompute or simulation")
	}
	selected := map[string]bool{}
	all := false
	for _, name := range o.Include {
		if name == "all" {
			all = true
			for _, c := range categories {
				selected[c] = true
			}
			continue
		}
		if name == "score" {
			name = "summary"
		}
		valid := false
		for _, c := range categories {
			valid = valid || name == c
		}
		if !valid {
			return report, fmt.Errorf("unknown include %q; choose all, plan, summary, score, progress, simulatorLogs, agentLogs, precompute, modules, config or bundle", name)
		}
		selected[name] = true
	}
	for _, name := range o.Files {
		if !safePath(name) {
			return report, errors.New("file must be an exact relative bundle path without traversal")
		}
	}
	if len(selected) == 0 && len(o.Files) == 0 {
		return report, errors.New("select at least one include or file")
	}
	if plan == nil || len(plan.Runs) == 0 {
		return report, errors.New("experiment plan has no runs")
	}
	known := map[string]bool{}
	for _, r := range plan.Runs {
		if !runPattern.MatchString(r.ID) || known[r.ID] {
			return report, errors.New("invalid or duplicate run ID in experiment plan")
		}
		known[r.ID] = true
	}
	wanted := map[string]bool{}
	for _, id := range o.Runs {
		if !known[id] {
			return report, fmt.Errorf("unknown run ID %q", id)
		}
		wanted[id] = true
	}
	runs := []experiment.Run{}
	for _, r := range plan.Runs {
		if len(wanted) == 0 || wanted[r.ID] {
			runs = append(runs, r)
		}
	}
	report = Report{Workflow: o.Workflow, WorkflowPhase: o.Phase, ResultPrefix: prefix, Include: o.Include, Stage: o.Stage, RequestedFiles: o.Files, Downloaded: []File{}, Unavailable: []string{}, CreatedAt: time.Now().UTC()}
	for _, r := range runs {
		report.Runs = append(report.Runs, r.ID)
	}
	// Mkdir reserves the output directory and rejects existing files/directories.
	if err = os.MkdirAll(filepath.Dir(o.Output), 0700); err != nil {
		return report, err
	}
	if err = os.Mkdir(o.Output, 0700); err != nil {
		return report, fmt.Errorf("create new output directory: %w", err)
	}
	d := downloader{source: source, ctx: ctx, output: o.Output, selected: selected, report: &report}
	defer func() {
		if err != nil {
			report.Error = err.Error()
		}
		data, e := json.MarshalIndent(report, "", "  ")
		if e == nil {
			e = os.WriteFile(filepath.Join(o.Output, "download.json"), append(data, '\n'), 0600)
		}
		if e != nil {
			err = errors.Join(err, fmt.Errorf("save download manifest: %w", e))
		}
	}()
	if selected["plan"] {
		if err = d.object(prefix+"/plan.json", "plan.json"); err != nil {
			return report, err
		}
	}
	for _, run := range runs {
		if selected["precompute"] {
			if err = d.object(prefix+"/"+run.ID+"/precompute/data.zip", run.ID+"/precompute/data.zip"); err != nil {
				return report, err
			}
		}
		for _, stage := range []string{"precompute", "simulation"} {
			if o.Stage != "all" && stage != o.Stage {
				continue
			}
			key := prefix + "/" + run.ID
			if stage == "precompute" {
				key += "/precompute"
			}
			local := run.ID + "/" + stage
			for _, name := range []string{"summary", "progress"} {
				if selected[name] {
					if err = d.object(key+"/"+name+".json", local+"/"+name+".json"); err != nil {
						return report, err
					}
				}
			}
			files := []string{}
			for _, name := range o.Files {
				if strings.HasPrefix(name, "logs/") && !strings.HasPrefix(name, "logs/"+stage+"/") {
					continue
				}
				files = append(files, name)
			}
			extract := len(files) > 0 || selected["simulatorLogs"] || selected["agentLogs"] || selected["modules"] || selected["config"]
			if extract || selected["bundle"] {
				if err = d.archive(key+"/bundle.tar.gz", local, stage, files, extract); err != nil {
					return report, err
				}
			}
		}
	}
	if len(report.Downloaded) == 0 {
		return report, errors.New("no requested artifacts are available yet; see download.json")
	}
	if (!all && len(report.Unavailable) > 0) || d.requiredMissing {
		return report, errors.New("some requested artifacts are unavailable; available files were saved; see download.json")
	}
	return report, nil
}

func (d *downloader) save(name, source string, body io.Reader) (err error) {
	if !safePath(name) {
		return errors.New("unsafe artifact output path")
	}
	path := filepath.Join(d.output, filepath.FromSlash(name))
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), body)
	closeErr := f.Close()
	if copyErr != nil {
		return errors.New("artifact transfer interrupted; incomplete file removed")
	}
	if closeErr != nil {
		return closeErr
	}
	d.report.Downloaded = append(d.report.Downloaded, File{Path: name, Source: source, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

func (d *downloader) object(key, local string) error {
	r, err := d.source.Open(d.ctx, key)
	if storage.IsNotFound(err) {
		d.report.Unavailable = append(d.report.Unavailable, key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", key, err)
	}
	defer r.Close()
	return d.save(local, key, r)
}

func (d *downloader) archive(key, local, stage string, files []string, extract bool) error {
	if !extract {
		return d.object(key, local+"/bundle.tar.gz")
	}
	r, err := d.source.Open(d.ctx, key)
	if storage.IsNotFound(err) {
		d.requiredMissing = d.requiredMissing || len(files) > 0
		d.report.Unavailable = append(d.report.Unavailable, key)
		return nil
	}
	if err != nil {
		return err
	}
	defer r.Close()
	// Stage a bundle once even when both raw and extracted forms are selected.
	f, err := os.CreateTemp(d.output, ".bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return errors.New("bundle transfer interrupted")
	}
	if closeErr != nil {
		return closeErr
	}
	f, err = os.Open(f.Name())
	if err != nil {
		return err
	}
	defer f.Close()
	if d.selected["bundle"] {
		if err = d.save(local+"/bundle.tar.gz", key, f); err != nil {
			return err
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("invalid result bundle gzip")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	found := map[string]bool{}
	matched := map[string]bool{}
	var expanded int64
	for count := 0; ; count++ {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		entry, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return errors.New("invalid or truncated result bundle")
		}
		name := strings.TrimSuffix(entry.Name, "/")
		if !safePath(name) || seen[name] {
			return errors.New("bundle contains an unsafe or duplicate path")
		}
		seen[name] = true
		if count >= 100000 || entry.Size < 0 || entry.Size > (16<<30)-expanded {
			return errors.New("bundle exceeds 100000 entries or 16GiB expanded size")
		}
		expanded += entry.Size
		if entry.Typeflag == tar.TypeDir {
			continue
		}
		if entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA {
			return errors.New("bundle contains a link or non-regular file")
		}
		choose := false
		for _, requested := range files {
			if name == requested {
				found[name] = true
				choose = true
			}
		}
		kind := ""
		switch {
		case strings.HasPrefix(name, "logs/"+stage+"/"):
			kind = "simulatorLogs"
			if strings.Contains(filepath.Base(name), "agent") {
				kind = "agentLogs"
			}
		case strings.HasPrefix(name, "modules/"):
			kind = "modules"
		case strings.HasPrefix(name, "scenario/config/"):
			kind = "config"
		}
		if d.selected[kind] {
			choose = true
			matched[kind] = true
		}
		if choose {
			output := name
			if strings.HasPrefix(name, "logs/"+stage+"/") {
				output = "logs/" + strings.TrimPrefix(name, "logs/"+stage+"/")
			}
			if err = d.save(local+"/"+output, key+"#"+name, tr); err != nil {
				return err
			}
		}
	}
	// Read through the gzip trailer so corruption is not reported as success.
	if _, err = io.Copy(io.Discard, gz); err != nil {
		return errors.New("invalid result bundle checksum")
	}
	for _, name := range files {
		if !found[name] {
			d.requiredMissing = true
			d.report.Unavailable = append(d.report.Unavailable, key+"#"+name)
		}
	}
	for _, kind := range []string{"simulatorLogs", "agentLogs", "modules", "config"} {
		if d.selected[kind] && !matched[kind] {
			d.report.Unavailable = append(d.report.Unavailable, key+"#"+kind)
		}
	}
	return nil
}
