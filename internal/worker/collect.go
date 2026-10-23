package worker

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"simworkflow/internal/storage"
)

func precomputeFiles(root string) ([]string, error) {
	return precomputeFilesIn(filepath.Join(root, "precompute"))
}

func precomputeFilesIn(directory string) ([]string, error) {
	files := []string{}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("precompute data contains a symlink")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("precompute data must contain regular files")
		}
		if info.Size() > 0 {
			relative, _ := filepath.Rel(directory, path)
			files = append(files, filepath.ToSlash(relative))
		}
		return nil
	})
	slices.Sort(files)
	return files, err
}

var scorePattern = regexp.MustCompile(`(?i)\bscore\s*[:=]\s*([-+]?[0-9]+(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?)`)

func readScore(root string) *float64 {
	f, err := os.Open(filepath.Join(root, "logs/simulation/kernel.log"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var score *float64
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		matches := scorePattern.FindAllSubmatch(scanner.Bytes(), -1)
		if len(matches) > 0 {
			value, err := strconv.ParseFloat(string(matches[len(matches)-1][1]), 64)
			if err == nil {
				score = &value
			}
		}
	}
	return score
}

func collect(ctx context.Context, root string, c Config) (collectErr error) {
	reporter := startProgress(ctx, root, c)
	defer func() {
		phase := "Succeeded"
		if collectErr != nil {
			phase = "Failed"
		}
		reporter.finish(phase)
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var server, agent State
	var agents map[string]State
	var serverDone, agentDone bool
waiting:
	for {
		server, serverDone = readState(root, "server")
		agent, agents, agentDone = agentStates(root, "")
		if serverDone && agentDone {
			break
		}
		select {
		case <-ctx.Done():
			break waiting
		case <-ticker.C:
		}
	}
	if !serverDone {
		server = State{Error: "server did not finish before the run deadline"}
	}
	if !agentDone {
		agent = State{Error: "agent did not finish before the run deadline"}
	}
	files, err := precomputeFiles(root)
	if err != nil {
		files = []string{}
	}
	summary := Summary{Stage: c.Stage, ExecutionID: c.ExecutionID, Experiment: c.Experiment, Run: c.Run, Phase: "Failed", Server: server, Agent: agent, Agents: agents, PrecomputeFiles: files, ResultPrefix: c.ResultPrefix, FinishedAt: time.Now().UTC()}
	if server.Success && agent.Success {
		summary.Phase = "Succeeded"
	}
	if slices.Contains(c.Collect, "score") {
		summary.Score = readScore(root)
	}
	// Run deadline cancellation must not prevent saving the failure report.
	uploadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if c.Stage == Precompute && summary.Phase == "Succeeded" {
		handoff := filepath.Join(root, "precompute.zip")
		err := makeHandoff(root, handoff, c)
		if err == nil {
			if c.PrecomputeUpload == nil {
				err = errors.New("missing precompute upload")
			} else {
				err = uploadFile(uploadCtx, handoff, *c.PrecomputeUpload)
			}
		}
		if err != nil {
			summary.Phase = "Failed"
			summary.Error = "could not publish precompute data"
		}
	}
	if err := writeJSON(filepath.Join(root, "result.json"), summary); err != nil {
		return err
	}
	archive := filepath.Join(root, "bundle.tar.gz")
	if err := bundle(root, archive, c.Collect); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	response, err := storage.Request(uploadCtx, http.MethodPut, c.Bundle, f)
	f.Close()
	if err != nil {
		return errors.New("result bundle upload failed")
	}
	response.Body.Close()
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	response, err = storage.Request(uploadCtx, http.MethodPut, c.Summary, strings.NewReader(string(data)))
	if err != nil {
		return errors.New("result summary upload failed")
	}
	response.Body.Close()
	if summary.Phase != "Succeeded" {
		return errors.New("experiment failed; result summary and logs were saved")
	}
	return nil
}

func bundle(root, destination string, collect []string) (err error) {
	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	defer func() {
		for _, close := range []func() error{tw.Close, gz.Close, f.Close} {
			if e := close(); err == nil && e != nil {
				err = e
			}
		}
	}()
	for _, name := range []string{"result.json", "precompute", "modules", "state", "scenario/config", "logs"} {
		err = filepath.WalkDir(filepath.Join(root, name), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if name == "logs" {
				isAgent := strings.Contains(entry.Name(), "agent")
				if isAgent && !slices.Contains(collect, "agentLogs") {
					return nil
				}
				if !isAgent && !slices.Contains(collect, "simulatorLogs") {
					return nil
				}
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("result artifacts must contain regular files")
			}
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name, _ = filepath.Rel(root, path)
			header.Name = filepath.ToSlash(header.Name)
			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			source, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, source)
			closeErr := source.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func uploadFile(ctx context.Context, path string, transfer storage.Transfer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	response, err := storage.Request(ctx, http.MethodPut, transfer, f)
	if err != nil {
		return err
	}
	return response.Body.Close()
}
