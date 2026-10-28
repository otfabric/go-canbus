// SPDX-License-Identifier: MIT

package canbus

import "context"

// Transport is one endpoint on a CAN bus: a SocketCAN socket, a port of a
// virtual bus, or a future hardware adapter.
//
// Send transmits a frame and Receive waits for the next one; neither
// retains the frame after it returns. Implementations allow Send from
// several goroutines at once and one Receive at a time. Both honour the
// context: a cancelled or expired context ends a blocked call with the
// context's error. Close ends blocked calls with [ErrClosed]; later calls
// return [ErrClosed] too.
type Transport interface {
	Send(ctx context.Context, f *Frame) error
	Receive(ctx context.Context, f *Frame) error
	Close() error
}

// Filterer is implemented by transports that filter in the kernel, in
// hardware or as frames arrive. Frames that do not pass are never
// delivered to Receive.
type Filterer interface {
	SetFilters(FilterSet) error
}

// Statser is implemented by transports that keep counters.
type Statser interface {
	Stats() Stats
}

// Namer is implemented by transports bound to a named interface.
type Namer interface {
	Name() string
}

// DropCounter is implemented by transports that lose frames before
// Receive returns them, for example to a kernel receive queue overflow.
// Drops returns the running total and the reason; a [Bus] reports the
// increments to its [Metrics].
type DropCounter interface {
	Drops() (total uint64, reason DropReason)
}

// Capabilitier is implemented by transports that describe themselves.
type Capabilitier interface {
	Capabilities() Capabilities
}

// Capabilities describes what a transport supports.
type Capabilities struct {
	FD            bool // sends and receives CAN FD frames
	ErrorFrames   bool // can deliver error frames
	KernelFilters bool // filters before delivery (Filterer)
	Timestamps    bool // frames carry receive timestamps
	RecvOwn       bool // can deliver its own transmissions
	Physical      bool // attached to a real or virtual network device, not simulated in process
}

// Stats are frame counters of a transport or bus.
type Stats struct {
	TxFrames  uint64 // frames sent
	TxBytes   uint64 // payload bytes sent
	RxFrames  uint64 // frames received
	RxBytes   uint64 // payload bytes received
	RxErrors  uint64 // error frames received
	Dropped   uint64 // frames lost: kernel receive queue overflow, full subscription queues, injected faults
	TxErrors  uint64 // failed send attempts
	RxFailure uint64 // failed receive attempts other than cancellation
}
