package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativeimport"
)

type bundleManifest struct {
	Version  int                       `json:"version"`
	Source   archive.ExternalSource    `json:"source"`
	Sessions []archive.ExternalSession `json:"sessions"`
	Warnings []string                  `json:"warnings,omitempty"`
	SHA256   string                    `json:"sha256"`
}

func writeBundle(path string, artifact *nativeimport.Artifact, output io.Writer) (err error) {
	hash := sha256.New()
	size, err := io.Copy(hash, artifact.File)
	if err != nil {
		return err
	}
	if _, err = artifact.File.Seek(0, io.SeekStart); err != nil {
		return err
	}
	manifest := bundleManifest{1, artifact.Source, artifact.Sessions, artifact.Warnings, hex.EncodeToString(hash.Sum(nil))}
	metadata, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(path)
		}
	}()
	tw := tar.NewWriter(file)
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(metadata))}); err != nil {
		return err
	}
	if _, err = tw.Write(metadata); err != nil {
		return err
	}
	if err = tw.WriteHeader(&tar.Header{Name: "source.raw", Mode: 0600, Size: size}); err != nil {
		return err
	}
	if _, err = io.Copy(tw, artifact.File); err != nil {
		return err
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{"bundle": path, "source": artifact.Source, "sha256": manifest.SHA256, "bytes": size, "sessions": len(artifact.Sessions), "warnings": artifact.Warnings})
}

func readBundle(ctx context.Context, path string) (_ *nativeimport.Artifact, err error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	tr := tar.NewReader(input)
	header, err := tr.Next()
	if err != nil || header.Name != "manifest.json" || header.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("bundle must start with manifest.json")
	}
	var manifest bundleManifest
	if err = json.NewDecoder(tr).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("invalid bundle manifest")
	}
	if manifest.Version != 1 || len(manifest.SHA256) != 64 {
		return nil, fmt.Errorf("unsupported or invalid bundle manifest")
	}
	header, err = tr.Next()
	if err != nil || header.Name != "source.raw" || header.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("bundle requires source.raw")
	}
	file, err := os.CreateTemp("", "cpa-native-bundle-*")
	if err != nil {
		return nil, err
	}
	a := &nativeimport.Artifact{File: file, Source: manifest.Source, Sessions: manifest.Sessions, Warnings: manifest.Warnings}
	defer func() {
		if err != nil {
			a.Close()
		}
	}()
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(file, hash), contextReader{ctx, tr}); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return nil, fmt.Errorf("bundle source checksum mismatch")
	}
	if _, err = tr.Next(); err != io.EOF {
		return nil, fmt.Errorf("unexpected bundle entry or invalid trailer")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return a, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}
