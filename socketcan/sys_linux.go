// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"math"
	"syscall"
	"unsafe"
)

// The only uses of package unsafe in this module. Each passes the address
// of a Go value to a system call the syscall package does not wrap for
// AF_CAN; the conversion to uintptr happens in the call expression, which
// keeps the value alive and unmoved for the duration of the call, and the
// kernel reads or writes no more than the stated size.

// bindCAN binds fd to a struct sockaddr_can (syscall.Bind accepts only
// the syscall package's own Sockaddr types, none of which is AF_CAN).
func bindCAN(fd int, sa *sockaddrCAN) syscall.Errno {
	_, _, e := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(sa)), sockaddrCANLen)
	return e
}

// recvmsg is recvmsg(2) into a prepared header without allocating, with
// the sender's struct sockaddr_can kept (syscall.Recvmsg allocates and
// fails with EAFNOSUPPORT on an AF_CAN source address).
func recvmsg(fd uintptr, msg *syscall.Msghdr) (uintptr, syscall.Errno) {
	r, _, e := syscall.Syscall(syscall.SYS_RECVMSG, fd, uintptr(unsafe.Pointer(msg)), 0)
	return r, e
}

// pollOut reports whether fd polls writable now: ppoll(2) for POLLOUT
// without waiting (ppoll, unlike poll, exists on every Linux
// architecture). A kernel ISO-TP socket polls writable only while it has
// no transfer in progress.
func pollOut(fd uintptr) bool {
	const pollOutEvent = 0x4 // POLLOUT
	pfd := pollFd{fd: fd32(fd), events: pollOutEvent}
	var ts syscall.Timespec // zero: do not wait (and no EINTR)
	n, _, e := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
	return e == 0 && n == 1 && pfd.revents&pollOutEvent != 0
}

// pollFd is struct pollfd.
type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

// fd32 is a descriptor as a C int. Descriptors are C ints; anything
// larger names no descriptor, and ppoll ignores the negative one.
func fd32(fd uintptr) int32 {
	if fd > math.MaxInt32 {
		return -1
	}
	return int32(fd)
}
