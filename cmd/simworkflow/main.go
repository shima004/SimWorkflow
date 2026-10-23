package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"simworkflow/internal/config"
)

func main() {
	if err := run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, lookup func(string) (string, bool), out, errOut io.Writer) error {
	flags := flag.NewFlagSet("simworkflow", flag.ContinueOnError)
	flags.SetOutput(errOut)
	envFile := flags.String("env-file", "", "optional local .env file (process environment takes precedence)")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: simworkflow [-env-file .env] config check\n       simworkflow experiment <validate|plan|modules> [options]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() >= 1 && flags.Arg(0) == "experiment" {
		return runExperiment(flags.Args()[1:], out, errOut)
	}
	if flags.NArg() != 2 || flags.Arg(0) != "config" || flags.Arg(1) != "check" {
		flags.Usage()
		return errors.New("expected config check or experiment command")
	}
	if *envFile != "" {
		values, err := config.ReadEnvFile(*envFile)
		if err != nil {
			return err
		}
		lookup = config.WithFallback(lookup, values)
	}
	c, err := config.Load(lookup)
	if err != nil {
		return err
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
