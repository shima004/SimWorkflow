package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"simworkflow/internal/config"
	"simworkflow/internal/connections"
)

func main() {
	if err := run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, lookup func(string) (string, bool), out, errOut io.Writer) error {
	if len(args) > 0 && args[0] == "worker" {
		return runWorker(args[1:], errOut)
	}
	flags := flag.NewFlagSet("simworkflow", flag.ContinueOnError)
	flags.SetOutput(errOut)
	envFile := flags.String("env-file", ".env", "environment file (default: .env in current directory if present; empty disables loading)")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: simworkflow [-env-file .env] config check [--connect]\n       simworkflow [-env-file .env] experiment <validate|plan|modules|render|submit|status|results> [options]\n       simworkflow [-env-file .env] map <register|list|inspect> [options]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	explicitEnvFile := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "env-file" {
			explicitEnvFile = true
		}
	})
	loadConfig := func() (config.Config, error) {
		if *envFile != "" {
			values, err := config.ReadEnvFile(*envFile)
			if err != nil && !(errors.Is(err, os.ErrNotExist) && !explicitEnvFile) {
				return config.Config{}, err
			}
			lookup = config.WithFallback(lookup, values)
		}
		return config.Load(lookup)
	}
	if flags.NArg() >= 1 && flags.Arg(0) == "map" {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		return runMap(flags.Args()[1:], c, out, errOut)
	}
	if flags.NArg() >= 1 && flags.Arg(0) == "experiment" {
		if flags.NArg() > 1 && remoteCommand(flags.Arg(1)) {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			return runRemote(flags.Args()[1:], c, out, errOut)
		}
		return runExperiment(flags.Args()[1:], out, errOut)
	}
	if flags.NArg() < 2 || flags.Arg(0) != "config" || flags.Arg(1) != "check" {
		flags.Usage()
		return errors.New("expected config check or experiment command")
	}
	checkFlags := flag.NewFlagSet("config check", flag.ContinueOnError)
	checkFlags.SetOutput(errOut)
	connect := checkFlags.Bool("connect", false, "check read access to configured services")
	if err := checkFlags.Parse(flags.Args()[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if checkFlags.NArg() != 0 {
		return errors.New("config check does not accept positional arguments")
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if *connect {
		var failures []error
		for _, result := range connections.Check(context.Background(), c) {
			if result.Err != nil {
				if _, err := fmt.Fprintf(out, "%s: FAILED (%s)\n", result.Service, result.Err); err != nil {
					return err
				}
				failures = append(failures, fmt.Errorf("%s connection check failed", result.Service))
			} else if _, err := fmt.Fprintf(out, "%s: OK (%s)\n", result.Service, result.Detail); err != nil {
				return err
			}
		}
		return errors.Join(failures...)
	}
	// Do not print Config: URLs, credentials, and tokens may contain secrets.
	_, err = fmt.Fprintf(out, "Configuration valid (no network requests made).\nArgo authentication: %s\nS3 authentication: access key\nRegistry authentication: %s\nHTTP timeout: %s\n", argoAuth(c.Argo), registryAuth(c.Registry), c.HTTPTimeout)
	return err
}

func argoAuth(c config.Argo) string {
	if c.Token != "" {
		return "bearer token"
	}
	return "anonymous"
}

func registryAuth(c config.Registry) string {
	if c.Token != "" {
		return "bearer token"
	}
	if c.Username != "" {
		return "username/password"
	}
	return "anonymous"
}
