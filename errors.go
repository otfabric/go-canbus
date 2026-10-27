// SPDX-License-Identifier: MIT

package canbus

import (
	"errors"
	"fmt"
)

// Sentinel errors. They are usually wrapped with detail; test with
// errors.Is.
var (
	// ErrClosed reports an operation on a bus, subscription or transport
	// that was closed.
	ErrClosed = errors.New("canbus: closed")

	// ErrTimeout reports an operation that did not complete in time. A
	// context deadline is reported as the context's error instead.
	ErrTimeout = errors.New("canbus: timeout")

	// ErrInvalidFrame reports a frame that cannot be represented or sent:
	// see [Frame.Validate]. A *[FrameError] carries the detail.
	ErrInvalidFrame = errors.New("canbus: invalid frame")

	// ErrUnsupported reports an operation or frame type the transport
	// does not support: SocketCAN on a system other than Linux, a CAN FD
	// frame on a socket not opened for CAN FD, a kernel filter on a
	// virtual port.
	ErrUnsupported = errors.New("canbus: unsupported")

	// ErrQueueFull reports a frame that could not be queued because a
	// bounded queue was full and its policy is to reject.
	ErrQueueFull = errors.New("canbus: queue full")

	// ErrInvalidOption reports an option value a bus, transport or
	// connection cannot be constructed with.
	ErrInvalidOption = errors.New("canbus: invalid option")

	// ErrInterfaceDown reports an interface that is down or does not
	// exist.
	ErrInterfaceDown = errors.New("canbus: interface down")

	// ErrBufferOverflow reports frames lost because a receive buffer
	// overflowed (the kernel's socket queue, a transmit queue that is
	// full: ENOBUFS).
	ErrBufferOverflow = errors.New("canbus: buffer overflow")
)

// FrameError reports a frame that failed validation. It wraps
// [ErrInvalidFrame].
type FrameError struct {
	Frame  Frame  // the offending frame
	Reason string // what is wrong
}

// Error describes the problem, for example
// "canbus: invalid frame 800#: standard identifier 0x800 exceeds 11 bits".
func (e *FrameError) Error() string {
	return fmt.Sprintf("canbus: invalid frame %s: %s", e.Frame.String(), e.Reason)
}

// Unwrap returns [ErrInvalidFrame].
func (e *FrameError) Unwrap() error { return ErrInvalidFrame }

// OpError reports a failed transport operation with the interface it was
// performed on.
type OpError struct {
	Op        string // "open", "send", "receive", "close", "filter", ...
	Interface string // interface name, or "" when unknown
	Err       error  // underlying error
}

// Error formats the operation, interface and cause.
func (e *OpError) Error() string {
	if e.Interface == "" {
		return "canbus: " + e.Op + ": " + e.Err.Error()
	}
	return "canbus: " + e.Op + " " + e.Interface + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *OpError) Unwrap() error { return e.Err }
