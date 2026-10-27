// SPDX-License-Identifier: MIT

// Package isotp implements ISO 15765-2 (ISO-TP), the transport protocol
// that carries messages of up to 4095 bytes (or 4 GiB with the escape
// sequence) over CAN frames, in userspace on top of a canbus.Bus.
//
// It supports normal addressing (also with 29-bit identifiers, as normal
// fixed addressing uses them), extended addressing (target address in the
// first payload byte) and mixed addressing (address extension in the first
// byte); single, first, consecutive and flow control frames; block size
// and STmin, including the 100–900 µs values; flow control WAIT with a
// limit and OVERFLOW; sequence number wrap-around; the N_Bs and N_Cr
// timeouts; padding; classical CAN and CAN FD frames (TX_DL 8 to 64,
// single frame escape) and the first frame escape for messages larger
// than 4095 bytes. Message sizes are bounded by Config.MaxMessageSize.
//
// The Linux kernel's ISO-TP sockets offer the same with the same Config:
// see socketcan.DialISOTP. Both implement [Endpoint].
//
// ISO 15765-2 is not publicly available; this implementation follows
// public descriptions and is tested against independent implementations
// (the Linux kernel and python-can-isotp). It makes no conformance claim.
package isotp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/otfabric/go-canbus"
)

// Endpoint is an ISO-TP connection: the userspace [Conn] or a kernel
// socket.
type Endpoint interface {
	Send(ctx context.Context, payload []byte) error
	Receive(ctx context.Context) ([]byte, error)
	Close() error
}

// Addressing selects how N_AI is carried.
type Addressing uint8

// Addressing modes.
const (
	// Normal: the CAN identifiers alone address the peers. Normal fixed
	// addressing is normal addressing with 29-bit identifiers that
	// encode the addresses (0x18DA<TA><SA>): give those identifiers.
	Normal Addressing = iota
	// Extended: the first payload byte is the target address.
	Extended
	// Mixed: the first payload byte is the address extension.
	Mixed
)

// String names the mode.
func (a Addressing) String() string {
	return [...]string{"normal", "extended", "mixed"}[min(int(a), 2)]
}

// Errors. They are wrapped with detail; test with errors.Is.
var (
	// ErrTimeout reports a peer that did not send a flow control frame in
	// time (N_Bs) or a consecutive frame in time (N_Cr).
	ErrTimeout = errors.New("isotp: timeout")
	// ErrOverflow reports a receiver that answered OVERFLOW: the message
	// is larger than it accepts.
	ErrOverflow = errors.New("isotp: receiver overflow")
	// ErrWaitLimit reports more consecutive WAIT flow controls than
	// Config.MaxWait.
	ErrWaitLimit = errors.New("isotp: too many flow control WAIT frames")
	// ErrSequence reports a consecutive frame with a wrong sequence
	// number; the message being received is dropped.
	ErrSequence = errors.New("isotp: wrong sequence number")
	// ErrTooLarge reports a message larger than Config.MaxMessageSize or
	// than the protocol allows.
	ErrTooLarge = errors.New("isotp: message too large")
	// ErrInterrupted reports a reception that a new single or first
	// frame replaced.
	ErrInterrupted = errors.New("isotp: reception interrupted by a new message")
	// ErrProtocol reports an invalid flow control frame.
	ErrProtocol = errors.New("isotp: protocol error")
)

// Config configures an ISO-TP connection. The same Config works for the
// userspace [Conn] and the kernel socket.
type Config struct {
	// TxID is the identifier of frames this side sends, RxID that of the
	// frames it receives. ExtendedIDs makes both 29-bit.
	TxID, RxID  canbus.ID
	ExtendedIDs bool

	// Addressing mode; TxAddress is the first byte of frames sent and
	// RxAddress the first byte expected in frames received (extended
	// and mixed addressing).
	Addressing           Addressing
	TxAddress, RxAddress byte

	// FD sends CAN FD frames of up to TxDL bytes (8, 12, 16, 20, 24, 32,
	// 48 or 64; default 64 with FD, 8 without). BRS sets bit rate
	// switching on them.
	FD   bool
	TxDL int
	BRS  bool

	// BlockSize and STmin are sent in the flow control frames of this
	// receiver: how many consecutive frames the sender may send before
	// the next flow control (0: all), and the minimum gap between them
	// (0 to 127 ms, or 100 µs steps from 100 to 900 µs).
	BlockSize uint8
	STmin     time.Duration
	// ForceTxSTmin, when set, replaces the STmin the peer asks for.
	ForceTxSTmin *time.Duration

	// Padding pads every frame to its full length (8 bytes, or the CAN
	// FD length) with PadByte (default 0xCC). CAN FD frames are always
	// padded to a valid CAN FD length.
	Padding bool
	PadByte *byte

	// TimeoutBs is N_Bs, the wait for a flow control frame (default 1 s);
	// TimeoutCr is N_Cr, the wait for a consecutive frame (default 1 s).
	TimeoutBs, TimeoutCr time.Duration
	// MaxWait is the number of WAIT flow controls accepted in a row
	// (WFTmax; default 10).
	MaxWait int
	// MaxMessageSize bounds received and sent messages (default 4095;
	// up to 1<<32−1 with the first frame escape). A larger first frame
	// is answered with OVERFLOW.
	MaxMessageSize int
	// SendFlowWait makes this receiver answer the first frame with this
	// many WAIT flow controls before CTS (for tests of peers).
	SendFlowWait int
	// QueueSize is the number of received messages (and errors) held
	// for Receive (default 16).
	QueueSize int

	Logger  *slog.Logger
	Metrics Metrics
}

