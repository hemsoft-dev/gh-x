package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var scannerPin = regexp.MustCompile(`(?m)^GITLEAKS_VERSION=([0-9]+\.[0-9]+\.[0-9]+)\r?$`)

func parsePin(contents string) (string, error) {
	matches := scannerPin.FindAllStringSubmatch(contents, -1)
	if len(matches) != 1 {
		return "", errors.New("credential scanner version pin is missing or ambiguous")
	}
	return matches[0][1], nil
}

func installScanner(ctx context.Context, root, destination, version string, output io.Writer) error {
	asset, binary, err := scannerAsset(runtime.GOOS, runtime.GOARCH, version)
	if err != nil {
		return err
	}
	checksums, err := os.ReadFile(filepath.Join(root, ".github", "gitleaks-checksums.txt"))
	if err != nil {
		return errors.New("cannot read pinned Gitleaks archive checksums")
	}
	url := "https://github.com/gitleaks/gitleaks/releases/download/v" + version + "/" + asset
	raw, err := downloadArchive(ctx, url)
	if err != nil {
		return err
	}
	if err := verifyArchive(raw, string(checksums), asset); err != nil {
		return err
	}
	contents, err := scannerBinary(raw, strings.HasSuffix(asset, ".zip"), binary)
	if err != nil {
		return err
	}
	if os.MkdirAll(destination, 0o755) != nil || os.WriteFile(filepath.Join(destination, binary), contents, 0o755) != nil {
		return errors.New("cannot install pinned Gitleaks binary")
	}
	_, err = fmt.Fprintln(output, "Installed checksum-verified Gitleaks "+version)
	if err != nil {
		return errors.New("cannot write credential scanner installation status")
	}
	return nil
}

func scannerAsset(system, arch, version string) (string, string, error) {
	if system != "linux" && system != "darwin" && system != "windows" {
		return "", "", errors.New("no pinned Gitleaks release archive for this operating system")
	}
	architectures := map[string]string{"amd64": "x64", "arm64": "arm64", "386": "x32"}
	name, ok := architectures[arch]
	if !ok {
		return "", "", errors.New("no pinned Gitleaks release archive for this architecture")
	}
	extension, binary := ".tar.gz", "gitleaks"
	if system == "windows" {
		extension, binary = ".zip", "gitleaks.exe"
	}
	return "gitleaks_" + version + "_" + system + "_" + name + extension, binary, nil
}

func downloadArchive(ctx context.Context, url string) (data []byte, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("cannot construct Gitleaks download")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, errors.New("cannot download pinned Gitleaks archive")
	}
	defer closeResource(response.Body, &err)
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("gitleaks download did not return success")
	}
	return boundedBytes(response.Body)
}

func boundedBytes(reader io.Reader) ([]byte, error) {
	const limit = 64 << 20
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("gitleaks archive or binary exceeds the supported size or is unreadable")
	}
	return raw, nil
}

func verifyArchive(raw []byte, manifest, asset string) error {
	expected := ""
	for _, line := range strings.Split(manifest, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == asset {
			if expected != "" {
				return errors.New("ambiguous Gitleaks archive checksum")
			}
			expected = parts[0]
		}
	}
	actual := sha256.Sum256(raw)
	if expected == "" || expected != hex.EncodeToString(actual[:]) {
		return errors.New("gitleaks archive does not match its pinned checksum")
	}
	return nil
}

func scannerBinary(raw []byte, zipped bool, binary string) (data []byte, err error) {
	if zipped {
		return zipBinary(raw, binary)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("invalid Gitleaks compressed archive")
	}
	defer closeResource(gzipReader, &err)
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err != nil {
			return nil, errors.New("gitleaks archive has no usable binary")
		}
		if header.Name == binary && header.Typeflag == tar.TypeReg {
			return boundedBytes(reader)
		}
	}
}

func zipBinary(raw []byte, binary string) (data []byte, err error) {
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, errors.New("invalid Gitleaks ZIP archive")
	}
	for _, file := range archive.File {
		if file.Name == binary && file.Mode().IsRegular() {
			reader, openErr := file.Open()
			if openErr != nil {
				return nil, errors.New("cannot open Gitleaks binary in archive")
			}
			defer closeResource(reader, &err)
			return boundedBytes(reader)
		}
	}
	return nil, errors.New("gitleaks archive has no usable binary")
}
