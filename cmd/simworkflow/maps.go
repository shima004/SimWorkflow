package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"simworkflow/internal/config"
	"simworkflow/internal/mapcatalog"
	"simworkflow/internal/storage"
)

func runMap(args []string, c config.Config, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected map register, list or inspect")
	}
	command := args[0]
	if command != "register" && command != "list" && command != "inspect" {
		return errors.New("expected map register, list or inspect")
	}
	flags := flag.NewFlagSet("map "+command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	name := flags.String("name", "", "registered map name")
	format := flags.String("format", "json", "output format: json or text (list only)")
	var version, path, description, origin string
	if command != "list" {
		flags.StringVar(&version, "version", "", "version (latest is accepted by inspect)")
	}
	if command == "register" {
		flags.StringVar(&path, "path", "", "map directory or ZIP (required)")
		flags.StringVar(&path, "f", "", "alias for --path")
		flags.StringVar(&description, "description", "", "description")
		flags.StringVar(&origin, "origin", "", "informational source URL or provenance")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("map commands accept options only")
	}
	if *format != "json" && (*format != "text" || command != "list") {
		return errors.New("format must be json, or text for map list")
	}
	catalog := &mapcatalog.Catalog{Store: storage.New(c)}
	ctx := context.Background()
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if command == "list" {
		entries, err := catalog.List(ctx, *name)
		if err != nil {
			return err
		}
		if *format == "json" {
			return encoder.Encode(entries)
		}
		writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "NAME\tVERSION\tSIZE (bytes)\tREGISTERED AT\tSHA256")
		for _, entry := range entries {
			fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\n", entry.Name, entry.Version, entry.SizeBytes, entry.RegisteredAt.Format("2006-01-02T15:04:05Z"), entry.SHA256)
		}
		return writer.Flush()
	}
	if *name == "" || version == "" {
		return errors.New("specify --name NAME and --version VERSION")
	}
	if command == "inspect" {
		entry, err := catalog.Inspect(ctx, *name, version)
		if err != nil {
			return err
		}
		return encoder.Encode(entry)
	}
	if path == "" {
		return errors.New("register requires --path DIRECTORY_OR_ZIP")
	}
	entry, err := catalog.Register(ctx, path, mapcatalog.RegisterOptions{Name: *name, Version: version, Description: description, Origin: origin})
	if err != nil {
		return err
	}
	return encoder.Encode(entry)
}
