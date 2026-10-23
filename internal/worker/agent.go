package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"simworkflow/internal/moduleconfig"
)

// Counts use the Java launcher's order: FB, FS, PF, PO, AT, AC.
func scenarioCounts(path string) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	counts := make([]int, 6)
	decoder := xml.NewDecoder(f)
	indices := map[string]int{"firebrigade": 0, "firestation": 1, "policeforce": 2, "policeoffice": 3, "ambulanceteam": 4, "ambulancecentre": 5, "ambulancecenter": 5}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid scenario XML")
		}
		if element, ok := token.(xml.StartElement); ok {
			if index, exists := indices[strings.ToLower(element.Name.Local)]; exists {
				counts[index]++
			}
		}
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return nil, errors.New("scenario has no controllable agents")
	}
	return counts, nil
}

func prepareAgent(root string, c Config) (string, error) {
	runtime := c.Run.Configuration.Runtime
	extension := "cfg"
	precompute := "precomp_data"
	if runtime == "python" {
		extension = "yaml"
		precompute = "precompute"
	}
	base := filepath.Join("/app/config", "module."+extension)
	data, err := os.ReadFile(base)
	if err != nil {
		return "", errors.New("agent image lacks its base module configuration")
	}
	rendered, err := moduleconfig.Render(data, runtime, c.Run.Configuration.Modules)
	if err != nil {
		return "", err
	}
	output := filepath.Join(root, "modules", "module."+extension)
	if err := os.WriteFile(output, rendered, 0644); err != nil {
		return "", err
	}
	if runtime == "python" {
		path := "/app/config/launcher.yaml"
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		var launcher map[string]any
		if yaml.Unmarshal(data, &launcher) != nil {
			return "", errors.New("invalid agent launcher YAML")
		}
		set := func(keys []string, value any) {
			node := launcher
			for _, key := range keys[:len(keys)-1] {
				next, ok := node[key].(map[string]any)
				if !ok {
					next = map[string]any{}
					node[key] = next
				}
				node = next
			}
			node[keys[len(keys)-1]] = value
		}
		set([]string{"adf", "agent", "moduleconfig", "filename"}, output)
		set([]string{"adf", "agent", "precompute", "dir_name"}, filepath.Join(root, "precompute"))
		set([]string{"team", "name"}, c.Run.Configuration.Agent)
		encoded, err := yaml.Marshal(launcher)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, encoded, 0644); err != nil {
			return "", err
		}
	} else {
		path := filepath.Join("/app", precompute)
		if info, err := os.Lstat(path); err == nil {
			if info.IsDir() {
				if err := os.Remove(path); err != nil {
					return "", errors.New("agent image contains nonempty precompute data; use a clean image")
				}
			} else {
				return "", errors.New("agent precompute path already exists")
			}
		}
		if err := os.Symlink(filepath.Join(root, "precompute"), path); err != nil {
			return "", err
		}
	}
	return output, nil
}

func agentCommand(c Config, phase, module string, counts []int, root string) []string {
	counts = append([]int(nil), counts...)
	if phase == "precompute" {
		for i, n := range counts {
			if i%2 == 1 {
				counts[i] = 0
			} else if n > 0 {
				counts[i] = 1
			}
		}
	}
	if c.Run.Configuration.Runtime == "python" {
		args := []string{"python", "main.py", "--host", "localhost", "--port", "27931", "-a", strconv.Itoa(counts[4]), "-f", strconv.Itoa(counts[0]), "-p", strconv.Itoa(counts[2]), "-ac", strconv.Itoa(counts[5]), "-fs", strconv.Itoa(counts[1]), "-po", strconv.Itoa(counts[3]), "--logger-file-name", filepath.Join(root, "logs", phase, "agent-internal.log"), "--logger-file-level", "INFO", "--logger-stream-level", "INFO"}
		if phase == "precompute" {
			args = append(args, "--precompute")
		}
		return args
	}
	parts := make([]string, len(counts))
	for i, n := range counts {
		parts[i] = strconv.Itoa(n)
	}
	pre := "false"
	if phase == "precompute" {
		pre = "true"
	}
	arguments := []string{"adf.impl.DefaultLoader", "-t", strings.Join(parts, ","), "-s", "localhost:27931", "-mc", module, "-pre", pre, "-tn", c.Run.Configuration.Agent}
	return []string{"./gradlew", "--offline", "--no-daemon", "launch", "--args=" + strings.Join(arguments, " ")}
}

