package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"simworkflow/internal/argo"
	"simworkflow/internal/config"
)

func runLifecycle(ctx context.Context, args []string, c config.Config, out, errOut io.Writer, loadWatch ...func() (config.Config, error)) error {
	flags := flag.NewFlagSet("experiment "+args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	if args[0] == "stop" {
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("specify exactly one workflow name: experiment stop WORKFLOW_NAME")
		}
		result, requested, err := argo.New(c).Stop(ctx, flags.Arg(0))
		if err != nil {
			return err
		}
		if result.Completed() {
			_, err = fmt.Fprintf(out, "%s is already %s; no stop request sent.\n", screenText(result.Name), screenText(result.Phase))
			return err
		}
		if !requested {
			_, err = fmt.Fprintf(out, "%s already has a shutdown request (%s).\n", screenText(result.Name), screenText(result.Shutdown))
			return err
		}
		_, err = fmt.Fprintf(out, "Stop requested for %s. Check completion with experiment status %s.\n", screenText(result.Name), screenText(result.Name))
		return err
	}
	phase := flags.String("phase", "", "filter by phase: Pending, Running, Succeeded, Failed or Error")
	limit := flags.Int("limit", 0, "maximum displayed experiments (default: all)")
	format := flags.String("format", "auto", "output format: auto (terminal pager), text or json")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("experiment list accepts options only")
	}
	if *limit < 0 {
		return errors.New("limit must be zero or positive")
	}
	if *format != "auto" && *format != "text" && *format != "json" {
		return errors.New("format must be auto, text or json")
	}
	if *phase != "" && *phase != "Pending" && *phase != "Running" && *phase != "Succeeded" && *phase != "Failed" && *phase != "Error" {
		return errors.New("phase must be Pending, Running, Succeeded, Failed or Error")
	}
	items, err := argo.New(c).List(ctx)
	if err != nil {
		return err
	}
	selected := make([]argo.Overview, 0)
	for _, item := range items {
		if *phase == "" || item.Phase == *phase {
			selected = append(selected, item)
		}
		if *limit > 0 && len(selected) >= *limit {
			break
		}
	}
	if *format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(selected)
	}
	if len(selected) == 0 {
		_, err = fmt.Fprintln(out, "No SimWorkflow experiments found in the configured namespace.")
		return err
	}
	if *format == "auto" && interactiveOutput(os.Stdin, out) {
		return browseExperiments(ctx, selected, os.Stdin, out, func(name string) error {
			watchConfig := c
			if len(loadWatch) > 0 {
				var err error
				watchConfig, err = loadWatch[0]()
				if err != nil {
					return err
				}
			}
			return watchProgress(ctx, watchConfig, name, os.Stdin, out)
		})
	}
	return writeExperimentList(out, selected)
}

func experimentListCell(value string) string {
	return strings.ReplaceAll(screenText(value), "\n", " ")
}

func writeExperimentList(out io.Writer, items []argo.Overview) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "WORKFLOW\tPHASE\tSHUTDOWN\tCREATED (UTC)\tFINISHED (UTC)")
	for _, item := range items {
		shutdown := item.Shutdown
		if shutdown == "" {
			shutdown = "-"
		}
		created, finished := "-", "-"
		if !item.CreatedAt.IsZero() {
			created = item.CreatedAt.UTC().Format(time.RFC3339)
		}
		if item.FinishedAt != nil && !item.FinishedAt.IsZero() {
			finished = item.FinishedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", experimentListCell(item.Name), experimentListCell(item.Phase), experimentListCell(shutdown), created, finished)
	}
	return w.Flush()
}
