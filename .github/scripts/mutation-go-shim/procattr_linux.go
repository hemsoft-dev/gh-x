package main

import "syscall"

// childProcAttr stops the real go command when Gremlins kills the shim at
// a mutant's deadline, so a timed-out build does not keep running.
func childProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
