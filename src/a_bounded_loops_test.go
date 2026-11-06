package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This file is named to sort first: go test runs files in name order, and
// under -failfast these controls stop a run before any later test can spin
// forever in the same loops. Each loop consumes a flag value by advancing its
// index inside the body; if that index ever moves backwards the loop appends
// without end, so each call runs in a child process whose address space is
// capped and fails quickly instead of exhausting the host's memory.

const boundedLoopChildEnv = "GH_X_BOUNDED_LOOP_CHILD"

var boundedLoopCases = map[string]struct {
	run  func() string
	want string
}{
	"repository access probe": {
		run: func() string {
			// Every repo flag form plus a trailing flag that has no value.
			return strings.Join(repositoryAccessProbeArgs([]string{"pr", "list", "--repo", "o/r", "-R", "x/y", "--repo=a/b", "-R=c/d", "--repo"}), " ")
		},
		want: "repo view o/r x/y a/b c/d --json nameWithOwner",
	},
	"terminal options": {
		run: func() string {
			cleaned, _, err := extractTerminalOptions([]string{"pr", "list", "--repo", "o/r", "--limit", "5", "--clear"})
			return fmt.Sprintf("%s err=%v", strings.Join(cleaned, " "), err)
		},
		want: "pr list --repo o/r --limit 5 err=<nil>",
	},
	"review flags": {
		run: func() string {
			flagArgs, target, err := splitReviewFlagArgs([]string{"12", "--model", "m", "--effort", "low", "--post"})
			return fmt.Sprintf("%s target=%s err=%v", strings.Join(flagArgs, " "), target, err)
		},
		want: "--model m --effort low --post target=12 err=<nil>",
	},
}

func TestFlagValueLoopsFinishWithinBoundedMemory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the address-space cap is Linux-only")
	}
	for name, tt := range boundedLoopCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFlagValueLoopChild$", "-test.count=1", "-test.v")
			cmd.Env = append(os.Environ(), boundedLoopChildEnv+"="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("did not finish within 20s:\n%s", out)
			}
			if err != nil {
				t.Fatalf("failed within its memory bound: %v\n%s", err, out)
			}
			if want := "result: " + tt.want + "\n"; !strings.Contains(string(out), want) {
				t.Fatalf("output lacks %q:\n%s", want, out)
			}
		})
	}
}

func TestFlagValueLoopChild(t *testing.T) {
	name := os.Getenv(boundedLoopChildEnv)
	if name == "" {
		t.Skip("helper process for TestFlagValueLoopsFinishWithinBoundedMemory")
	}
	tt, ok := boundedLoopCases[name]
	if !ok {
		t.Fatalf("unknown case %q", name)
	}
	if err := limitTestAddressSpace(512 << 20); err != nil {
		t.Fatalf("cap address space: %v", err)
	}
	fmt.Printf("result: %s\n", tt.run())
}
