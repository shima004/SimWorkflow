package main

import (
	"fmt"
	"time"

	"simworkflow/internal/worker"
)

var memoryActors = []string{"server", "agent-fire-brigade", "agent-police-force", "agent-ambulance-team"}
var memoryNames = []string{"Server", "Fire brigade (+ station)", "Police force (+ office)", "Ambulance team (+ centre)"}

func memoryBytes(n uint64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.2fGiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.1fMiB", float64(n)/(1<<20))
}

func memoryStatus(row runProgressRow, value worker.MemoryUsage, now time.Time) string {
	if value.Finished || row.phase == "Succeeded" || row.phase == "Failed" || row.phase == "Error" {
		return "last"
	}
	if now.Sub(value.UpdatedAt) > 20*time.Second {
		return "stale"
	}
	return "live"
}

func memoryCell(row runProgressRow, actor string, now time.Time) string {
	value, ok := row.memory[actor]
	if !ok || value.UpdatedAt.IsZero() {
		return "-"
	}
	text := memoryBytes(value.UsageBytes)
	switch memoryStatus(row, value, now) {
	case "stale":
		text += "~"
	case "last":
		text += "*"
	}
	return text
}

func memoryDetails(row runProgressRow, now time.Time) []string {
	lines := []string{"Container memory (includes child processes and file cache)"}
	for i, actor := range memoryActors {
		value, ok := row.memory[actor]
		if !ok || value.UpdatedAt.IsZero() {
			lines = append(lines, "  "+memoryNames[i]+": unavailable")
			continue
		}
		limit := "unlimited/unknown"
		if value.LimitBytes != nil {
			limit = memoryBytes(*value.LimitBytes)
		}
		age := max(time.Duration(0), now.Sub(value.UpdatedAt).Round(time.Second))
		lines = append(lines, fmt.Sprintf("  %s: %s / %s | peak %s | %s, %s ago", memoryNames[i], memoryBytes(value.UsageBytes), limit, memoryBytes(value.PeakBytes), memoryStatus(row, value, now), age))
	}
	return append(lines, "")
}
