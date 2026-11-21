//go:build !opstats

package vm

import "io"

// recordRegReject is a no-op without the opstats tag.
func recordRegReject(co *CodeObject, reason string) {}

// DumpRegRejects is a no-op without the opstats tag.
func DumpRegRejects(w io.Writer) {}

// ResetRegRejects is a no-op without the opstats tag.
func ResetRegRejects() {}