func runAgent(ctx context.Context, root string, c Config) error {
	module, err := prepareAgent(root, c)
	if err != nil {
		return err
	}
	counts, err := scenarioCounts(filepath.Join(root, "scenario/map/scenario.xml"))
	if err != nil {
		return err
	}
	for _, phase := range []string{"precompute", "simulation"} {
		if phase == "simulation" {
			state, err := waitState(ctx, root, "precompute.server")
			if err != nil {
				return err
			}
			if !state.Success {
				return errors.New("precompute server phase failed")
			}
		}
		// A failed server must abort the wait instead of waiting for readiness.
		for {
			if _, ok := readState(root, phase+".ready"); ok {
				break
			}
			if state, ok := readState(root, "server"); ok && !state.Success {
				return errors.New("server failed before agent startup")
			}
			select {
			case <-ctx.Done():
				return errors.New("agent startup deadline reached")
			case <-time.After(100 * time.Millisecond):
			}
		}
		p, err := start(root, phase, "agent", "/app", agentCommand(c, phase, module, counts, root), false)
		if err != nil {
			return err
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		finished := false
		for !finished {
			select {
			case err := <-p.done:
				if err != nil {
					ticker.Stop()
					return fmt.Errorf("%s agent failed; inspect agent.log", phase)
				}
				finished = true
			case <-ctx.Done():
				p.stop()
				ticker.Stop()
				return errors.New("agent run deadline reached")
			case <-ticker.C:
				if state, ok := readState(root, "server"); ok && !state.Success {
					p.stop()
					ticker.Stop()
					return errors.New("server failed during agent execution")
				}
				if phase == "simulation" {
					if _, ok := readState(root, "simulation.server"); ok {
						select {
						case err := <-p.done:
							if err != nil {
								ticker.Stop()
								return errors.New("simulation agent failed after server completion")
							}
						case <-time.After(5 * time.Second):
							p.stop()
						}
						finished = true
					}
				}
			}
		}
		ticker.Stop()
		if phase == "precompute" {
			files, err := precomputeFiles(root)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return errors.New("precompute agent produced no data")
			}
		}
		if phase == "simulation" {
			if err := verifyResumed(root, c.Run.Configuration.Runtime, counts); err != nil {
				return err
			}
		}
		if err := writeJSON(statePath(root, phase+".agent"), State{Success: true}); err != nil {
			return err
		}
		fmt.Printf("agent: %s completed\n", phase)
	}
	return nil
}

var pythonPlatoon = regexp.MustCompile(`Connected to kernel: (PlatoonAmbulance|PlatoonFire|PlatoonPolice) \(`)
var pythonResumed = regexp.MustCompile(`mode:\s*(1|Mode\.PRECOMPUTED|PRECOMPUTED)\)`)

func verifyResumed(root, runtime string, counts []int) error {
	name := "agent.log"
	if runtime == "python" {
		name = "agent-internal.log"
	}
	f, err := os.Open(filepath.Join(root, "logs/simulation", name))
	if err != nil {
		return err
	}
	defer f.Close()
	expected := counts[0] + counts[2] + counts[4]
	connected := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if runtime == "python" {
			var entry struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) != nil || !pythonPlatoon.MatchString(entry.Event) {
				continue
			}
			connected++
			if !pythonResumed.MatchString(entry.Event) {
				return errors.New("Python platoon did not resume precomputed data")
			}
		} else if strings.Contains(line, "Connected -") && strings.Contains(line, ".platoon.Platoon") {
			connected++
			if !strings.Contains(line, "(PRECOMPUTED)") {
				return errors.New("Java platoon did not resume precomputed data")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("could not verify agent resume log")
	}
	if expected <= 0 || connected != expected {
		return errors.New("not all expected platoon agents confirmed precomputed mode")
	}
	return nil
}
