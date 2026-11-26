//go:build darwin

package vm

import (
	"io/fs"
	"syscall"

	"golang.org/x/sys/unix"
)

// dup2Impl implements POSIX dup2 on Darwin. unix.Dup2 performs the
// equal-descriptor validation internally.
func dup2Impl(oldfd, newfd int) error {
	return unix.Dup2(oldfd, newfd)
}

// sysStatInfo fills the fields only the kernel knows on Darwin.
func sysStatInfo(info fs.FileInfo, si *statInfo) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	si.Mode = int64(st.Mode)
	si.Ino = int64(st.Ino)
	si.Dev = int64(st.Dev)
	si.Nlink = int64(st.Nlink)
	si.Uid = int64(st.Uid)
	si.Gid = int64(st.Gid)
	si.Size = st.Size
	si.AtimeNs = st.Atimespec.Sec*1e9 + st.Atimespec.Nsec
	si.MtimeNs = st.Mtimespec.Sec*1e9 + st.Mtimespec.Nsec
	si.CtimeNs = st.Ctimespec.Sec*1e9 + st.Ctimespec.Nsec
}

// ttySize reads the window size of the controlling terminal on Darwin.
func ttySize() (cols, lines int, ok bool) {
	for _, fd := range []int{1, 0, 2} {
		ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		if err == nil && ws.Col > 0 && ws.Row > 0 {
			return int(ws.Col), int(ws.Row), true
		}
	}
	return 0, 0, false
}

// getUnameInfo queries the kernel via unix.Uname on Darwin.
func getUnameInfo() unameInfo {
	var buf unix.Utsname
	if err := unix.Uname(&buf); err != nil {
		return unameInfo{
			Sysname:  "Darwin",
			Nodename: "localhost",
			Release:  "24.0.0",
			Version:  "#1",
			Machine:  "arm64",
		}
	}
	return unameInfo{
		Sysname:  unix.ByteSliceToString(buf.Sysname[:]),
		Nodename: unix.ByteSliceToString(buf.Nodename[:]),
		Release:  unix.ByteSliceToString(buf.Release[:]),
		Version:  unix.ByteSliceToString(buf.Version[:]),
		Machine:  unix.ByteSliceToString(buf.Machine[:]),
	}
}
