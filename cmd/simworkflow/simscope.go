package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"simworkflow/internal/argo"
	"simworkflow/internal/artifacts"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
	"simworkflow/internal/worker"
)

type viewerOpened struct {
	err error
}

func simScopeCommand(ctx context.Context, c config.Config) func(string, experiment.Run) tea.Cmd {
	return func(workflow string, run experiment.Run) tea.Cmd {
		return func() tea.Msg {
			u, err := prepareSimScope(ctx, c, workflow, run)
			if err == nil {
				err = openBrowser(ctx, u)
			}
			return viewerOpened{err: err}
		}
	}
}

func simScopeURL(base string, run experiment.Run, params url.Values) (string, error) {
	if base == "" {
		return "", errors.New("SimScope URL is not configured; set simScope.url in config.yaml or SIMSCOPE_URL")
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("SIMSCOPE_URL must be an http(s) URL without credentials, query, or fragment")
	}
	params.Set("team", run.Configuration.Agent)
	mapName := run.Map.Name
	if run.Map.Version != "" {
		mapName += "@" + run.Map.Version
	}
	params.Set("map", mapName)
	u.RawQuery = params.Encode()
	return u.String(), nil
}

// Refresh Argo at keypress time: a retry may have changed the live Pod since
// the last screen refresh. Never use the precompute server as a viewer target.
func prepareSimScope(ctx context.Context, c config.Config, workflow string, run experiment.Run) (string, error) {
	if !statusRunID.MatchString(run.ID) {
		return "", errors.New("invalid selected run")
	}
	if _, err := simScopeURL(c.SimScopeURL, run, url.Values{}); err != nil {
		return "", err
	}
	snapshot, err := argo.New(c).Get(ctx, workflow)
	if err != nil {
		return "", err
	}
	if snapshot.Metadata.Annotations["simworkflow.io/run-sequences"] == "v1" {
		prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
		if !artifacts.ValidPrefix(prefix) {
			return "", errors.New("workflow has no valid SimWorkflow result prefix")
		}
		var plan experiment.Plan
		if err := readProgressJSON(ctx, storage.New(c), prefix+"/plan.json", &plan); err != nil {
			return "", errors.New("simulation sequence plan unavailable; try again later")
		}
		attachSequenceNodes(snapshot, &plan)
	}
	node, found := latestStageNode(snapshot, run.ID, worker.Simulation)
	if !found || node.Phase == "Pending" {
		return "", errors.New("simulation is not ready; press w after simulation starts")
	}
	if node.Phase == "Running" {
		host := node.PodIP
		if net.ParseIP(host) == nil {
			prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
			if !artifacts.ValidPrefix(prefix) {
				return "", errors.New("workflow has no valid SimWorkflow result prefix")
			}
			var progress worker.Progress
			if err := storage.New(c).GetJSON(ctx, prefix+"/"+run.ID+"/progress.json", &progress); err != nil {
				return "", errors.New("live progress unavailable; try w again after startup")
			}
			if progress.RunID != run.ID || progress.Stage != worker.Simulation || progress.ExecutionID == "" || progress.ExecutionID != snapshot.Metadata.Annotations["simworkflow.io/execution-id"] || progress.StartedAt.Before(node.StartedAt) || progress.UpdatedAt.Before(node.StartedAt) {
				return "", errors.New("current attempt's live progress is not ready; try w again")
			}
			if progress.Phase != "Running" {
				return "", errors.New("simulation is finishing; try w again after results are saved")
			}
			if net.ParseIP(progress.PodIP) == nil {
				return "", errors.New("live IP unavailable; use a viewer-enabled worker")
			}
			host = progress.PodIP
		}
		return simScopeURL(c.SimScopeURL, run, url.Values{"host": {host}, "port": {"27931"}})
	}
	if node.Phase != "Succeeded" && node.Phase != "Failed" && node.Phase != "Error" {
		return "", errors.New("simulation is not ready for viewing")
	}
	prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
	if !artifacts.ValidPrefix(prefix) {
		return "", errors.New("workflow has no valid SimWorkflow result prefix")
	}
	store := storage.New(c)
	var summary worker.Summary
	if err := store.GetJSON(ctx, prefix+"/"+run.ID+"/summary.json", &summary); err != nil {
		return "", errors.New("simulation result is not available yet; try w again later")
	}
	if summary.Run.ID != run.ID || summary.Stage != worker.Simulation || summary.ExecutionID == "" || summary.ExecutionID != snapshot.Metadata.Annotations["simworkflow.io/execution-id"] || summary.FinishedAt.IsZero() || summary.FinishedAt.Before(node.StartedAt) {
		return "", errors.New("simulation result does not match the current attempt")
	}
	// Timestamp partitions prevent reusing logs from a failed, earlier attempt.
	key := fmt.Sprintf("%s/%s/viewer/%d/rescue.log.7z", prefix, run.ID, summary.FinishedAt.UnixNano())
	exists, err := store.Exists(ctx, key)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := publishViewerLog(ctx, store, snapshot.Metadata.Name, prefix, run, key); err != nil {
			return "", err
		}
	}
	transfer, err := store.SignDownload(ctx, store.Bucket, key)
	if err != nil {
		return "", err
	}
	// Intentionally deliver this single-object capability to SimScope, without
	// putting credentials or a signed URL into terminal output or local manifests.
	return simScopeURL(c.SimScopeURL, run, url.Values{"url": {transfer.URL}})
}

func publishViewerLog(ctx context.Context, store *storage.Store, workflow, prefix string, run experiment.Run, key string) error {
	dir, err := os.MkdirTemp("", "simworkflow-viewer-*")
	if err != nil {
		return errors.New("could not create viewer temporary directory")
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "download")
	_, err = artifacts.Download(ctx, store, &experiment.Plan{Runs: []experiment.Run{run}}, prefix, artifacts.Options{Workflow: workflow, Output: output, Stage: worker.Simulation, Runs: []string{run.ID}, Files: []string{"logs/simulation/rescue.log.7z"}})
	if err != nil {
		return errors.New("could not extract simulation rescue.log.7z; check that simulatorLogs was collected")
	}
	f, err := os.Open(filepath.Join(output, run.ID, worker.Simulation, "logs", "rescue.log.7z"))
	if err != nil {
		return errors.New("simulation rescue.log.7z is not available")
	}
	defer f.Close()
	transfer, err := store.Upload(ctx, key)
	if err != nil {
		return err
	}
	response, err := storage.Request(ctx, http.MethodPut, transfer, f)
	if err != nil {
		return errors.New("could not save viewer log to S3")
	}
	response.Body.Close()
	return nil
}

func openBrowser(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", target)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	default:
		// WSL can open the Windows default browser directly, without cmd.exe
		// interpreting ampersands or percent escapes in a signed URL.
		for _, candidate := range []struct {
			name string
			args []string
		}{
			{"xdg-open", []string{target}},
			{"wslview", []string{target}},
			{"rundll32.exe", []string{"url.dll,FileProtocolHandler", target}},
			{"gio", []string{"open", target}},
			{"sensible-browser", []string{target}},
		} {
			if path, err := exec.LookPath(candidate.name); err == nil {
				cmd = exec.CommandContext(ctx, path, candidate.args...)
				break
			}
		}
	}
	if cmd == nil {
		return errors.New("no browser opener found; install xdg-open or configure WSL browser")
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("could not open browser; check the default browser and desktop session")
	}
	return nil
}
