//go:build !linux

package main

import "errors"

// limitTestAddressSpace is only implemented on Linux.
func limitTestAddressSpace(uint64) error {
	return errors.New("address-space limits are Linux-only")
}
