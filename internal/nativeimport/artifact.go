package nativeimport

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"cpa-session-archive/internal/archive"
)

type Artifact struct {
	File     *os.File
	Source   archive.ExternalSource
	Sessions []archive.ExternalSession
	Warnings []string
}

func (a *Artifact) Close() error {
	name := a.File.Name()
	err := a.File.Close()
	removeErr := os.Remove(name)
	if err != nil {
		return err
	}
	return removeErr
}

func Prepare(ctx context.Context, source archive.ExternalSource, rawOnly bool) (_ *Artifact, err error) {
	path, err := filepath.Abs(source.Path)
	if err != nil {
		return nil, err
	}
	source.Path = path
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source must be a regular file")
	}
	source.ModifiedAt = info.ModTime().UTC()
	file, err := os.CreateTemp("", "cpa-native-import-*")
	if err != nil {
		return nil, err
	}
	a := &Artifact{File: file, Source: source}
	defer func() {
		if err != nil {
			a.Close()
		}
	}()
	if source.Tool == "t3" && !rawOnly {
		a.Source.Format = "t3-history-export-v1"
		a.Sessions, a.Warnings, err = prepareT3(ctx, path, file)
	} else {
		input, openErr := os.Open(path)
		if openErr != nil {
			return nil, openErr
		}
		_, copyErr := io.Copy(file, input)
		closeErr := input.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		if rawOnly {
			a.Source.Format = "raw"
			a.Warnings = []string{"Raw source preserved; no searchable native-event projection requested."}
		} else {
			switch strings.ToLower(source.Tool) {
			case "codex", "claude":
				a.Source.Format = strings.ToLower(source.Tool) + "-jsonl"
				a.Sessions, err = parseJSONL(ctx, file, a.Source)
			default:
				return nil, fmt.Errorf("unsupported native tool %q; use --raw-only to preserve its source without a searchable projection", source.Tool)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if _, err = a.File.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return a, nil
}
