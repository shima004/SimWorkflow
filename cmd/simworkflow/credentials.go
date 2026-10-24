package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/x/term"

	"simworkflow/internal/credentials"
)

func readCredential(label string, out io.Writer) (string, error) {
	if !term.IsTerminal(os.Stdin.Fd()) {
		return "", errors.New("credentials set requires an interactive terminal; supply credentials through environment variables in CI")
	}
	state, err := term.GetState(os.Stdin.Fd())
	if err != nil {
		return "", errors.New("could not inspect credential input terminal")
	}
	// ReadPassword restores echo on a normal return, but the process's default
	// signal handler would terminate before its defer runs. Restore it explicitly.
	interrupted := make(chan os.Signal, 1)
	finished := make(chan struct{})
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupted)
	defer close(finished)
	go func() {
		select {
		case <-interrupted:
			term.Restore(os.Stdin.Fd(), state)
			os.Exit(130)
		case <-finished:
		}
	}()
	if _, err := fmt.Fprint(out, label+": "); err != nil {
		return "", err
	}
	value, err := term.ReadPassword(os.Stdin.Fd())
	fmt.Fprintln(out)
	if err != nil {
		return "", errors.New("could not read credential from terminal")
	}
	defer clear(value)
	return string(value), nil
}

func runCredentials(args []string, loadSettings func() (func(string) (string, bool), error), store *credentials.Store, prompt func(string) (string, error), out, errOut io.Writer) error {
	help := func(value string) bool { return value == "-h" || value == "--help" }
	if (len(args) == 1 && help(args[0])) || (len(args) == 2 && help(args[1])) {
		_, err := fmt.Fprintln(errOut, "Usage: simworkflow credentials set <argo|s3|registry> [--auth password|token]\n       simworkflow credentials status [argo|s3|registry]\n       simworkflow credentials delete <argo|s3|registry>\nCredential values are entered at hidden prompts, never as command arguments.")
		return err
	}
	if len(args) == 0 {
		return errors.New("expected credentials set, status or delete")
	}
	command := args[0]
	if command != "set" && command != "status" && command != "delete" {
		return errors.New("expected credentials set, status or delete")
	}
	targets := credentials.Targets()
	if len(args) > 1 {
		if len(credentials.Fields(args[1])) == 0 {
			return errors.New("credential target must be argo, s3 or registry")
		}
		targets = []string{args[1]}
	} else if command != "status" {
		return errors.New("specify credential target: argo, s3 or registry")
	}
	flags := flag.NewFlagSet("credentials "+command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	auth := flags.String("auth", "", "registry authentication for set: password (default) or token")
	if len(args) > 2 {
		if err := flags.Parse(args[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected credential arguments; enter credential values only at the hidden prompts")
	}
	if *auth != "" && (command != "set" || targets[0] != "registry" || (*auth != "password" && *auth != "token")) {
		return errors.New("--auth is only supported for credentials set registry: password or token")
	}
	settings, err := loadSettings()
	if err != nil {
		return err
	}
	var failures []error
	for _, target := range targets {
		account, err := credentials.Account(target, settings)
		if err != nil {
			if command != "status" {
				return err
			}
			fmt.Fprintf(out, "%s: endpoint not configured\n", target)
			failures = append(failures, err)
			continue
		}
		switch command {
		case "set":
			labels := map[string]string{"ARGO_WORKFLOW_TOKEN": "Argo token", "S3_ACCESS_KEY_ID": "S3 access key ID", "S3_SECRET_ACCESS_KEY": "S3 secret access key", "S3_SESSION_TOKEN": "S3 session token (optional; Enter for none)", "REGISTRY_USERNAME": "Registry username", "REGISTRY_PASSWORD": "Registry password", "REGISTRY_TOKEN": "Registry token"}
			fields := credentials.Fields(target)
			if target == "registry" {
				fields = []string{"REGISTRY_USERNAME", "REGISTRY_PASSWORD"}
				if *auth == "token" {
					fields = []string{"REGISTRY_TOKEN"}
				}
			}
			values := map[string]string{}
			for _, field := range fields {
				value, err := prompt(labels[field])
				if err != nil {
					return errors.New("credential input cancelled or unavailable; nothing was saved; use an interactive terminal, or supply environment variables in CI")
				}
				values[field] = value
			}
			if err := store.Set(target, account, values); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s credentials saved to OS store; configuration files were not modified.\n", target)
			if mode, _ := settings("CREDENTIALS_STORE"); mode != "keyring" {
				fmt.Fprintln(out, "To use stored credentials, set credentials.store: keyring in config.yaml or CREDENTIALS_STORE=keyring.")
			}
		case "delete":
			if err := store.Delete(account); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s credentials removed from OS store for the configured endpoint.\n", target)
		case "status":
			_, found, err := store.Get(target, account)
			if err != nil {
				fmt.Fprintf(out, "%s: OS store unavailable or invalid\n", target)
				failures = append(failures, err)
				continue
			}
			state := "not registered"
			if found {
				state = "registered"
			}
			fmt.Fprintf(out, "%s: %s (OS store)\n", target, state)
		}
	}
	return errors.Join(failures...)
}
