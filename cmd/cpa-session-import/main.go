package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativeimport"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Import failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("cpa-session-import", flag.ContinueOnError)
	dbPath := flags.String("db", "", "Destination collector SQLite archive database (server-local)")
	bundle := flags.String("bundle", "", "Prepared private bundle to import on the collector host")
	bundleOut := flags.String("bundle-out", "", "Prepare a private transfer bundle without importing")
	source := archive.ExternalSource{}
	flags.StringVar(&source.Path, "source", "", "Original native history file (read only)")
	flags.StringVar(&source.Host, "host", "", "Original host")
	flags.StringVar(&source.Account, "account", "", "Original account")
	flags.StringVar(&source.Tool, "tool", "", "Original tool: codex, claude or t3")
	originalPath := flags.String("original-path", "", "Original absolute path when --source is a transferred copy")
	rawOnly := flags.Bool("raw-only", false, "Preserve bytes without a searchable event projection; do not use for credential-bearing databases")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *bundle != "" {
		if *dbPath == "" || *bundleOut != "" || source.Path != "" || source.Host != "" || source.Account != "" || source.Tool != "" || *originalPath != "" || *rawOnly {
			return fmt.Errorf("--bundle requires --db and cannot be combined with source options")
		}
		artifact, err := readBundle(ctx, *bundle)
		if err != nil {
			return err
		}
		defer artifact.Close()
		return importArtifact(ctx, *dbPath, artifact, output)
	}
	if (*dbPath == "") == (*bundleOut == "") || source.Path == "" || source.Host == "" || source.Account == "" || source.Tool == "" {
		return fmt.Errorf("--source, --host, --account and --tool plus either --db or --bundle-out are required")
	}
	source.Tool = strings.ToLower(source.Tool)
	if source.Tool == "t3" && *rawOnly {
		return fmt.Errorf("T3 databases contain authentication data; use the history-table export without --raw-only")
	}
	if *originalPath != "" && !filepath.IsAbs(*originalPath) {
		return fmt.Errorf("--original-path must be absolute")
	}
	if sourceInfo, err := os.Stat(source.Path); err == nil {
		if destinationInfo, err := os.Stat(*dbPath); err == nil && os.SameFile(sourceInfo, destinationInfo) {
			return fmt.Errorf("destination archive database must differ from the source")
		}
	}
	artifact, err := nativeimport.Prepare(ctx, source, *rawOnly)
	if err != nil {
		return err
	}
	defer artifact.Close()
	if *originalPath != "" {
		artifact.Source.Path = *originalPath
	}
	if *bundleOut != "" {
		return writeBundle(*bundleOut, artifact, output)
	}
	return importArtifact(ctx, *dbPath, artifact, output)
}

func importArtifact(ctx context.Context, dbPath string, artifact *nativeimport.Artifact, output io.Writer) error {
	store, err := archive.OpenStore(dbPath, false)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	receipt, err := store.ImportExternal(ctx, artifact.Source, artifact.File, artifact.Sessions)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Receipt  archive.ExternalImportReceipt `json:"receipt"`
		Warnings []string                      `json:"warnings,omitempty"`
	}{receipt, artifact.Warnings})
}
