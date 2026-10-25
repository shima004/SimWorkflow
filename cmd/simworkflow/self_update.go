package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"simworkflow/internal/cliupdate"
)

func runSelfUpdate(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("self-update", flag.ContinueOnError)
	flags.SetOutput(errOut)
	o := cliupdate.Options{Current: version}
	flags.BoolVar(&o.Check, "check", false, "check for updates without modifying the executable")
	flags.BoolVar(&o.Prerelease, "prerelease", false, "include prereleases (required for current v0.x releases)")
	flags.StringVar(&o.Version, "version", "", "select a release tag, e.g. v0.1.2")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("self-update does not accept positional arguments")
	}
	if !o.Check {
		executable, err := os.Executable()
		if err != nil {
			return errors.New("could not locate running executable")
		}
		o.Target, err = filepath.EvalSymlinks(executable)
		if err != nil {
			return errors.New("could not resolve running executable")
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 5*time.Minute)
	defer timeout()
	result, err := cliupdate.New().Run(ctx, o, errOut)
	if err != nil {
		return err
	}
	if result.Updated {
		_, err = fmt.Fprintf(out, "Updated SimWorkflow %s -> %s.\n", result.Current, result.Available)
	} else if o.Check {
		_, err = fmt.Fprintf(out, "Current: %s\nAvailable: %s\n", result.Current, result.Available)
	} else {
		_, err = fmt.Fprintf(out, "SimWorkflow %s is up to date.\n", result.Current)
	}
	return err
}
