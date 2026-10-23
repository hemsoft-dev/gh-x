package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var realScanner = flag.Bool("gitleaks-integration", false, "require and exercise the pinned real Gitleaks binary")

func TestMain(m *testing.M) {
	if mode := os.Getenv("GH_X_SCANNER_FIXTURE"); mode != "" {
		if len(os.Args) > 1 && os.Args[1] == "version" {
			fmt.Fprintln(os.Stdout, "8.30.1")
			os.Exit(0)
		}
		fakeScannerProcess(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeScannerProcess(mode string) {
	path := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "--report-path=") {
			path = strings.TrimPrefix(arg, "--report-path=")
		}
	}
	// A broken tool may emit finding content on either stream. The wrapper must
	// discard both, including when report production or validation fails.
	fmt.Fprintln(os.Stdout, "scanner diagnostic sentinel")
	fmt.Fprintln(os.Stderr, "scanner diagnostic sentinel")
	if mode == "no-report" {
		os.Exit(2)
	}
	body, code := "[]", 0
	switch mode {
	case "leak":
		body, code = `[{"Secret":"REDACTED"}]`, 3
	case "unredacted":
		body, code = `[{"Secret":"unsafe diagnostic sentinel"}]`, 1
	case "malformed":
		body, code = `null`, 1
	case "startup":
		code = 2
	}
	if os.WriteFile(path, []byte(body), 0o600) != nil {
		os.Exit(3)
	}
	os.Exit(code)
}

func useFakeScanner(t *testing.T, mode string) {
	t.Helper()
	root := t.TempDir()
	name := "gitleaks"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_X_SCANNER_FIXTURE", mode)
}

func fixturePolicy(t *testing.T, root string) {
	t.Helper()
	writeFixture(t, filepath.Join(root, ".gitleaks.toml"), "[extend]\nuseDefault = true\n")
	writeFixture(t, filepath.Join(root, ".github", "quality-tools.env"), "GITLEAKS_VERSION=8.30.1\n")
}

func TestScanRepositoryProducesOnlyCountsAndScope(t *testing.T) {
	useFakeScanner(t, "clean")
	root := fixtureRepo(t)
	fixturePolicy(t, root)
	var output bytes.Buffer
	report := filepath.Join(t.TempDir(), "summary.json")
	if err := command([]string{"-repo", root, "-report", report}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"complete": true`) || strings.Contains(output.String(), "sentinel") {
		t.Fatal("unsafe or incomplete output")
	}
	stored, err := os.ReadFile(report)
	if err != nil || !bytes.Equal(stored, output.Bytes()) {
		t.Fatal("public summary differs")
	}
}

func TestScannerFailuresNeverPublishDiagnosticValues(t *testing.T) {
	for _, mode := range []string{"leak", "unredacted", "malformed", "startup", "no-report"} {
		t.Run(mode, func(t *testing.T) {
			useFakeScanner(t, mode)
			root := fixtureRepo(t)
			fixturePolicy(t, root)
			var output bytes.Buffer
			err := command([]string{"-repo", root}, &output)
			if err == nil || strings.Contains(output.String()+err.Error(), "sentinel") {
				t.Fatalf("unsafe failure: %s %v", output.String(), err)
			}
			if mode == "leak" && (!strings.Contains(output.String(), `"complete": true`) || !strings.Contains(output.String(), `"history_findings": 1`)) {
				t.Fatal("both source and history findings must fail")
			}
		})
	}
}

func TestScannerRejectsMissingPinsAndEnvironmentOverrides(t *testing.T) {
	if _, err := pinnedVersion(t.TempDir()); err == nil {
		t.Fatal("missing pin accepted")
	}
	if err := command([]string{"-unknown"}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid arguments accepted")
	}
	if _, err := scanRepository(context.Background(), t.TempDir(), "0.0.0"); err == nil {
		t.Fatal("unavailable scanner accepted")
	}
	clean := scannerEnvironment([]string{"PATH=fixture", "GITLEAKS_CONFIG=unsafe", "gitleaks_config_toml=unsafe", "OTHER=value"})
	if strings.Join(clean, "|") != "PATH=fixture|OTHER=value" {
		t.Fatal("scanner environment can override policy")
	}
	for _, raw := range []string{"invalid", "null", `[{"Secret":"unredacted"}]`, `[{"Secret":"REDACTED"}]`} {
		if _, err := findingCount([]byte(raw), nil); err == nil {
			t.Fatal("inconsistent/unredacted report accepted")
		}
	}
}

func TestPinnedProducerCaptureIsFullyRedacted(t *testing.T) {
	raw, err := os.ReadFile("testdata/gitleaks-8.30.1-redacted.json")
	if err != nil {
		t.Fatal(err)
	}
	var findings []struct{ RuleID, Secret, Match string }
	if json.Unmarshal(raw, &findings) != nil || len(findings) != 1 || findings[0].RuleID != "github-pat" || findings[0].Secret != "REDACTED" || findings[0].Match != "REDACTED" {
		t.Fatal("pinned producer capture is incomplete or unredacted")
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) {
	return 0, errors.New("sensitive output diagnostic sentinel")
}

func TestDiagnosticWriteFailuresPreserveCredentialFinding(t *testing.T) {
	useFakeScanner(t, "leak")
	root := fixtureRepo(t)
	fixturePolicy(t, root)
	for _, args := range [][]string{{"-repo", root}, {"-repo", root, "-report", t.TempDir()}} {
		err := command(args, failedOutput{})
		if err == nil || !strings.Contains(err.Error(), "credentials detected") || strings.Contains(err.Error(), "sentinel") {
			t.Fatal("diagnostic failure replaced the finding or leaked details")
		}
	}
}

func TestGitleaksDetectsRemovedCredentialsAndIgnoresNoSuppressions(t *testing.T) {
	if !*realScanner {
		t.Skip("real scanner fixtures run through the credential fixture gate")
	}
	if err := scannerVersion(context.Background(), "8.30.1"); err != nil {
		t.Fatal(err)
	}
	root := fixtureRepo(t)
	fixturePolicy(t, root)
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", ".gitleaks.toml"))
	if err != nil {
		t.Fatal("cannot read the actual repository scanner policy")
	}
	writeFixture(t, filepath.Join(root, ".gitleaks.toml"), string(policy))
	writeFixture(t, filepath.Join(root, "fixture.txt"), "token=example\n")
	baseline, err := scanRepository(context.Background(), root, "8.30.1")
	if err != nil || !baseline.Complete || baseline.SourceFindings != 0 {
		t.Fatal("short placeholder required an exception")
	}
	// Construct an inert synthetic token at runtime so repository scans never
	// need an exception for the scanner's own regression source.
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("inert scanner regression fixture")))
	token := "ghp_" + digest[:36]
	writeFixture(t, filepath.Join(root, "fixture.txt"), "token="+token+" # gitleaks:allow\n")
	summary, err := scanRepository(context.Background(), root, "8.30.1")
	if err == nil || !summary.Complete || summary.SourceFindings < 1 {
		t.Fatalf("source fixture was not rejected: %+v %v", summary, err)
	}
	fixtureGit(t, root, "add", ".")
	fixtureGit(t, root, "commit", "-m", "synthetic credential fixture")
	fixtureGit(t, root, "push", "origin", "main")
	// Capture the pinned producer's fully redacted report for parser provenance.
	workspace := t.TempDir()
	if _, err := scannerFindings(context.Background(), root, workspace, "git", root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "git.json"))
	if err != nil || bytes.Contains(raw, []byte(token)) {
		t.Fatal("pinned producer report leaked synthetic credential")
	}
	if capture := os.Getenv("GH_X_SCANNER_CAPTURE"); capture != "" {
		if err := os.WriteFile(capture, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var findings []struct{ Fingerprint string }
	if err := json.Unmarshal(raw, &findings); err != nil || len(findings) == 0 {
		t.Fatal("real scanner omitted finding fingerprints")
	}
	fixtureGit(t, root, "rm", "fixture.txt")
	fixtureGit(t, root, "commit", "-m", "remove fixture from tip")
	fixtureGit(t, root, "push", "origin", "main")
	summary, err = scanRepository(context.Background(), root, "8.30.1")
	if err == nil || !summary.Complete || summary.SourceFindings != 0 || summary.HistoryFindings < 1 {
		t.Fatalf("removed history fixture escaped: %+v %v", summary, err)
	}
	writeFixture(t, filepath.Join(root, ".gitleaksignore"), findings[0].Fingerprint+"\n")
	summary, err = scanRepository(context.Background(), root, "8.30.1")
	if err == nil || !strings.Contains(err.Error(), "suppression") || summary.Complete {
		t.Fatal("ignore-file suppression was accepted")
	}
}
