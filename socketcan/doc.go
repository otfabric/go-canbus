// SPDX-License-Identifier: MIT

// Package socketcan is the Linux SocketCAN transport: CAN_RAW sockets with
// CAN FD, kernel filters, error frames, loopback control, kernel
// timestamps and drop accounting, and kernel ISO-TP sockets (CAN_ISOTP).
//
// It uses only the syscall package (no cgo). The socket is non-blocking
// and registered with the Go runtime's network poller, so blocked Send
// and Receive calls are ended by their context or by Close without extra
// goroutines.
//
// Opening a socket never changes the interface: bit rates, CAN FD mode and
// the interface state are configured separately (package
// socketcan/link, or ip-link(8)).
//
// On systems other than Linux the package compiles, and [Open] and
// [DialISOTP] return [canbus.ErrUnsupported].
package socketcan
