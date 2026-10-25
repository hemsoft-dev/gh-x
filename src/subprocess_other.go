//go:build !windows

package main

import (
	"context"
	"os/exec"
)

func newCapturedCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
