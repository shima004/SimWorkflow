package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type process struct {
	cmd  *exec.Cmd
	done chan error
	log  *os.File
}

func start(root, phase, actor, dir string, args []string, ready bool) (*process, error) {
	logDir := filepath.Join(root, "logs", phase)
	if err := os.MkdirAll(logDir, 0777); err != nil {
		return nil, err
	}
	os.Chmod(logDir, 0777)
	f, err := os.Create(filepath.Join(logDir, actor+".log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output io.Writer = f
	if ready {
		output = &readyWriter{writer: f, root: root, phase: phase}
	}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("could not start %s in %s", actor, phase)
	}
	p := &process{cmd: cmd, done: make(chan error, 1), log: f}
	go func() { err := cmd.Wait(); f.Close(); p.done <- err }()
	fmt.Printf("%s: %s started\n", actor, phase)
	return p, nil
}

func (p *process) stop() {
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
}

type readyWriter struct {
	mu                sync.Mutex
	writer            io.Writer
	root, phase, tail string
	ready             bool
}

func (w *readyWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.writer.Write(data)
	if err != nil {
		return n, err
	}
	if !w.ready {
		text := w.tail + string(data)
		if strings.Contains(text, "Start your agents") {
			if err := writeJSON(statePath(w.root, w.phase+".ready"), State{Success: true}); err != nil {
				return n, err
			}
			w.ready = true
		}
		if len(text) > 32 {
			text = text[len(text)-32:]
		}
		w.tail = text
	}
	return n, nil
}

func runServer(ctx context.Context, root string, c Config) error {
	for _, phase := range []string{"precompute", "simulation"} {
		mode := "precompute"
		if phase == "simulation" {
			mode = "comprun"
		}
		args := []string{"java", "-jar", "/opt/rcrs-server/jars/rcrs-server-launcher.jar", mode, "--nogui", "--map", filepath.Join(root, "scenario/map"), "--config", filepath.Join(root, "scenario/config"), "--log", filepath.Join(root, "logs", phase), "--team", c.Run.Configuration.Agent}
		p, err := start(root, phase, "server", "/opt/rcrs-server", args, true)
		if err != nil {
			return err
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		finished := false
		for !finished {
			select {
			case err := <-p.done:
				if phase == "precompute" {
					ticker.Stop()
					return errors.New("precompute server exited before agent completion")
				}
				if err != nil {
					ticker.Stop()
					return errors.New("simulation server failed; inspect server.log")
				}
				finished = true
			case <-ctx.Done():
				p.stop()
				ticker.Stop()
				return errors.New("server run deadline reached")
			case <-ticker.C:
				if state, _, _ := agentStates(root, ""); state.Error != "" {
					p.stop()
					ticker.Stop()
					return errors.New("agent failed; server stopped")
				}
				if state, _, ok := agentStates(root, phase); ok {
					if !state.Success {
						p.stop()
						ticker.Stop()
						return errors.New("agent phase failed")
					}
					if phase == "precompute" {
						p.stop()
						finished = true
					}
				}
			}
		}
		ticker.Stop()
		if err := writeJSON(statePath(root, phase+".server"), State{Success: true}); err != nil {
			return err
		}
		if err := waitAgents(ctx, root, phase); err != nil {
			return err
		}
		fmt.Printf("server: %s completed\n", phase)
	}
	return nil
}