// Metrics observes an ISO-TP connection. Methods must be safe for
// concurrent use and must not block. Embed [NopMetrics].
type Metrics interface {
	OnMessageSent(size int, d time.Duration)
	OnMessageReceived(size int, d time.Duration)
	OnError(op string, err error)
}

// NopMetrics is a [Metrics] that does nothing.
type NopMetrics struct{}

// OnMessageSent does nothing.
func (NopMetrics) OnMessageSent(int, time.Duration) {}

// OnMessageReceived does nothing.
func (NopMetrics) OnMessageReceived(int, time.Duration) {}

// OnError does nothing.
func (NopMetrics) OnError(string, error) {}

// withDefaults validates the configuration and fills defaults.
func (c Config) withDefaults() (Config, error) {
	bad := func(format string, args ...any) (Config, error) {
		return c, fmt.Errorf("%w: isotp: "+format, append([]any{canbus.ErrInvalidOption}, args...)...)
	}
	maxID := canbus.MaxStandardID
	if c.ExtendedIDs {
		maxID = canbus.MaxExtendedID
	}
	if c.TxID > maxID || c.RxID > maxID {
		return bad("identifier out of range")
	}
	if c.Addressing > Mixed {
		return bad("unknown addressing %d", c.Addressing)
	}
	if c.TxDL == 0 {
		c.TxDL = 8
		if c.FD {
			c.TxDL = 64
		}
	}
	if !canbus.ValidFDLen(c.TxDL) || c.TxDL < 8 || !c.FD && c.TxDL != 8 {
		return bad("TX_DL %d (8 for classical CAN, 8..64 CAN FD lengths with FD)", c.TxDL)
	}
	if c.BRS && !c.FD {
		return bad("BRS needs FD")
	}
	if _, err := encodeSTmin(c.STmin); err != nil {
		return c, err
	}
	if c.ForceTxSTmin != nil && (*c.ForceTxSTmin < 0 || *c.ForceTxSTmin > 127*time.Millisecond) {
		return bad("ForceTxSTmin %v", *c.ForceTxSTmin)
	}
	if c.PadByte == nil {
		b := byte(0xCC)
		c.PadByte = &b
	}
	if c.TimeoutBs <= 0 {
		c.TimeoutBs = time.Second
	}
	if c.TimeoutCr <= 0 {
		c.TimeoutCr = time.Second
	}
	if c.MaxWait <= 0 {
		c.MaxWait = 10
	}
	if c.MaxMessageSize <= 0 {
		c.MaxMessageSize = 4095
	}
	if uint64(c.MaxMessageSize) > 1<<32-1 {
		return bad("MaxMessageSize %d exceeds 2^32-1", c.MaxMessageSize)
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 16
	}
	if c.Logger == nil {
		c.Logger = slog.New(discard{})
	}
	if c.Metrics == nil {
		c.Metrics = NopMetrics{}
	}
	return c, nil
}

// encodeSTmin converts a duration to the STmin byte.
func encodeSTmin(d time.Duration) (byte, error) {
	switch {
	case d < 0:
	case d < time.Millisecond && d%(100*time.Microsecond) == 0 && d > 0:
		return byte(0xF0 + d/(100*time.Microsecond)), nil
	case d%time.Millisecond == 0 && d <= 127*time.Millisecond:
		return byte(d / time.Millisecond), nil
	}
	return 0, fmt.Errorf("%w: isotp: STmin %v (0..127 ms in ms steps, or 100..900 µs in 100 µs steps)", canbus.ErrInvalidOption, d)
}

// EncodeSTmin is the STmin byte of a duration, for kernel sockets and
// tools.
func EncodeSTmin(d time.Duration) (byte, error) { return encodeSTmin(d) }

// DecodeSTmin converts an STmin byte to a duration. Reserved values mean
// 127 ms, as ISO 15765-2 requires of a sender.
func DecodeSTmin(b byte) time.Duration {
	switch {
	case b <= 0x7F:
		return time.Duration(b) * time.Millisecond
	case b >= 0xF1 && b <= 0xF9:
		return time.Duration(b-0xF0) * 100 * time.Microsecond
	}
	return 127 * time.Millisecond
}

type discard struct{}

func (discard) Enabled(context.Context, slog.Level) bool  { return false }
func (discard) Handle(context.Context, slog.Record) error { return nil }
func (d discard) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discard) WithGroup(string) slog.Handler           { return d }
