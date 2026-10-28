package main

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

type stepSample struct {
	started, updated, advanced time.Time
	steps                      int
	secondsPerStep             float64
}

type runEstimator struct{ samples map[string]stepSample }

func newRunEstimator() *runEstimator { return &runEstimator{samples: map[string]stepSample{}} }

type runEstimate struct {
	at                time.Time
	remaining         time.Duration
	known             bool
	reason            string
	samples, parallel int
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	i := len(values) / 2
	if len(values)%2 == 1 {
		return values[i]
	}
	return (values[i-1] + values[i]) / 2
}

func (e *runEstimator) observe(rows []runProgressRow, plan *experiment.Plan, phase string, now time.Time) runEstimate {
	result := runEstimate{at: now, reason: "collecting step samples"}
	if completedPhase(phase) {
		result.known = true
		return result
	}
	if plan == nil || len(rows) == 0 {
		return result
	}
	openWork, validRows := false, 0
	for _, r := range rows {
		if r.runID == "" {
			continue
		}
		validRows++
		finished := r.phase == "Failed" || r.phase == "Error" || r.phase == "Not run" || (r.stage == worker.Simulation && r.phase == "Succeeded")
		stepsDone := r.stage == worker.Simulation && r.phase == "Running" && r.totalSteps > 0 && r.completedSteps >= r.totalSteps
		if !finished && !stepsDone {
			openWork = true
		}
	}
	if validRows > 0 && !openWork {
		result.reason = "finishing and saving results"
		return result
	}
	var stepTimes, preTimes, startupTimes []float64
	active := 0
	stalled := false
	for _, r := range rows {
		if r.runID == "" {
			continue
		}
		if r.phase == "Running" {
			active++
		}
		if !r.precomputeStartedAt.IsZero() && r.precomputeFinishedAt.After(r.precomputeStartedAt) {
			preTimes = append(preTimes, r.precomputeFinishedAt.Sub(r.precomputeStartedAt).Seconds())
		}
		if r.stage != worker.Simulation {
			continue
		}
		previous, ok := e.samples[r.runID]
		if !ok || !previous.started.Equal(r.startedAt) || r.completedSteps < previous.steps {
			previous = stepSample{started: r.startedAt, updated: r.progressUpdatedAt, steps: r.completedSteps}
		}
		if !r.progressUpdatedAt.IsZero() && r.progressUpdatedAt.After(previous.updated) {
			if r.completedSteps > previous.steps && !previous.updated.IsZero() {
				rate := r.progressUpdatedAt.Sub(previous.updated).Seconds() / float64(r.completedSteps-previous.steps)
				if previous.secondsPerStep > 0 {
					rate = .3*rate + .7*previous.secondsPerStep
				}
				previous.secondsPerStep, previous.advanced = rate, r.progressUpdatedAt
			}
			if previous.updated.IsZero() || r.completedSteps > previous.steps {
				previous.updated, previous.steps = r.progressUpdatedAt, r.completedSteps
			}
		}
		e.samples[r.runID] = previous
		if r.phase == "Running" && previous.secondsPerStep > 0 && !sampleUsable(previous, now) {
			stalled = true
		}
		if r.phase == "Succeeded" && r.finishedAt.After(r.startedAt) && !r.startedAt.IsZero() {
			steps := r.completedSteps
			if steps <= 0 {
				steps = r.totalSteps
			}
			if steps > 0 {
				stepTimes = append(stepTimes, r.finishedAt.Sub(r.startedAt).Seconds()/float64(steps))
			}
		} else if r.phase == "Running" && sampleUsable(previous, now) {
			stepTimes = append(stepTimes, previous.secondsPerStep)
			if !r.progressStartedAt.IsZero() && r.progressUpdatedAt.After(r.progressStartedAt) {
				startupTimes = append(startupTimes, max(0, r.progressUpdatedAt.Sub(r.progressStartedAt).Seconds()-float64(r.completedSteps)*previous.secondsPerStep))
			}
		}
	}
	result.samples = len(stepTimes)
	if active == 0 {
		result.reason = "waiting for execution"
		return result
	}
	result.parallel = min(active, plan.MaxParallelRuns)
	if result.parallel <= 0 {
		return result
	}
	secondsPerStep, precompute, startup := median(stepTimes), median(preTimes), median(startupTimes)
	if secondsPerStep <= 0 {
		if stalled {
			result.reason = "step progress stalled or unavailable"
		}
		return result
	}
	var total, longest float64
	for _, r := range rows {
		if r.runID == "" || r.phase == "Failed" || r.phase == "Error" || r.phase == "Not run" || (r.stage == worker.Simulation && r.phase == "Succeeded") {
			continue
		}
		if r.totalSteps <= 0 {
			result.reason = "total steps unavailable"
			return result
		}
		rate := secondsPerStep
		steps := r.totalSteps
		extra := startup
		if r.stage == worker.Simulation && r.phase == "Running" {
			if r.progressUpdatedAt.IsZero() || now.Sub(r.progressUpdatedAt) > 30*time.Second {
				result.reason = "simulation progress unavailable"
				return result
			}
			if s := e.samples[r.runID]; sampleUsable(s, now) {
				rate = s.secondsPerStep
			} else if s.secondsPerStep > 0 {
				result.reason = "step progress stalled or unavailable"
				return result
			}
			steps = max(0, r.totalSteps-r.completedSteps)
			if r.completedSteps > 0 {
				extra = 0
			}
		}
		work := float64(steps)*rate + extra
		if r.stage != worker.Simulation && r.precomputeFinishedAt.IsZero() {
			if precompute <= 0 {
				result.reason = "collecting precompute samples"
				return result
			}
			remaining := precompute
			if r.stage == worker.Precompute && r.phase == "Running" && !r.startedAt.IsZero() {
				remaining = max(0, remaining-now.Sub(r.startedAt).Seconds())
			}
			work += remaining
		}
		total += work
		longest = max(longest, work)
	}
	seconds := max(longest, total/float64(result.parallel))
	if seconds <= 0 {
		result.reason = "finishing and saving results"
		return result
	}
	// Bound conversion for exceptionally long plans rather than overflow Duration.
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds >= float64(math.MaxInt64)/float64(time.Second) {
		result.reason = "estimate exceeds duration range"
		return result
	}
	result.remaining = time.Duration(seconds * float64(time.Second))
	result.known = true
	return result
}

