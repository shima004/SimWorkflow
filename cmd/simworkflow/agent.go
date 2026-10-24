package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"simworkflow/internal/agentbuild"
	"simworkflow/internal/config"
)

func runAgent(args []string, load func(bool) (config.Config, error), out, errOut io.Writer) error {
	return runAgentWithBuilder(args, load, agentbuild.New(), out, errOut)
}

func runAgentWithBuilder(args []string, load func(bool) (config.Config, error), builder *agentbuild.Builder, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] != "build" {
		return errors.New("expected agent build")
	}
	flags := flag.NewFlagSet("agent build", flag.ContinueOnError)
	flags.SetOutput(errOut)
	o := agentbuild.Options{}
	flags.StringVar(&o.Repository, "repository", "", "repository path under REGISTRY_URL (required)")
	flags.StringVar(&o.Tag, "tag", "latest", "image tag")
	flags.StringVar(&o.Dockerfile, "dockerfile", "", "Dockerfile path (default: CONTEXT/Dockerfile)")
	flags.StringVar(&o.Platform, "platform", "linux/amd64", "single target os/architecture[/variant]")
	flags.BoolVar(&o.Push, "push", false, "upload to REGISTRY_URL after a successful build")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("agent build accepts at most one local context directory after options")
	}
	o.Context = "."
	if flags.NArg() == 1 {
		o.Context = flags.Arg(0)
	}
	if err := o.Validate(); err != nil {
		return err
	}
	c, err := load(o.Push)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result, err := builder.Build(ctx, o, c, errOut)
	if err != nil {
		return err
	}
	if result.Published != "" {
		_, err = fmt.Fprintf(out, "Published %s\n\nimage: %s\n", result.Image, result.Published)
	} else {
		_, err = fmt.Fprintf(out, "Built %s locally (not uploaded).\n", result.Image)
	}
	return err
}
