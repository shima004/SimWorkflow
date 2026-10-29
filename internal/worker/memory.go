package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"simworkflow/internal/experiment"
)

// MemoryUsage measures the entire container cgroup, including child processes
// and charged file cache. A missing limit means unlimited or unavailable.
type MemoryUsage struct {
	UsageBytes uint64    `json:"usageBytes"`
	PeakBytes  uint64    `json:"peakBytes"`
	LimitBytes *uint64   `json:"limitBytes,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Finished   bool      `json:"finished,omitempty"`
}

type memoryRecord struct {
	ExecutionID string `json:"executionId"`
	RunID       string `json:"runId"`
	Stage       string `json:"stage"`
	Actor       string `json:"actor"`
	MemoryUsage
}

type memoryFiles struct{ usage, limit, peak string }

// Resolve the process's own cgroup rather than assuming the mount root is the
// container. This also supports runtimes which use the host cgroup namespace.
func resolveMemoryFiles(cgroups, mounts string) memoryFiles {
	// In hybrid setups memory belongs to exactly one hierarchy; an unrelated
	// v2 mount may exist without the memory controller enabled.
	v1Memory := false
	for _, line := range strings.Split(cgroups, "\n") {
		group := strings.SplitN(line, ":", 3)
		if len(group) == 3 && containsController(group[1], "memory") {
			v1Memory = true
		}
	}
	for _, line := range strings.Split(cgroups, "\n") {
		group := strings.SplitN(line, ":", 3)
		if len(group) != 3 {
			continue
		}
		v2 := group[0] == "0" && group[1] == ""
		if v2 && v1Memory {
			continue
		}
		if !v2 && !containsController(group[1], "memory") {
			continue
		}
		for _, line := range strings.Split(mounts, "\n") {
			parts := strings.SplitN(line, " - ", 2)
			if len(parts) != 2 {
				continue
			}
			left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
			if len(left) < 5 || len(right) < 3 {
				continue
			}
			if (v2 && right[0] != "cgroup2") || (!v2 && (right[0] != "cgroup" || !containsController(right[2], "memory"))) {
				continue
			}
			rel, err := filepath.Rel(mountPath(left[3]), filepath.Clean(group[2]))
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			dir := filepath.Join(mountPath(left[4]), rel)
			if v2 {
				return memoryFiles{filepath.Join(dir, "memory.current"), filepath.Join(dir, "memory.max"), filepath.Join(dir, "memory.peak")}
			}
			return memoryFiles{filepath.Join(dir, "memory.usage_in_bytes"), filepath.Join(dir, "memory.limit_in_bytes"), filepath.Join(dir, "memory.max_usage_in_bytes")}
		}
	}
	return memoryFiles{}
}

func containsController(list, name string) bool {
	for _, value := range strings.Split(list, ",") {
		if value == name {
			return true
		}
	}
	return false
}

func mountPath(path string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(path)
}

func memoryNumber(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func (f memoryFiles) read(previousPeak uint64) (MemoryUsage, error) {
	usage, err := memoryNumber(f.usage)
	if err != nil {
		return MemoryUsage{}, err
	}
	peak, _ := memoryNumber(f.peak) // memory.peak is absent on older kernels.
	m := MemoryUsage{UsageBytes: usage, PeakBytes: max(usage, previousPeak, peak), UpdatedAt: time.Now().UTC()}
	if limit, err := memoryNumber(f.limit); err == nil && limit < 1<<60 {
		m.LimitBytes = &limit // v1 represents unlimited using a huge sentinel.
	}
	return m, nil
}

func startMemorySampling(ctx context.Context, root string, c Config, actor string) func() {
	groups, _ := os.ReadFile("/proc/self/cgroup")
	mounts, _ := os.ReadFile("/proc/self/mountinfo")
	return sampleMemory(ctx, root, c, actor, resolveMemoryFiles(string(groups), string(mounts)), 5*time.Second)
}

func sampleMemory(ctx context.Context, root string, c Config, actor string, files memoryFiles, interval time.Duration) func() {
	record := memoryRecord{ExecutionID: c.ExecutionID, RunID: c.Run.ID, Stage: c.Stage, Actor: actor}
	sample := func(finished bool) {
		m, err := files.read(record.PeakBytes)
		if err != nil {
			return // Telemetry must never fail a simulation.
		}
		record.MemoryUsage, record.Finished = m, finished
		_ = writeJSON(statePath(root, "memory-"+actor), record)
	}
	sample(false)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sample(false)
			case <-ctx.Done():
				return
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		sample(true)
	}
}

func readMemory(root string, c Config) map[string]MemoryUsage {
	values := make(map[string]MemoryUsage)
	actors := []string{"server"}
	for _, role := range experiment.Roles() {
		actors = append(actors, AgentActor(role))
	}
	for _, actor := range actors {
		data, err := os.ReadFile(statePath(root, "memory-"+actor))
		var r memoryRecord
		if err != nil || json.Unmarshal(data, &r) != nil || r.ExecutionID != c.ExecutionID || r.RunID != c.Run.ID || r.Stage != c.Stage || r.Actor != actor || r.UpdatedAt.IsZero() {
			continue
		}
		values[actor] = r.MemoryUsage
	}
	return values
}
