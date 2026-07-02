//go:build linux

package server

import (
	"os"
	"syscall"
	"unsafe"
)

// stdinIsTerminal reports whether stdin is a real TTY, via the terminal
// ioctl. A char-device check is NOT enough: /dev/null is a char device too.
func stdinIsTerminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
