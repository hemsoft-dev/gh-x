package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func scanRepository(ctx context.Context, root, version string) (summary scanSummary, err error) {
	summary, err = prepareScan(ctx, root, version)
	if err != nil {
		return summary, err
	}
	workspace, err := os.MkdirTemp("", "gh-x-credentials-")
	if err != nil {
		return summary, errors.New("cannot prepare private scanner workspace")
	}
	defer removePrivateWorkspace(workspace, &err)
	snapshot := filepath.Join(workspace, "source")
	summary.SourceFiles, err = snapshotSource(ctx, root, snapshot)
	if err != nil {
		return summary, err
	}
	summary.SourceFindings, err = scannerFindings(ctx, root, workspace, "dir", snapshot)
	if err != nil {
		return summary, err
	}
	summary.HistoryFindings, err = scannerFindings(ctx, root, workspace, "git", root)
	if err != nil {
		return summary, err
	}
	summary.Complete = true
	if summary.SourceFindings+summary.HistoryFindings > 0 {
		return summary, errors.New("credentials detected; investigate privately and verify rotation before resolving genuine findings")
	}
	return summary, nil
}

func prepareScan(ctx context.Context, root, version string) (summary scanSummary, err error) {
	summary = scanSummary{Scanner: "gitleaks " + version, Scope: "nonignored working source; all local refs plus HEAD; advertised origin heads/tags verified"}
	if err := requireRepositoryRoot(ctx, root); err != nil {
		return summary, err
	}
	if err := rejectIgnoreFile(root); err != nil {
		return summary, err
	}
	if err := scannerVersion(ctx, version); err != nil {
		return summary, err
	}
	head, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return summary, err
	}
	summary.Head = strings.TrimSpace(string(head))
	if err := historyScope(ctx, root, &summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func closeResource(resource io.Closer, primary *error) {
	if resource.Close() != nil {
		*primary = errors.Join(*primary, errors.New("cannot close credential scan resource"))
	}
}

func removePrivateWorkspace(path string, primary *error) {
	if os.RemoveAll(path) != nil {
		*primary = errors.Join(*primary, errors.New("cannot remove private credential scan workspace"))
	}
}

func rejectIgnoreFile(root string) error {
	_, err := os.Lstat(filepath.Join(root, ".gitleaksignore"))
	if os.IsNotExist(err) {
		return nil
	}
	return errors.New("project .gitleaksignore files are unsupported; remove suppression and investigate privately")
}

func requireRepositoryRoot(ctx context.Context, root string) error {
	actual, err := gitOutput(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	expectedInfo, expectedErr := os.Stat(root)
	actualInfo, actualErr := os.Stat(strings.TrimSpace(string(actual)))
	if expectedErr != nil || actualErr != nil || !os.SameFile(expectedInfo, actualInfo) {
		return errors.New("credential scan must start at the repository root")
	}
	return nil
}

func scannerVersion(ctx context.Context, version string) error {
	actual, err := exec.CommandContext(ctx, "gitleaks", "version").Output()
	if err != nil || strings.TrimSpace(string(actual)) != version {
		return errors.New("pinned Gitleaks is missing or mismatched; run credential-scan -install DIRECTORY")
	}
	return nil
}

func scannerFindings(ctx context.Context, root, workspace, mode, source string) (int, error) {
	report := filepath.Join(workspace, mode+".json")
	ignore := filepath.Join(workspace, "empty-ignore")
	if err := os.WriteFile(ignore, nil, 0o600); err != nil {
		return 0, errors.New("cannot prepare scanner ignore policy")
	}
	config, err := filepath.Abs(filepath.Join(root, ".gitleaks.toml"))
	if err != nil {
		return 0, errors.New("cannot locate scanner configuration")
	}
	args := []string{mode, "--config=" + config, "--redact=100", "--exit-code=3", "--report-format=json", "--report-path=" + report,
		"--gitleaks-ignore-path=" + ignore, "--ignore-gitleaks-allow", "--no-banner", "--no-color", "--log-level=fatal", "--timeout=120"}
	if mode == "git" {
		args = append(args, "--log-opts=--all HEAD --full-history --root -m")
	}
	cmd := exec.CommandContext(ctx, "gitleaks", append(args, source)...)
	// Never forward scanner diagnostics or finding content, even on startup errors.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.Env = scannerEnvironment(os.Environ())
	runErr := cmd.Run()
	raw, err := os.ReadFile(report)
	if err != nil {
		return 0, errors.New("scanner did not produce a fresh private report")
	}
	return findingCount(raw, runErr)
}

func scannerEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(strings.ToUpper(entry), "GITLEAKS_") {
			clean = append(clean, entry)
		}
	}
	return clean
}

func findingCount(raw []byte, runErr error) (int, error) {
	var findings []struct{ Secret string }
	if json.Unmarshal(raw, &findings) != nil || findings == nil {
		return 0, errors.New("scanner report is missing or malformed")
	}
	for _, finding := range findings {
		if finding.Secret != "REDACTED" {
			return 0, errors.New("scanner report did not fully redact credential values")
		}
	}
	if runErr == nil && len(findings) == 0 {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) && exit.ExitCode() == 3 && len(findings) > 0 {
		return len(findings), nil
	}
	return 0, errors.New("credential scanner failed or returned inconsistent evidence")
}
