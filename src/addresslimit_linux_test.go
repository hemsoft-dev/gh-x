package main

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// limitTestAddressSpace caps this process's virtual memory at its current size
// plus headroom bytes; allocations past the cap make the Go runtime exit.
func limitTestAddressSpace(headroom uint64) error {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "VmSize:" {
			continue
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return err
		}
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_AS, &limit); err != nil {
			return err
		}
		limit.Cur = kilobytes*1024 + headroom
		return syscall.Setrlimit(syscall.RLIMIT_AS, &limit)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("VmSize missing from /proc/self/status")
}
