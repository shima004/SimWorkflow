package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"simworkflow/internal/argo"
	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

func failedRunPhase(phase string) bool {
	return phase == "Failed" || phase == "Error"
}

func findRunWrapperFailure(snapshot *argo.Snapshot, runID string, failures map[string]argo.Node) (string, argo.Node, bool) {
	root := runID
	if snapshot.RunNodeNames != nil {
		root = snapshot.RunNodeNames[runID]
		if root == "" {
			return "", argo.Node{}, false
		}
	}
	path := snapshot.Metadata.Name + "." + root
	for i, stage := range []string{worker.Simulation, worker.Precompute} {
		if node, ok := failures[fmt.Sprintf("%s[%d].%s", path, 1-i, stage)]; ok {
			return stage, node, true
		}
	}
	node, ok := failures[path]
	return "-", node, ok
}

var failureURL = regexp.MustCompile(`(?i)\b(?:https?|s3)://[^\s"'<>]+`)
var failureSecret = regexp.MustCompile(`(?i)x-amz-|authorization|bearer\s|password|secret.?access.?key|token\s*[=:]|simworkflow_run`)

func appendFailureMessage(lines []string, source, message string) []string {
	message = strings.Join(strings.Fields(screenText(ansi.Strip(message))), " ")
	if message == "" {
		return lines
	}
	message = failureURL.ReplaceAllString(message, "[URL hidden]")
	if failureSecret.MatchString(message) {
		message = "[message hidden because it may contain credentials or run configuration]"
	}
	return append(lines, "  "+source+": "+ansi.Truncate(message, 800, "…"))
}

func appendSummaryFailures(lines []string, summary worker.Summary) []string {
	lines = appendFailureMessage(lines, "Worker", summary.Error)
	lines = appendFailureMessage(lines, "server", summary.Server.Error)
	beforeAgents := len(lines)
	for _, role := range experiment.Roles() {
		lines = appendFailureMessage(lines, worker.AgentActor(role), summary.Agents[role].Error)
	}
	if len(lines) == beforeAgents || summary.Agent.Error == "agent did not finish before the run deadline" {
		lines = appendFailureMessage(lines, "Agents", summary.Agent.Error)
	}
	return lines
}

func failureDetails(row runProgressRow) []string {
	if !failedRunPhase(row.phase) {
		return nil
	}
	stage := row.stage
	if stage == "-" {
		stage = "unknown"
	}
	lines := []string{fmt.Sprintf("Failure | Stage: %s | State: %s", stage, row.phase)}
	if len(row.failure) == 0 {
		lines = append(lines, "  No failure message available from Argo or the saved summary.")
	}
	lines = append(lines, row.failure...)
	return append(lines, "")
}
