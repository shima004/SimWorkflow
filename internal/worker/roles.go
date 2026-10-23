package worker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"simworkflow/internal/experiment"
)

func AgentActor(role string) string {
	switch role {
	case "fireBrigade":
		return "agent-fire-brigade"
	case "policeForce":
		return "agent-police-force"
	case "ambulanceTeam":
		return "agent-ambulance-team"
	}
	return ""
}

func countsForRole(counts []int, role string) []int {
	selected := make([]int, 6)
	for i, candidate := range experiment.Roles() {
		if candidate == role {
			copy(selected[2*i:2*i+2], counts[2*i:2*i+2])
		}
	}
	return selected
}

func rolePrecompute(root, role string) string {
	return filepath.Join(root, "precompute", role)
}

// agentStates reports failures immediately, while done requires every role to
// finish so collection never races a still-running agent container.
func agentStates(root, phase string) (State, map[string]State, bool) {
	aggregate := State{Success: true}
	states := map[string]State{}
	done := true
	var failures []string
	for _, role := range experiment.Roles() {
		name := AgentActor(role)
		if phase != "" {
			name = phase + "." + name
		}
		state, ok := readState(root, name)
		if !ok {
			done = false
			aggregate.Success = false
			continue
		}
		states[role] = state
		if !state.Success {
			aggregate.Success = false
			failures = append(failures, fmt.Sprintf("%s: %s", role, state.Error))
		}
	}
	aggregate.Error = strings.Join(failures, "; ")
	return aggregate, states, done
}

func waitAgents(ctx context.Context, root, phase string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, _, done := agentStates(root, phase)
		if state.Error != "" {
			return errors.New("agent role failed during " + phase)
		}
		if final, _, _ := agentStates(root, ""); final.Error != "" {
			return errors.New("agent role failed")
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("run deadline reached while waiting for agent roles")
		case <-ticker.C:
		}
	}
}
