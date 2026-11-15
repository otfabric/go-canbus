// SPDX-License-Identifier: MIT

package canbus

import (
	"log/slog"
	"runtime/debug"
)

// DropReason says why frames were lost.
type DropReason uint8

// Drop reasons.
const (
	DropQueueFull      DropReason = iota + 1 // a subscription's queue was full
	DropKernelOverflow                       // the kernel's socket receive queue overflowed
	DropFault                                // an injected fault (virtual bus)
	DropFiltered                             // not delivered because of the port's format (FD frame to a classical port)
)

// String names the reason.
func (r DropReason) String() string {
	switch r {
	case DropQueueFull:
		return "queue-full"
	case DropKernelOverflow:
		return "kernel-overflow"
	case DropFault:
		return "fault"
	case DropFiltered:
		return "filtered"
	}
	return "unknown"
}

// State is the state of a bus as far as the library can tell: whether it
// runs, and the controller's error state reported by error frames.
type State uint8

// Bus states.
const (
	StateUnknown State = iota
	StateActive        // running; controller error active (or no report)
	StateWarning       // error warning (an error counter reached 96)
	StatePassive       // error passive (an error counter reached 128)
	StateBusOff        // bus off
	StateDown          // the interface is down
	StateClosed        // closed by the application
)

// String names the state.
func (s State) String() string {
	names := [...]string{"unknown", "active", "warning", "passive", "bus-off", "down", "closed"}
	if int(s) < len(names) {
		return names[s]
	}
	return "unknown"
}

// Metrics is an optional callback interface for observing a [Bus]. A nil
// Metrics disables collection.
//
// The methods are called synchronously, possibly from several goroutines
// at once; implementations must be safe for concurrent use and must not
// block. Frames passed to them must not be retained or modified. Embed
// [NopMetrics] to implement only some callbacks and to stay compatible
// when methods are added.
//
// Where they are called: OnFrameReceived, OnXLFrameReceived, OnDrop,
// OnQueue, OnError for "receive" and the OnStateChange that follows a
// received frame or a receive error on the reader goroutine of the bus,
// in the order of the frames, so a callback that waits holds up every
// subscription; OnFrameSent, OnXLFrameSent and OnError for "send" on the
// goroutine that called Send; the OnStateChange of [NewBus] and of
// [Bus.Close] on the goroutine of that call.
//
// A callback may call the other methods of the bus and of its
// subscriptions (Stats, State, Subscribe, Send, a subscription's Close).
// It must not call [Bus.Close]: Close waits for the reader goroutine,
// which waits for the callback. A callback that closes the bus starts it
// with "go bus.Close()"; that Close completes once the callback has
// returned.
//
// A callback that does not return normally is stopped where the bus calls
// it and logged at error level, with the value it ended with and the
// stack, to the logger of the bus ([WithLogger]); the bus goes on as if
// the callback had returned.
type Metrics interface {
	// OnFrameSent is called after a frame was handed to the transport.
	OnFrameSent(iface string, f *Frame)
	// OnFrameReceived is called for every frame read from the transport,
	// before filtering by subscriptions.
	OnFrameReceived(iface string, f *Frame)
	// OnDrop is called when n frames were lost.
	OnDrop(iface string, reason DropReason, n uint64)
	// OnError is called when a send or receive failed.
	OnError(iface, op string, err error)
	// OnQueue is called after a frame was queued for a subscription with
	// the queue's depth and capacity.
	OnQueue(iface string, depth, capacity int)
	// OnStateChange is called when the bus state changes.
	OnStateChange(iface string, from, to State)
}

// guardMetrics returns m with every callback guarded: one that does not
// return normally is logged to log at error level with its stack, and the
// caller goes on as if it had returned. The bus holds no lock where it calls its metrics, so the
// entry is written where the callback ended. [NopMetrics] is returned as it
// is. The result is an [XLMetrics] when m is one.
func guardMetrics(m Metrics, log *slog.Logger) Metrics {
	if _, nop := m.(NopMetrics); nop {
		return m
	}
	g := guardedMetrics{m: m, log: log}
	if x, ok := m.(XLMetrics); ok {
		return guardedXLMetrics{guardedMetrics: g, x: x}
	}
	return g
}

