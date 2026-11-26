//go:build unix && !linux && !darwin

package vm

import (
	"io/fs"
	"runtime"

	"golang.org/x/sys/unix"
)

// dup2Impl implements POSIX dup2 on BSD-family systems. unix.Dup2 performs the
// equal-descriptor validation internally.
func dup2Impl(oldfd, newfd int) error {
	return unix.Dup2(oldfd, newfd)
}

// sysStatInfo has no kernel-specific data off Linux; the portable fields
// filled by fileInfoToStat are used as is.
func sysStatInfo(info fs.FileInfo, si *statInfo) {}

// ttySize reports that no terminal size is available.
func ttySize() (cols, lines int, ok bool) { return 0, 0, false }

// getUnameInfo returns fallback unameInfo off Linux.
func getUnameInfo() unameInfo {
	return unameInfo{
		Sysname:  runtime.GOOS,
		Nodename: "localhost",
		Release:  "1.0.0",
		Version:  "#1",
		Machine:  runtime.GOARCH,
	}
}
