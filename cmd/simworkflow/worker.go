package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"simworkflow/internal/experiment"
	"simworkflow/internal/worker"
)

func runWorker(args []string, errOut io.Writer) error {
	if runtime.GOOS != "linux" {
		return errors.New("worker commands require a Linux container; client commands are available on this OS")
	}
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
		c, err := worker.ReadRunConfigSource(ctx, worker.RunConfigSource{Inline: os.Getenv("SIMWORKFLOW_RUN"), Remote: os.Getenv("SIMWORKFLOW_RUN_URL"), BundleURL: os.Getenv("SIMWORKFLOW_CONFIG_BUNDLE_URL"), BundleSHA256: os.Getenv("SIMWORKFLOW_CONFIG_BUNDLE_SHA256"), RunID: os.Getenv("SIMWORKFLOW_RUN_ID"), Stage: os.Getenv("SIMWORKFLOW_STAGE"), ExecutionID: os.Getenv("SIMWORKFLOW_EXECUTION_ID"), Sequence: os.Getenv("SIMWORKFLOW_SEQUENCE"), Index: os.Getenv("SIMWORKFLOW_RUN_INDEX")})
		if err != nil {
			return err
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
