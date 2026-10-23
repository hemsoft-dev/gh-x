package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (transport fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestInstallerUsesCommittedChecksumAndExpectedBinary(t *testing.T) {
	asset, binary, err := scannerAsset(runtime.GOOS, runtime.GOARCH, "8.30.1")
	if err != nil {
		t.Skip("fixture platform has no upstream archive")
	}
	var archive bytes.Buffer
	if strings.HasSuffix(asset, ".zip") {
		writer := zip.NewWriter(&archive)
		file, err := writer.Create(binary)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("verified binary fixture")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&archive)
		writer := tar.NewWriter(gz)
		if err := writer.WriteHeader(&tar.Header{Name: binary, Size: int64(len("verified binary fixture")), Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte("verified binary fixture")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	http.DefaultClient = &http.Client{Transport: fixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/"+asset {
			t.Fatal("untrusted download route")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(archive.Bytes()))}, nil
	})}
	root := t.TempDir()
	fixturePolicy(t, root)
	writeFixture(t, filepath.Join(root, ".github", "gitleaks-checksums.txt"), fmt.Sprintf("%x  %s\n", sha256.Sum256(archive.Bytes()), asset))
	destination := filepath.Join(t.TempDir(), "tools")
	var output bytes.Buffer
	if err := command([]string{"-repo", root, "-install", destination}, &output); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(destination, binary))
	if err != nil || string(raw) != "verified binary fixture" || !strings.Contains(output.String(), "checksum-verified") {
		t.Fatal("verified installation missing")
	}
	writeFixture(t, filepath.Join(root, ".github", "gitleaks-checksums.txt"), "invalid")
	if err := command([]string{"-repo", root, "-install", destination}, &output); err == nil {
		t.Fatal("invalid checksum installation succeeded")
	}
}

func TestDownloadAndCleanupFailuresKeepSensitiveDiagnosticsPrivate(t *testing.T) {
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	http.DefaultClient = &http.Client{Transport: fixtureTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("sensitive diagnostic sentinel") })}
	if _, err := downloadArchive(context.Background(), "https://example.invalid"); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("unsafe download error")
	}
	http.DefaultClient = &http.Client{Transport: fixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("sensitive diagnostic sentinel"))}, nil
	})}
	if _, err := downloadArchive(context.Background(), "https://example.invalid"); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("unsafe server error")
	}
	if _, err := downloadArchive(context.Background(), ":invalid"); err == nil {
		t.Fatal("invalid URL accepted")
	}
	primary := errors.New("original scan failure")
	combined := primary
	closeResource(failingCloser{}, &combined)
	if !errors.Is(combined, primary) || strings.Contains(combined.Error(), "sentinel") {
		t.Fatal("cleanup replaced the primary failure or leaked diagnostics")
	}
}

type failingCloser struct{}

func (failingCloser) Close() error { return errors.New("sensitive diagnostic sentinel") }

func TestScannerPinsAndArchiveChecksums(t *testing.T) {
	for _, raw := range []string{"", "GITLEAKS_VERSION=latest", "GITLEAKS_VERSION=8.30.1\nGITLEAKS_VERSION=8.30.1"} {
		if _, err := parsePin(raw); err == nil {
			t.Fatal("invalid pin accepted")
		}
	}
	if pin, err := parsePin("OTHER=value\nGITLEAKS_VERSION=8.30.1\r\n"); err != nil || pin != "8.30.1" {
		t.Fatal("valid pin rejected")
	}
	raw := []byte("archive fixture")
	line := fmt.Sprintf("%x  asset.tar.gz", sha256.Sum256(raw))
	if err := verifyArchive(raw, line, "asset.tar.gz"); err != nil {
		t.Fatal(err)
	}
	for _, manifest := range []string{"", line + "\n" + line, strings.Replace(line, "asset.tar.gz", "other.tar.gz", 1)} {
		if verifyArchive(raw, manifest, "asset.tar.gz") == nil {
			t.Fatal("invalid checksum inventory accepted")
		}
	}
	if verifyArchive([]byte("changed"), line, "asset.tar.gz") == nil {
		t.Fatal("corrupt archive accepted")
	}
	if _, _, err := scannerAsset("linux", "amd64", "8.30.1"); err != nil {
		t.Fatal(err)
	}
	if _, binary, err := scannerAsset("windows", "arm64", "8.30.1"); err != nil || binary != "gitleaks.exe" {
		t.Fatal("Windows asset missing")
	}
	if _, _, err := scannerAsset("other", "amd64", "8.30.1"); err == nil {
		t.Fatal("unsupported OS accepted")
	}
	if _, _, err := scannerAsset("linux", "other", "8.30.1"); err == nil {
		t.Fatal("unsupported arch accepted")
	}
}

func TestScannerArchiveExtractsOnlyTheExpectedBinary(t *testing.T) {
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tarWriter := tar.NewWriter(gz)
	for _, name := range []string{"../escape", "gitleaks"} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: 7}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte("fixture")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if binary, err := scannerBinary(raw.Bytes(), false, "gitleaks"); err != nil || string(binary) != "fixture" {
		t.Fatal("tar binary unavailable")
	}
	if _, err := scannerBinary(raw.Bytes(), false, "missing"); err == nil {
		t.Fatal("missing tar binary accepted")
	}
	if _, err := scannerBinary([]byte("invalid"), false, "gitleaks"); err == nil {
		t.Fatal("invalid tar accepted")
	}
	var zipped bytes.Buffer
	zipWriter := zip.NewWriter(&zipped)
	file, err := zipWriter.Create("gitleaks.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if binary, err := scannerBinary(zipped.Bytes(), true, "gitleaks.exe"); err != nil || string(binary) != "fixture" {
		t.Fatal("zip binary unavailable")
	}
	if _, err := scannerBinary(zipped.Bytes(), true, "missing"); err == nil {
		t.Fatal("missing zip binary accepted")
	}
	if _, err := scannerBinary([]byte("invalid"), true, "gitleaks.exe"); err == nil {
		t.Fatal("invalid zip accepted")
	}
}
