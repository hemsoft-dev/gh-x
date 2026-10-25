package main

import (
	"context"
	"os/exec"
	"syscall"
)

// newCapturedCommand keeps CLI metadata/API children console-free even when
// gh-x itself has no console. HideWindow alone only sets SW_HIDE; it does not
// prevent Windows from allocating a console for a console-subsystem child.
func newCapturedCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd
}
