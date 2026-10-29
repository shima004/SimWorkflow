package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

// Run explicitly inside a memory-limited container to check real cgroup mounts.
func TestMemoryContainerCgroup(t *testing.T) {
	expected := os.Getenv("SIMWORKFLOW_TEST_MEMORY_LIMIT")
	if expected == "" {
		t.Skip("requires a container with an explicit memory limit")
	}
	groups, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	files := resolveMemoryFiles(string(groups), string(mounts))
	m, err := files.read(0)
	limit, parseErr := strconv.ParseUint(expected, 10, 64)
	if err != nil || parseErr != nil || m.UsageBytes == 0 || m.LimitBytes == nil || *m.LimitBytes != limit {
		t.Fatalf("container memory = %+v (%v), files = %+v", m, err, files)
	}
	t.Logf("usage=%d, peak=%d, limit=%d", m.UsageBytes, m.PeakBytes, *m.LimitBytes)
}

func TestMemoryCgroupResolution(t *testing.T) {
	for _, tc := range []struct{ name, groups, mounts, want string }{
		{"v2 private", "0::/", "1 2 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup rw", "/sys/fs/cgroup/memory.current"},
		{"v2 host namespace", "0::/kubepods/pod/container", "1 2 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup rw", "/sys/fs/cgroup/kubepods/pod/container/memory.current"},
		{"subtree mount", "0::/kubepods/pod/container", "1 2 0:1 /kubepods/pod /sys/fs/cgroup rw - cgroup2 cgroup rw", "/sys/fs/cgroup/container/memory.current"},
		{"v1", "3:cpu,cpuacct:/other\n7:memory:/docker/container", "1 2 0:1 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory", "/sys/fs/cgroup/memory/docker/container/memory.usage_in_bytes"},
		{"hybrid", "0::/unified\n7:memory:/docker/container", "1 2 0:1 / /sys/fs/cgroup/unified rw - cgroup2 cgroup rw\n2 3 0:2 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory", "/sys/fs/cgroup/memory/docker/container/memory.usage_in_bytes"},
		{"escaped mount", "0::/", `1 2 0:1 / /sys/cgroup\040space rw - cgroup2 cgroup rw`, "/sys/cgroup space/memory.current"},
		{"outside mounted subtree", "0::/other", "1 2 0:1 /container /sys/fs/cgroup rw - cgroup2 cgroup rw", ""},
		{"unavailable", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMemoryFiles(tc.groups, tc.mounts); got.usage != tc.want {
				t.Fatalf("resolved %q, want %q", got.usage, tc.want)
			}
		})
	}
}

func memoryFixture(t *testing.T) memoryFiles {
	t.Helper()
	dir := t.TempDir()
	f := memoryFiles{filepath.Join(dir, "usage"), filepath.Join(dir, "limit"), filepath.Join(dir, "peak")}
	for path, value := range map[string]string{f.usage: "104857600", f.limit: "2147483648", f.peak: "209715200"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestMemoryReadLimitsAndPeak(t *testing.T) {
	f := memoryFixture(t)
	m, err := f.read(0)
	if err != nil || m.UsageBytes != 100<<20 || m.PeakBytes != 200<<20 || m.LimitBytes == nil || *m.LimitBytes != 2<<30 {
		t.Fatalf("memory = %+v, %v", m, err)
	}
	for _, value := range []string{"max", "9223372036854771712"} {
		if err := os.WriteFile(f.limit, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(f.peak); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		m, err = f.read(300 << 20)
		if err != nil || m.LimitBytes != nil || m.PeakBytes != 300<<20 {
			t.Fatalf("unlimited or sampled peak: %+v, %v", m, err)
		}
	}
	if err := os.WriteFile(f.usage, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.read(0); err == nil {
		t.Fatal("invalid usage must be unavailable, never zero")
	}
}

func TestMemorySamplingAndProgressPublication(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	c := Config{ExecutionID: "execution", Stage: Precompute, Run: experiment.Run{ID: "run-one"}}
	files := memoryFixture(t)
	stop := sampleMemory(context.Background(), root, c, "server", files, 5*time.Millisecond)
	initial := readMemory(root, c)["server"]
	if initial.Finished || initial.UsageBytes != 100<<20 {
		t.Fatalf("initial: %+v", initial)
	}
	if err := os.WriteFile(files.usage, []byte("52428800"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current := readMemory(root, c)["server"]
		if current.UsageBytes == 50<<20 && !current.Finished {
			break
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("memory was not updated while the actor was running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	final := readMemory(root, c)["server"]
	if !final.Finished || final.UsageBytes != 50<<20 || final.PeakBytes != 200<<20 {
		t.Fatalf("final: %+v", final)
	}
	// Other stages, runs and executions must not accidentally reuse samples.
	for _, other := range []Config{{ExecutionID: "other", Stage: c.Stage, Run: c.Run}, {ExecutionID: c.ExecutionID, Stage: Simulation, Run: c.Run}, {ExecutionID: c.ExecutionID, Stage: c.Stage, Run: experiment.Run{ID: "other"}}} {
		if len(readMemory(root, other)) != 0 {
			t.Fatal("mismatched sample accepted")
		}
	}
	var mu sync.Mutex
	var published Progress
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if err := json.NewDecoder(r.Body).Decode(&published); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c.ProgressUpload = &storage.Transfer{URL: server.URL}
	p := startProgress(context.Background(), root, c)
	p.finish("Succeeded")
	mu.Lock()
	defer mu.Unlock()
	if published.Memory["server"].UsageBytes != final.UsageBytes || !published.Memory["server"].Finished {
		t.Fatalf("memory missing from final progress: %+v", published.Memory)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop = sampleMemory(ctx, root, c, "agent-fire-brigade", memoryFiles{}, time.Millisecond)
	stop() // Missing cgroups and cancellation must be harmless.
}
