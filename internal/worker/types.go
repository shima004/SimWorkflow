// Package worker runs the precompute and simulation processes inside a run Pod.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

const (
	Precompute = "precompute"
	Simulation = "simulation"
)

type Config struct {
	Stage              string            `json:"stage"`
	Deadline           time.Time         `json:"deadline"`
	MapSHA256          string            `json:"mapSHA256"`
	PrecomputeUpload   *storage.Transfer `json:"precomputeUpload,omitempty"`
	PrecomputeDownload *storage.Transfer `json:"precomputeDownload,omitempty"`
	ExecutionID        string            `json:"executionId"`
	Experiment         string            `json:"experiment"`
	Run                experiment.Run    `json:"run"`
	TimeoutSeconds     int64             `json:"timeoutSeconds"`
	Map                storage.Transfer  `json:"map"`
	Bundle             storage.Transfer  `json:"bundle"`
	Summary            storage.Transfer  `json:"summary"`
	ResultPrefix       string            `json:"resultPrefix"`
	Collect            []string          `json:"collect"`
}

type State struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type Summary struct {
	WorkflowPhase   string           `json:"workflowPhase,omitempty"`
	Stage           string           `json:"stage,omitempty"`
	Error           string           `json:"error,omitempty"`
	ExecutionID     string           `json:"executionId"`
	Experiment      string           `json:"experiment"`
	Run             experiment.Run   `json:"run"`
	Phase           string           `json:"phase"`
	Server          State            `json:"server"`
	Agent           State            `json:"agent"`
	Agents          map[string]State `json:"agents,omitempty"`
	PrecomputeFiles []string         `json:"precomputeFiles"`
	Score           *float64         `json:"score,omitempty"`
	ResultPrefix    string           `json:"resultPrefix"`
	FinishedAt      time.Time        `json:"finishedAt"`
}

func Load(root string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(root, "run.json"))
	if err != nil {
		return c, err
	}
	if json.Unmarshal(data, &c) != nil || c.TimeoutSeconds <= 0 || c.Run.ID == "" {
		return c, errors.New("invalid worker run configuration")
	}
	return c, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func statePath(root, name string) string { return filepath.Join(root, "state", name+".json") }

func readState(root, name string) (State, bool) {
	var state State
	data, err := os.ReadFile(statePath(root, name))
	if err != nil {
		return state, false
	}
	if json.Unmarshal(data, &state) != nil {
		return state, false
	}
	return state, true
}

func Run(ctx context.Context, root, actor string) (err error) {
	c, err := Load(root)
	if err != nil {
		return err
	}
	if c.Stage != Precompute && c.Stage != Simulation {
		return errors.New("worker requires a precompute or simulation stage")
	}
	ctx, cancel := context.WithDeadline(ctx, c.Deadline)
	defer cancel()
	if actor == "collect" {
		return collect(ctx, root, c)
	}
	role := ""
	for _, candidate := range experiment.Roles() {
		if actor == AgentActor(candidate) {
			role = candidate
		}
	}
	if actor != "server" && role == "" {
		return errors.New("worker actor must be server, a role-specific agent or collect")
	}
	defer func() {
		state := State{Success: err == nil}
		if err != nil {
			state.Error = err.Error()
		}
		if e := writeJSON(statePath(root, actor), state); err == nil && e != nil {
			err = e
		}
	}()
	if actor == "server" {
		return runServer(ctx, root, c)
	}
	return runAgent(ctx, root, c, role)
}
