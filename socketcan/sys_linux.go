// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
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

// rtSigprocmask is rt_sigprocmask(2) with a 64-bit kernel sigset_t, for
// the calling (locked) OS thread; the syscall package has no wrapper.
func rtSigprocmask(how int, set, old *uint64) syscall.Errno {
	_, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, uintptr(how), uintptr(unsafe.Pointer(set)), uintptr(unsafe.Pointer(old)), 8, 0, 0)
	return e
}
