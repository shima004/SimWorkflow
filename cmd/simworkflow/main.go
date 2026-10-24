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

var (
	version = "dev"
	commit  = "unknown"
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
	configFile := flags.String("config", "", "config YAML file (default: user configuration directory/simworkflow/config.yaml; empty disables YAML loading)")
	showVersion := flags.Bool("version", false, "print version and source commit")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: simworkflow [--config FILE] [-env-file .env] config <init|path|check> [options]\n       simworkflow [--config FILE] [-env-file .env] experiment <validate|plan|modules|render|submit|status|results|download> [options]\n       simworkflow [--config FILE] [-env-file .env] map <register|list|inspect> [options]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintf(out, "simworkflow %s (commit %s)\n", version, commit)
		return err
	}
	explicitEnvFile := false
	explicitConfigFile := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "env-file" {
			explicitEnvFile = true
		}
		if f.Name == "config" {
			explicitConfigFile = true
		}
	})
	configPath := func() (string, error) {
		if explicitConfigFile {
			return *configFile, nil
		}
		return config.DefaultPath()
	}
	if flags.NArg() >= 2 && flags.Arg(0) == "config" && (flags.Arg(1) == "path" || flags.Arg(1) == "init") {
		command := flags.Arg(1)
		sub := flag.NewFlagSet("config "+command, flag.ContinueOnError)
		sub.SetOutput(errOut)
		if err := sub.Parse(flags.Args()[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if sub.NArg() != 0 {
			return errors.New("config " + command + " does not accept positional arguments")
		}
		path, err := configPath()
		if err != nil {
			return err
		}
		if path == "" {
			return errors.New("config YAML loading is disabled by --config=\"\"")
		}
		if command == "init" {
			if err := config.Init(path); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "Created %s; edit connection settings and credentials, then run config check.\n", path)
		} else {
			_, err = fmt.Fprintln(out, path)
		}
		return err
	}
	loadConfig := func() (config.Config, error) {
		get := lookup
		if *envFile != "" {
			values, err := config.ReadEnvFile(*envFile)
			if err != nil && !(errors.Is(err, os.ErrNotExist) && !explicitEnvFile) {
				return config.Config{}, err
			}
			get = config.WithFallback(get, values)
		}
		path, pathErr := configPath()
		// A missing user directory must not prevent environment-only operation.
		if pathErr == nil && path != "" {
			values, err := config.ReadYAML(path)
			if err != nil && !(errors.Is(err, os.ErrNotExist) && !explicitConfigFile) {
				return config.Config{}, err
			}
			get = config.WithFallback(get, values)
		}
		return config.Load(get)
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
		return errors.New("expected config init, config path, config check or experiment command")
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
