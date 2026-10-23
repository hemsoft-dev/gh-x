package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := command(os.Args[1:], os.Stdout); err != nil {
		// Errors from subprocesses and reports never reach public output.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("credential-scan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("repo", ".", "repository to scan")
	report := flags.String("report", "", "counts and scope report, without finding contents")
	install := flags.String("install", "", "install pinned scanner into this directory")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("usage: credential-scan [-repo DIRECTORY] [-report FILE] [-install DIRECTORY]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	version, err := pinnedVersion(*root)
	if err != nil {
		return err
	}
	if *install != "" {
		return installScanner(ctx, *root, *install, version, output)
	}
	summary, scanErr := scanRepository(ctx, *root, version)
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return errors.Join(scanErr, errors.New("cannot encode credential scan scope"))
	}
	if *report != "" {
		if err := os.WriteFile(filepath.Clean(*report), append(encoded, '\n'), 0o600); err != nil {
			return errors.Join(scanErr, errors.New("cannot save credential scan scope report"))
		}
	}
	if _, err := fmt.Fprintln(output, string(encoded)); err != nil {
		return errors.Join(scanErr, errors.New("cannot write credential scan scope"))
	}
	return scanErr
}

func pinnedVersion(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, ".github", "quality-tools.env"))
	if err != nil {
		return "", errors.New("cannot read credential scanner pin")
	}
	return parsePin(string(raw))
}
