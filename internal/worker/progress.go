package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"simworkflow/internal/storage"
)

// Progress contains observations only, never the worker's signed capabilities.
// CompletedSteps counts completed kernel timesteps, not elapsed wall time.
type Progress struct {
	Memory         map[string]MemoryUsage `json:"memory,omitempty"`
	Activity       string                 `json:"activity,omitempty"`
	PodIP          string                 `json:"podIP,omitempty"`
	ExecutionID    string                 `json:"executionId"`
	RunID          string                 `json:"runId"`
	Stage          string                 `json:"stage"`
	Phase          string                 `json:"phase"`
	CompletedSteps int                    `json:"completedSteps"`
	TotalSteps     int                    `json:"totalSteps,omitempty"`
	Score          *float64               `json:"score,omitempty"`
	StartedAt      time.Time              `json:"startedAt"`
	UpdatedAt      time.Time              `json:"updatedAt"`
}

var completedStepPattern = regexp.MustCompile(`\bTimestep\s+(\d+)\s+complete\b`)
var totalStepPattern = regexp.MustCompile(`Termination condition: Timestep\s*>=\s*(\d+)`)

type progressTail struct {
	offset int64
	file   os.FileInfo
	value  Progress
}

func (t *progressTail) read(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	if t.file != nil && (!os.SameFile(t.file, info) || info.Size() < t.offset) {
		t.offset, t.value.CompletedSteps, t.value.TotalSteps, t.value.Score = 0, 0, 0, nil
	}
	t.file = info
	// Read the newest bounded window rather than draining a DEBUG-log backlog.
	// Keep an incomplete final line for the next poll.
	const window = int64(8 << 20)
	start := max(t.offset, info.Size()-window)
	data := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(data, start); err != nil {
		return
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return
	}
	if start > t.offset {
		// The window may begin inside a line; never parse that fragment.
		first := bytes.IndexByte(data, '\n')
		data = data[first+1 : end+1]
	} else {
		data = data[:end+1]
	}
	if t.value.TotalSteps == 0 {
		if match := totalStepPattern.FindSubmatch(data); match != nil {
			t.value.TotalSteps, _ = strconv.Atoi(string(match[1]))
		}
	}
	t.observeLatest(bytes.NewReader(data), int64(len(data)))
	t.offset = start + int64(end) + 1
}

func (t *progressTail) observe(line string) {
	if match := totalStepPattern.FindStringSubmatch(line); match != nil {
		if n, err := strconv.Atoi(match[1]); err == nil && n > 0 {
			t.value.TotalSteps = n
		}
	}
	if match := completedStepPattern.FindStringSubmatch(line); match != nil {
		if n, err := strconv.Atoi(match[1]); err == nil && n > t.value.CompletedSteps {
			t.value.CompletedSteps = n
		}
	}
	if matches := scorePattern.FindAllStringSubmatch(line, -1); len(matches) > 0 {
		if n, err := strconv.ParseFloat(matches[len(matches)-1][1], 64); err == nil {
			t.value.Score = &n
		}
	}
}

// Read the final observations even when shutdown messages exceed the live window.
func (t *progressTail) readLatest(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	if t.file != nil && (!os.SameFile(t.file, info) || info.Size() < t.offset) {
		t.value.CompletedSteps, t.value.TotalSteps, t.value.Score = 0, 0, nil
	}
	t.file = info
	t.observeLatest(f, info.Size())
	t.offset = info.Size()
}

func (t *progressTail) observeLatest(r io.ReaderAt, size int64) {
	var latest progressTail
	foundStep, foundScore := false, false
	scanLogBackwards(r, size, true, func(line []byte) bool {
		// Observe each field only once: older scores must not overwrite the
		// final score while searching for the last completed timestep.
		latest.value = Progress{}
		latest.observe(string(line))
		if !foundStep && latest.value.CompletedSteps > 0 {
			t.value.CompletedSteps = max(t.value.CompletedSteps, latest.value.CompletedSteps)
			foundStep = true
		}
		if !foundScore && latest.value.Score != nil {
			t.value.Score = latest.value.Score
			foundScore = true
		}
		if latest.value.TotalSteps > 0 && t.value.TotalSteps == 0 {
			t.value.TotalSteps = latest.value.TotalSteps
		}
		return foundStep && foundScore
	})
}

type progressReporter struct {
	activity atomic.Value
	notify   chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
	tail     progressTail
	root     string
	c        Config
}

func startProgress(ctx context.Context, root string, c Config) *progressReporter {
	p := &progressReporter{root: root, c: c, notify: make(chan struct{}, 1)}
	p.activity.Store("")
	p.tail.value = Progress{ExecutionID: c.ExecutionID, RunID: c.Run.ID, Stage: c.Stage, Phase: "Running", StartedAt: time.Now().UTC()}
	if c.Stage == Simulation {
		if ip := net.ParseIP(os.Getenv("SIMWORKFLOW_POD_IP")); ip != nil {
			p.tail.value.PodIP = ip.String()
		}
	}
	if c.Stage == Simulation && c.Run.Simulator.Parameters != nil && c.Run.Simulator.Parameters.Timesteps != nil {
		p.tail.value.TotalSteps = *c.Run.Simulator.Parameters.Timesteps
	}
	if c.ProgressUpload == nil {
		return p // Old rendered workflows remain supported.
	}
	ctx, p.cancel = context.WithCancel(ctx)
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			p.publish(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-p.notify:
			}
		}
	}()
	return p
}

func (p *progressReporter) setActivity(activity string) {
	p.activity.Store(activity)
	fmt.Printf("collector: %s\n", activity)
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *progressReporter) publish(ctx context.Context) {
	p.tail.value.Memory = readMemory(p.root, p.c)
	p.tail.value.Activity, _ = p.activity.Load().(string)
	if p.c.Stage == Simulation {
		if p.tail.value.Activity != "" || p.tail.value.Phase != "Running" {
			p.tail.readLatest(filepath.Join(p.root, "logs/simulation/kernel.log"))
		} else {
			p.tail.read(filepath.Join(p.root, "logs/simulation/kernel.log"))
		}
	}
	p.tail.value.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(p.tail.value)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := storage.Request(ctx, http.MethodPut, *p.c.ProgressUpload, bytes.NewReader(data))
	if err == nil {
		response.Body.Close()
	}
	// Progress is best effort. An unavailable observer must not fail the run.
}

func (p *progressReporter) finish(phase string) {
	if p.cancel == nil {
		return
	}
	p.cancel()
	<-p.done
	p.activity.Store("")
	p.tail.value.Phase = phase
	p.publish(context.Background())
}
