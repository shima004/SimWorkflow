package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

func runWorker(args []string, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("worker requires prepare, server, agent or collect")
	}
	flags := flag.NewFlagSet("worker "+args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	role := flags.String("role", "", "agent role: fireBrigade, policeForce or ambulanceTeam (required for agent)")
	root := flags.String("work-dir", "/work", "shared run directory")
	tools := flags.String("tools-dir", "/tools", "binary installation directory for prepare")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected worker arguments")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	if args[0] == "prepare" {
		var c worker.Config
		if json.Unmarshal([]byte(os.Getenv("SIMWORKFLOW_RUN")), &c) != nil {
			return errors.New("SIMWORKFLOW_RUN must contain run configuration JSON")
		}
		if err := os.MkdirAll(*tools, 0755); err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		source, err := os.Open(executable)
		if err != nil {
			return err
		}
		defer source.Close()
		target, err := os.OpenFile(filepath.Join(*tools, "simworkflow"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return err
		}
		_, err = io.Copy(target, source)
		closeErr := target.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return worker.Prepare(ctx, *root, c)
	}
	actor := args[0]
	if actor == "agent" {
		if !experiment.ValidRole(*role) {
			return errors.New("agent requires --role fireBrigade, policeForce or ambulanceTeam")
		}
		actor = worker.AgentActor(*role)
	} else if *role != "" {
		return errors.New("--role is only supported for agent")
	}
	return worker.Run(ctx, *root, actor)
}
