package main

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

type scanSummary struct {
	Scanner         string `json:"scanner"`
	Head            string `json:"head"`
	Scope           string `json:"scope"`
	Complete        bool   `json:"complete"`
	OriginRefs      int    `json:"origin_refs"`
	LocalRefs       int    `json:"local_refs"`
	HistoryCommits  int    `json:"history_commits"`
	SourceFiles     int    `json:"source_files"`
	SourceFindings  int    `json:"source_findings"`
	HistoryFindings int    `json:"history_findings"`
}

func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	data, err := cmd.Output()
	if err != nil {
		return nil, errors.New("git could not verify the credential scan scope")
	}
	return data, nil
}

func historyScope(ctx context.Context, root string, summary *scanSummary) error {
	shallow, err := gitOutput(ctx, root, "rev-parse", "--is-shallow-repository")
	if err != nil || strings.TrimSpace(string(shallow)) != "false" {
		return errors.New("credential history scope is shallow or unavailable; fetch complete history")
	}
	local, err := gitOutput(ctx, root, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return err
	}
	remote, err := gitOutput(ctx, root, "ls-remote", "--heads", "--tags", "origin")
	if err != nil {
		return errors.New("cannot verify origin heads/tags; history scope is incomplete")
	}
	summary.OriginRefs, summary.LocalRefs, err = verifyOriginRefs(string(local), string(remote))
	if err != nil {
		return err
	}
	count, err := gitOutput(ctx, root, "rev-list", "--all", "HEAD", "--count")
	if err != nil {
		return err
	}
	summary.HistoryCommits, err = strconv.Atoi(strings.TrimSpace(string(count)))
	if err != nil || summary.HistoryCommits < 1 {
		return errors.New("cannot count reachable history")
	}
	return nil
}

func verifyOriginRefs(local, remote string) (int, int, error) {
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(local), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return 0, 0, errors.New("invalid local ref inventory")
		}
		refs[parts[1]] = parts[0]
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(remote), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return 0, len(refs), errors.New("origin did not advertise a complete ref inventory")
		}
		if strings.HasSuffix(parts[1], "^{}") {
			continue
		}
		name := strings.Replace(parts[1], "refs/heads/", "refs/remotes/origin/", 1)
		if refs[name] != parts[0] {
			return count, len(refs), errors.New("origin heads/tags are missing or changed locally; fetch all origin heads and tags")
		}
		count++
	}
	return count, len(refs), nil
}
