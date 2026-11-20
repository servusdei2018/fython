//go:build darwin

package vm

import "golang.org/x/sys/unix"

// termiosSetConstants registers the termios constants available on macOS.
// CBAUD, XCASE and baud rates above B230400 are Linux-only and are omitted.
func termiosSetConstants(set func(name string, v int64)) {
	set("B0", int64(unix.B0))
	set("B50", int64(unix.B50))
	set("B75", int64(unix.B75))
	set("B110", int64(unix.B110))
	set("B134", int64(unix.B134))
	set("B150", int64(unix.B150))
	set("B200", int64(unix.B200))
	set("B300", int64(unix.B300))
	set("B600", int64(unix.B600))
	set("B1200", int64(unix.B1200))
	set("B1800", int64(unix.B1800))
	set("B2400", int64(unix.B2400))
	set("B4800", int64(unix.B4800))
	set("B9600", int64(unix.B9600))
	set("B19200", int64(unix.B19200))
	set("B38400", int64(unix.B38400))
	set("B57600", int64(unix.B57600))
	set("B115200", int64(unix.B115200))
	set("B230400", int64(unix.B230400))
}

func termiosGet(fd int) (*termiosAttrs, error) {
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}
	a := &termiosAttrs{
		Iflag: int64(t.Iflag), Oflag: int64(t.Oflag), Cflag: int64(t.Cflag),
		Lflag: int64(t.Lflag), Ispeed: int64(t.Ispeed), Ospeed: int64(t.Ospeed),
		Cc: make([]byte, len(t.Cc)),
	}
	copy(a.Cc, t.Cc[:])
	return a, nil
}

func termiosSetAttr(fd int, when int, a *termiosAttrs) error {
	t := &unix.Termios{
		Iflag: uint64(a.Iflag), Oflag: uint64(a.Oflag), Cflag: uint64(a.Cflag),
		Lflag: uint64(a.Lflag), Ispeed: uint64(a.Ispeed), Ospeed: uint64(a.Ospeed),
	}
	for i := range t.Cc {
		if i < len(a.Cc) {
			t.Cc[i] = a.Cc[i]
		}
	}
	switch when {
	case 0:
		return unix.IoctlSetTermios(fd, unix.TIOCSETA, t)
	case 1:
		return unix.IoctlSetTermios(fd, unix.TIOCSETAW, t)
	case 2:
		return unix.IoctlSetTermios(fd, unix.TIOCSETAF, t)
	}
	return unix.EINVAL
}

func winsizeGet(fd int) (rows, cols int, err error) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Row), int(ws.Col), nil
}

func winsizeSet(fd int, rows, cols uint16) error {
	return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
}
