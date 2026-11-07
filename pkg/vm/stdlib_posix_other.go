//go:build !linux

package vm

import (
	"io/fs"
	"runtime"
)

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
