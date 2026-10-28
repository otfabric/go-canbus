// SPDX-License-Identifier: MIT

package slcan

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// openPTY opens a pseudo-terminal and returns its master and the path of
// its slave. The ioctls go through SyscallConn: File.Fd would put the
// master in blocking mode, and Close would then not end a pending Read.
func openPTY() (*os.File, string, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, "", err
	}
	rc, err := m.SyscallConn()
	if err != nil {
		m.Close()
		return nil, "", err
	}
	var unlock, n uint32
	var e syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		if _, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e == 0 {
			_, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
		}
	}); err != nil || e != 0 {
		m.Close()
		return nil, "", fmt.Errorf("pty: %v %v", err, e)
	}
	return m, fmt.Sprintf("/dev/pts/%d", n), nil
}
