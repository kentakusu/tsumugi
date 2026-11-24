//go:build darwin

package main

import (
	"bytes"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS: /dev/ptmx を開き、grant・unlock して、相手の名前を TIOCPTYGNAME で引く。
// ★この機（Windows）では動かして確かめていない（Mac で初めて通すときに確かめる）。
func openPTY() (*os.File, *os.File, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, *os.File, error) { unix.Close(fd); return nil, nil, err }
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGRANT), 0); e != 0 {
		return fail(e)
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYUNLK), 0); e != 0 {
		return fail(e)
	}
	buf := make([]byte, 128)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return fail(e)
	}
	name := string(buf[:bytes.IndexByte(buf, 0)])
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), os.NewFile(uintptr(sfd), name), nil
}
