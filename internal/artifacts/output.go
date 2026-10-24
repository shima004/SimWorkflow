package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"simworkflow/internal/experiment"
)

// openOutput accepts only directories created by downloads of this experiment.
// Root confines subsequent reads and writes even if a subdirectory is a symlink.
func openOutput(path, workflow, prefix string) (*os.Root, *Report, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, err
	}
	existing := false
	if err := os.Mkdir(path, 0700); err != nil {
		if !os.IsExist(err) {
			return nil, nil, err
		}
		existing = true
		info, e := os.Lstat(path)
		if e != nil {
			return nil, nil, e
		}
		if !info.IsDir() {
			return nil, nil, errors.New("output must be a directory, not a file or symlink")
		}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.Root, *Report, error) { root.Close(); return nil, nil, err }
	lock, err := root.OpenFile(".download.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(errors.New("output directory is in use; another download may be running (.download.lock)"))
	}
	lock.Close()
	failLocked := func(err error) (*os.Root, *Report, error) { root.Remove(".download.lock"); return fail(err) }
	if !existing {
		return root, nil, nil
	}
	info, err := root.Lstat("download.json")
	if err != nil || !info.Mode().IsRegular() {
		return failLocked(errors.New("existing output must contain a regular download.json from this experiment"))
	}
	f, err := root.Open("download.json")
	if err != nil {
		return failLocked(err)
	}
	var previous Report
	err = json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&previous)
	f.Close()
	if err != nil {
		return failLocked(errors.New("existing download.json is invalid"))
	}
	if previous.Workflow != workflow || previous.ResultPrefix != prefix {
		return failLocked(errors.New("output belongs to a different experiment; choose another --output"))
	}
	seen := map[string]bool{}
	for _, file := range previous.Downloaded {
		if !safePath(file.Path) || seen[file.Path] || file.Size < 0 || !experiment.ValidSHA256(file.SHA256) || !strings.HasPrefix(file.Source, prefix+"/") {
			return failLocked(errors.New("existing download.json has invalid file records"))
		}
		seen[file.Path] = true
	}
	return root, &previous, nil
}

func rootMkdirAll(root *os.Root, path string) error {
	if path == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		current = filepath.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil {
			if !os.IsExist(err) {
				return err
			}
			info, e := root.Lstat(current)
			if e != nil {
				return e
			}
			if !info.IsDir() {
				return errors.New("artifact parent must be a directory, not a file or symlink")
			}
		}
	}
	return nil
}

func (d *downloader) skipExisting(name, source string) (bool, error) {
	info, err := d.root.Lstat(filepath.FromSlash(name))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	record, tracked := d.existing[name]
	if !tracked || record.Source != source || !info.Mode().IsRegular() {
		return false, fmt.Errorf("refusing to replace untracked file or link: %s", name)
	}
	f, err := d.root.Open(filepath.FromSlash(name))
	if err != nil {
		return false, err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	f.Close()
	if err != nil {
		return false, err
	}
	if size != record.Size || hex.EncodeToString(h.Sum(nil)) != record.SHA256 {
		return false, fmt.Errorf("local file differs from download manifest: %s; choose another --output to preserve local changes", name)
	}
	for _, old := range d.report.Skipped {
		if old == name {
			return true, nil
		}
	}
	d.report.Skipped = append(d.report.Skipped, name)
	return true, nil
}

func (d *downloader) remember(file File) {
	for i, old := range d.report.Downloaded {
		if old.Path == file.Path {
			d.report.Downloaded[i] = file
			d.report.Added++
			return
		}
	}
	d.report.Downloaded = append(d.report.Downloaded, file)
	d.report.Added++
}

func saveManifest(root *os.Root, path string, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if info, err := root.Lstat("download.json"); err == nil && !info.Mode().IsRegular() {
		return errors.New("download.json must be a regular file")
	}
	// Write a complete private file before replacing the previous manifest.
	f, err := os.CreateTemp(path, ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(path, "download.json"))
}
