//go:build linux

package vm

import (
	"io/fs"
	"syscall"
	"unsafe"
)

// sysStatInfo fills the fields only the kernel knows.
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
	si.AtimeNs = st.Atim.Sec*1e9 + st.Atim.Nsec
	si.MtimeNs = st.Mtim.Sec*1e9 + st.Mtim.Nsec
	si.CtimeNs = st.Ctim.Sec*1e9 + st.Ctim.Nsec
}

// ttySize reads the window size of the controlling terminal.
func ttySize() (cols, lines int, ok bool) {
	var ws struct{ Row, Col, X, Y uint16 }
	for _, fd := range []uintptr{1, 0, 2} {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
		if errno == 0 && ws.Col > 0 && ws.Row > 0 {
			return int(ws.Col), int(ws.Row), true
		}
	}
	return 0, 0, false
}

// getUnameInfo queries the kernel via syscall.Uname on Linux.
func getUnameInfo() unameInfo {
	var buf syscall.Utsname
	if err := syscall.Uname(&buf); err != nil {
		return unameInfo{
			Sysname:  "Linux",
			Nodename: "localhost",
			Release:  "6.0.0",
			Version:  "#1",
			Machine:  "x86_64",
		}
	}
	charsToString := func(p unsafe.Pointer, maxLen int) string {
		b := unsafe.Slice((*byte)(p), maxLen)
		for i, c := range b {
			if c == 0 {
				return string(b[:i])
			}
		}
		return string(b)
	}
	return unameInfo{
		Sysname:  charsToString(unsafe.Pointer(&buf.Sysname[0]), len(buf.Sysname)),
		Nodename: charsToString(unsafe.Pointer(&buf.Nodename[0]), len(buf.Nodename)),
		Release:  charsToString(unsafe.Pointer(&buf.Release[0]), len(buf.Release)),
		Version:  charsToString(unsafe.Pointer(&buf.Version[0]), len(buf.Version)),
		Machine:  charsToString(unsafe.Pointer(&buf.Machine[0]), len(buf.Machine)),
	}
}
