//go:build !linux

package main

import "syscall"

// childProcAttr has no parent-death signal outside Linux.
func childProcAttr() *syscall.SysProcAttr {
	return nil
}