type guardedMetrics struct {
	m   Metrics
	log *slog.Logger
}

// recovered is deferred around a callback: when the callback does not
// return normally it logs that and lets the caller go on.
func (g guardedMetrics) recovered(callback string) {
	if r := recover(); r != nil {
		g.log.Error("canbus: callback did not return normally", "callback", callback, "value", r, "stack", string(debug.Stack()))
	}
}

func (g guardedMetrics) OnFrameSent(iface string, f *Frame) {
	defer g.recovered("Metrics.OnFrameSent")
	g.m.OnFrameSent(iface, f)
}

func (g guardedMetrics) OnFrameReceived(iface string, f *Frame) {
	defer g.recovered("Metrics.OnFrameReceived")
	g.m.OnFrameReceived(iface, f)
}

func (g guardedMetrics) OnDrop(iface string, reason DropReason, n uint64) {
	defer g.recovered("Metrics.OnDrop")
	g.m.OnDrop(iface, reason, n)
}

func (g guardedMetrics) OnError(iface, op string, err error) {
	defer g.recovered("Metrics.OnError")
	g.m.OnError(iface, op, err)
}

func (g guardedMetrics) OnQueue(iface string, depth, capacity int) {
	defer g.recovered("Metrics.OnQueue")
	g.m.OnQueue(iface, depth, capacity)
}

func (g guardedMetrics) OnStateChange(iface string, from, to State) {
	defer g.recovered("Metrics.OnStateChange")
	g.m.OnStateChange(iface, from, to)
}

// guardedXLMetrics is guardedMetrics for a Metrics that is an XLMetrics.
type guardedXLMetrics struct {
	guardedMetrics
	x XLMetrics
}

func (g guardedXLMetrics) OnXLFrameSent(iface string, x *XLFrame) {
	defer g.recovered("XLMetrics.OnXLFrameSent")
	g.x.OnXLFrameSent(iface, x)
}

func (g guardedXLMetrics) OnXLFrameReceived(iface string, x *XLFrame) {
	defer g.recovered("XLMetrics.OnXLFrameReceived")
	g.x.OnXLFrameReceived(iface, x)
}

// NopMetrics is a [Metrics] that does nothing. Embed it in your own type.
type NopMetrics struct{}

// OnFrameSent does nothing.
func (NopMetrics) OnFrameSent(string, *Frame) {}

// OnFrameReceived does nothing.
func (NopMetrics) OnFrameReceived(string, *Frame) {}

// OnDrop does nothing.
func (NopMetrics) OnDrop(string, DropReason, uint64) {}

// OnError does nothing.
func (NopMetrics) OnError(string, string, error) {}

// OnQueue does nothing.
func (NopMetrics) OnQueue(string, int, int) {}

// OnStateChange does nothing.
func (NopMetrics) OnStateChange(string, State, State) {}

// XLMetrics is implemented by [Metrics] that also observe CAN XL frames.
// A Bus calls these instead of OnFrameSent and OnFrameReceived for CAN XL
// frames; Metrics without them see CAN XL frames only in the counters of
// [Bus.Stats]. [NopMetrics] implements it.
type XLMetrics interface {
	// OnXLFrameSent is called after a CAN XL frame was handed to the
	// transport.
	OnXLFrameSent(iface string, x *XLFrame)
	// OnXLFrameReceived is called for every CAN XL frame read from the
	// transport, before filtering by subscriptions.
	OnXLFrameReceived(iface string, x *XLFrame)
}

// OnXLFrameSent does nothing.
func (NopMetrics) OnXLFrameSent(string, *XLFrame) {}

// OnXLFrameReceived does nothing.
func (NopMetrics) OnXLFrameReceived(string, *XLFrame) {}
