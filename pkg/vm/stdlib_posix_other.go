//go:build !linux

package vm

import "io/fs"

// sysStatInfo has no kernel-specific data off Linux; the portable fields
// filled by fileInfoToStat are used as is.
func sysStatInfo(info fs.FileInfo, si *statInfo) {}

// ttySize reports that no terminal size is available.
func ttySize() (cols, lines int, ok bool) { return 0, 0, false }
