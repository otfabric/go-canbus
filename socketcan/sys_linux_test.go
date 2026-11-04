// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"syscall"
	"testing"
	"unsafe"
)

// TestLayouts checks the sizes the unsafe system calls and the portable
// control message decoder rely on against the Go definitions.
func TestLayouts(t *testing.T) {
	if s := unsafe.Sizeof(sockaddrCAN{}); s != sockaddrCANLen {
		t.Fatalf("sockaddrCAN is %d bytes, want %d", s, sockaddrCANLen)
	}
	if s := unsafe.Sizeof(syscall.Cmsghdr{}); s != cmsgHdrLen {
		t.Fatalf("Cmsghdr is %d bytes, cmsgHdrLen %d", s, cmsgHdrLen)
	}
	if s := unsafe.Sizeof(syscall.Timespec{}); s != 2*wordSize {
		t.Fatalf("Timespec is %d bytes, want %d", s, 2*wordSize)
	}
	var c Conn
	if len(c.rsa) != sockaddrCANLen || len(c.oob) != oobSize {
		t.Fatal("receive buffers")
	}
}
