package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func snapshotSource(ctx context.Context, root, target string) (count int, err error) {
	raw, err := gitOutput(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return 0, err
	}
	files := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if err := os.MkdirAll(target, 0o700); err != nil {
		return 0, errors.New("cannot prepare source snapshot")
	}
	source, err := os.OpenRoot(root)
	if err != nil {
		return 0, errors.New("cannot open source root")
	}
	defer closeResource(source, &err)
	for _, name := range files {
		if name == "" {
			continue
		}
		copied, err := snapshotFile(source, target, name)
		if err != nil {
			return count, err
		}
		if copied {
			count++
		}
	}
	return count, nil
}

func snapshotFile(source *os.Root, target, name string) (bool, error) {
	if !filepath.IsLocal(name) {
		return false, errors.New("invalid source snapshot path")
	}
	info, err := source.Lstat(name)
	if os.IsNotExist(err) {
		return false, nil // A tracked deletion has no current source bytes.
	}
	if err != nil {
		return false, errors.New("cannot inspect source snapshot input")
	}
	destination := filepath.Join(target, name)
	if os.MkdirAll(filepath.Dir(destination), 0o700) != nil {
		return false, errors.New("cannot create source snapshot directory")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := source.Readlink(name)
		if err != nil || os.WriteFile(destination, []byte(link), 0o600) != nil {
			return false, errors.New("cannot snapshot symbolic link text")
		}
		return true, nil
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("source contains a submodule or unsupported file; snapshot scope is incomplete")
	}
	return true, copySnapshotFile(source, name, destination)
}

func copySnapshotFile(source *os.Root, name, destination string) (err error) {
	input, err := source.Open(name) // Root prevents concurrent links escaping the checkout.
	if err != nil {
		return errors.New("cannot open source snapshot input within checkout")
	}
	defer closeResource(input, &err)
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("cannot open source snapshot output")
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		return errors.New("cannot finish source snapshot")
	}
	return nil
}