func sampleUsable(s stepSample, now time.Time) bool {
	if s.secondsPerStep <= 0 || s.advanced.IsZero() {
		return false
	}
	return now.Sub(s.advanced).Seconds() <= max(30, 5*s.secondsPerStep)
}

func progressOverview(rows []runProgressRow, estimate runEstimate, phase string, started, now time.Time, width int) []string {
	if rows == nil {
		return nil
	}
	counts := map[string]int{}
	total := 0
	for _, r := range rows {
		if r.runID != "" {
			state := r.phase
			state = strings.Join(strings.Fields(screenText(state)), " ")
			if state == "" {
				state = "Unknown"
			}
			counts[state]++
			total++
		}
	}
	order := []string{"Pending", "Running", "Succeeded", "Failed", "Error", "Not run"}
	var other []string
	for state := range counts {
		if !slices.Contains(order, state) {
			other = append(other, state)
		}
	}
	slices.Sort(other)
	order = append(order, other...)
	tokens := []string{fmt.Sprintf("STATE (total %d):", total)}
	for _, state := range order {
		tokens = append(tokens, fmt.Sprintf("%s:%d", state, counts[state]))
	}
	lines := wrapOverview(tokens, width)
	reason := estimate.reason
	if reason == "" {
		reason = "collecting step samples"
	}
	text := "Estimate: -- (" + reason + ")"
	if completedPhase(phase) {
		text = "Estimate: finished"
	} else if estimate.known {
		if now.Sub(estimate.at) > 30*time.Second {
			text = "Estimate: -- (status data is stale)"
		} else {
			remaining := max(time.Second, estimate.remaining-max(time.Duration(0), now.Sub(estimate.at)))
			text = "Estimate: remaining ~" + remaining.Round(time.Second).String()
			if !started.IsZero() {
				text += " | total ~" + (max(time.Duration(0), now.Sub(started)) + remaining).Round(time.Second).String()
			}
			text += fmt.Sprintf(" | samples:%d parallel:%d", estimate.samples, estimate.parallel)
		}
	}
	return append(lines, wrapOverview(strings.Fields(text), width)...)
}

func wrapOverview(tokens []string, width int) []string {
	if width <= 0 {
		return []string{strings.Join(tokens, " ")}
	}
	lines := []string{}
	line := ""
	for _, token := range tokens {
		if line != "" && ansi.StringWidth(line+" "+token) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += token
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
