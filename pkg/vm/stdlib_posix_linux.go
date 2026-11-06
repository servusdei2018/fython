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
	si.Size = int64(st.Size)
	si.AtimeNs = int64(st.Atim.Sec)*1e9 + int64(st.Atim.Nsec)
	si.MtimeNs = int64(st.Mtim.Sec)*1e9 + int64(st.Mtim.Nsec)
	si.CtimeNs = int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec)
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
