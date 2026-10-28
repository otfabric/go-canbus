// SPDX-License-Identifier: MIT

package slcan

import (
	"bytes"
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
	var name [128]byte
	var e syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		for _, req := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
			if _, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, req, 0); e != 0 {
				return
			}
		}
		_, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0])))
	}); err != nil || e != 0 {
		m.Close()
		return nil, "", fmt.Errorf("pty: %v %v", err, e)
	}
	return m, string(name[:bytes.IndexByte(name[:], 0)]), nil
}
