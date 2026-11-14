// SPDX-License-Identifier: MIT

// Package slcan is a CAN transport over the serial-line CAN protocol
// (LAWICEL ASCII, "slcan") of USB-to-CAN adapters such as the CANable,
// CANtact, USBtin and LAWICEL CANUSB, without the Linux slcan driver: it
// works on every operating system go-serial supports and over any byte
// stream (a pseudo-terminal, a TCP connection to a serial server).
//
// Frames are text records ended by "\r": "t1232A1B2" is a standard data
// frame 123 with two bytes, "T" an extended one, "r"/"R" remote frames and
// "d"/"D"/"b"/"B" CAN FD frames without and with bit rate switch (the
// extension of the CANable 2.0 firmware). Open sends "C" (close), the bit
// rate ("S0".."S8", "Y2"/"Y5" for the CAN FD data phase) and "O" (open) or
// "L" (listen only); Close sends "C". Adapter replies ("\r", "\a", "z",
// "Z") and records that are not frames are skipped.
//
// Adapters that report their status send "e" records (bus errors, such
// as "e1a" for an ACK error) and "s" records (controller state and error
// counters, such as "sp130012"); a Conn turns them into error frames
// exactly as the Linux slcan driver does, delivered when the filters'
// ErrorMask selects them. The protocol has no error state indicator and
// no echo of sent frames; receive timestamps are taken on arrival.
//
// A peer that stops reading cannot hold up a Conn: when the byte stream
// has write deadlines (SetWriteDeadline, as a net.Conn or a pollable
// *os.File has), Send ends at its context's deadline or
// cancellation; on any stream, Close gives the closing "C" at most
// 100 ms and then closes the stream, which ends a blocked write. On a
// stream without write deadlines a write blocked on such a peer ends
// only when Close closes the stream.
package slcan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	serial "github.com/otfabric/go-serial"
)

// Bitrates are the nominal bit rates of the S commands.
var Bitrates = map[int]string{
	10000: "S0", 20000: "S1", 50000: "S2", 100000: "S3", 125000: "S4",
	250000: "S5", 500000: "S6", 750000: "S7", 1000000: "S8", 83300: "S9",
}

// DataBitrates are the CAN FD data phase bit rates of the Y commands of
// the CANable 2.0 firmware.
var DataBitrates = map[int]string{2000000: "Y2", 5000000: "Y5"}

// Config configures a Conn.
type Config struct {
	// Bitrate sets the nominal bit rate (a key of [Bitrates]); 0 keeps
	// the adapter's setting.
	Bitrate int
	// DataBitrate sets the CAN FD data phase bit rate (a key of
	// [DataBitrates]); 0 sends none.
	DataBitrate int
	// ListenOnly opens the channel with "L": the adapter neither sends nor
	// acknowledges frames.
	ListenOnly bool
	// NoCommands sends no setup or close commands: for a peer that is not
	// an adapter, such as the Linux slcan line discipline or another
	// program.
	NoCommands bool
	// Name is the interface name (default "slcan").
	Name string
	// QueueSize is the number of received frames held for Receive
	// (default 1024); more are dropped and counted.
	QueueSize int
}

// Conn is a CAN transport over slcan. It implements [canbus.Transport].
type Conn struct {
	rw    io.ReadWriteCloser
	cfg   Config
	ch    chan canbus.Frame
	done  chan struct{} // closed by Close
	ended chan struct{} // closed when the reader stops
	// wsem is held by the one write in progress; a channel rather than
	// a mutex so that waiting for it honours a context, and Close can
	// skip it.
	wsem chan struct{}
	buf  []byte
	// partial: a write ended part way through a record, so the next
	// record starts with "\r" to end the fragment.
	partial bool

	filters atomic.Pointer[canbus.FilterSet]
	smu     sync.Mutex // guards state, txErrs, rxErrs
	state   canbus.State
	// txErrs and rxErrs are the error counters of the last state record.
	txErrs, rxErrs int

	closeOnce sync.Once
	readErr   error // why the reader stopped, set before ended closes
	// polled: rw's reads time out, so Close waits for the reader before
	// closing it (a serial port must not be closed during a read).
	polled bool

	txFrames, txBytes, rxFrames, rxBytes, rxErrors, dropped, txErrors, rxFailures atomic.Uint64
}

// New starts a Conn on rw, which it owns: Close closes it. Unless
// cfg.NoCommands, it first sends the setup commands.
func New(rw io.ReadWriteCloser, cfg Config) (*Conn, error) { return newConn(rw, cfg, false) }

func newConn(rw io.ReadWriteCloser, cfg Config, polled bool) (*Conn, error) {
	if cfg.Name == "" {
		cfg.Name = "slcan"
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1024
	}
	var setup []string
	if !cfg.NoCommands {
		setup = append(setup, "C")
		if cfg.Bitrate != 0 {
			s, ok := Bitrates[cfg.Bitrate]
			if !ok {
				return nil, fmt.Errorf("%w: slcan: bit rate %d", canbus.ErrInvalidOption, cfg.Bitrate)
			}
			setup = append(setup, s)
		}
		if cfg.DataBitrate != 0 {
			s, ok := DataBitrates[cfg.DataBitrate]
			if !ok {
				return nil, fmt.Errorf("%w: slcan: data bit rate %d", canbus.ErrInvalidOption, cfg.DataBitrate)
			}
			setup = append(setup, s)
		}
		if cfg.ListenOnly {
			setup = append(setup, "L")
		} else {
			setup = append(setup, "O")
		}
	}
	c := &Conn{rw: rw, cfg: cfg, polled: polled, ch: make(chan canbus.Frame, cfg.QueueSize), done: make(chan struct{}), ended: make(chan struct{}),
		wsem: make(chan struct{}, 1), state: canbus.StateActive}
	c.filters.Store(&canbus.FilterSet{})
	for _, s := range setup {
		if _, err := rw.Write([]byte(s + "\r")); err != nil {
			_ = rw.Close()
			return nil, fmt.Errorf("slcan: %s: %w", s, err)
		}
	}
	go c.read()
	return c, nil
}

// Open opens a serial adapter at device (for example /dev/ttyACM0 or
// COM3) with the given serial baud rate (0: 115200; USB adapters ignore
// it) and starts a Conn on it.
func Open(device string, baud int, cfg Config) (*Conn, error) {
	if baud == 0 {
		baud = 115200
	}
	if cfg.Name == "" {
		cfg.Name = device
	}
	// A finite read timeout lets Close stop the reader: closing a port
	// does not interrupt a blocked read everywhere.
	p, err := serial.Open(&serial.Config{Address: device, BaudRate: serial.BaudRate(baud), Timeout: 100 * time.Millisecond})
	if err != nil {
		return nil, fmt.Errorf("slcan: %w", err)
	}
	return newConn(p, cfg, true)
}

// timeout reports a read that timed out without data (serial ports with a
// read timeout, connections with a deadline).
func timeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.Is(err, serial.ErrTimeout) || errors.Is(err, os.ErrDeadlineExceeded) || errors.As(err, &t) && t.Timeout()
}

func (c *Conn) read() {
	defer close(c.ended)
	buf := make([]byte, 4096)
	var line []byte
	for {
		n, err := c.rw.Read(buf)
		if n < 0 {
			n = 0 // go-serial before v0.1.7 returns -1 with the error of a failed read
		}
		n = min(n, len(buf)) // a reader that overreports must not crash the Conn
		if n == 0 && err == nil {
			// A hung-up tty (adapter unplugged) reads 0 bytes at once,
			// forever: the end of the stream.
			err = io.EOF
		}
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\a':
				c.line(line)
				line = line[:0]
			case '\n':
			default:
				if len(line) < 256 {
					line = append(line, b)
				} else {
					c.rxFailures.Add(1) // garbage without a terminator
					line = line[:0]
				}
			}
		}
		if err != nil {
			if !timeout(err) {
				c.readErr = err
				return
			}
			select {
			case <-c.done:
				return
			default:
			}
		}
	}
}

func (c *Conn) line(line []byte) {
	var f canbus.Frame
	var ok bool
	var err error
	switch {
	case len(line) > 0 && line[0] == 'e':
		ok, err = ParseErrorRecord(line, &f)
	case len(line) > 0 && line[0] == 's':
		ok, err = c.stateRecord(line, &f)
	default:
		ok, err = ParseFrame(line, &f)
	}
	if err != nil {
		c.rxFailures.Add(1)
		return
	}
	if !ok || !c.filters.Load().Match(&f) {
		return
	}
	f.Timestamp = time.Now()
	f.Interface = c.cfg.Name
	if f.IsError() {
		c.rxErrors.Add(1)
	} else {
		c.rxFrames.Add(1)
		c.rxBytes.Add(uint64(len(f.Payload())))
	}
	select {
	case c.ch <- f:
	default:
		c.dropped.Add(1)
	}
}

// stateRecord applies an "s" record and fills f with the error frame for
// a change of state.
func (c *Conn) stateRecord(line []byte, f *canbus.Frame) (bool, error) {
	st, tx, rx, ok, err := ParseStateRecord(line)
	if !ok {
		return false, err
	}
	c.smu.Lock()
	defer c.smu.Unlock()
	c.txErrs, c.rxErrs = tx, rx
	g, changed := StateFrame(c.state, st, tx, rx)
	c.state = st
	*f = g
	return changed, nil
}

// State returns the controller state and error counters of the last "s"
// record (error active with zero counters until one arrives). Adapters
// that send no state records always report error active.
func (c *Conn) State() (st canbus.State, txErrors, rxErrors int) {
	c.smu.Lock()
	defer c.smu.Unlock()
	return c.state, c.txErrs, c.rxErrs
}

// SetFilters sets the filters, applied in software as records arrive.
// As with SocketCAN, a Conn delivers every data frame and no error
// frames until filters are set; error frames are delivered when their
// class is in the set's ErrorMask.
func (c *Conn) SetFilters(fs canbus.FilterSet) error {
	fs.Filters = append([]canbus.Filter(nil), fs.Filters...)
	c.filters.Store(&fs)
	return nil
}

// Send writes a frame. It returns once the record is written to the
// device; the protocol does not confirm transmission on the bus.
func (c *Conn) Send(ctx context.Context, f *canbus.Frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return canbus.ErrClosed
	default:
	}
	if c.cfg.ListenOnly {
		return fmt.Errorf("%w: slcan: listen-only", canbus.ErrUnsupported)
	}
	select {
	case c.wsem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return canbus.ErrClosed
	}
	defer func() { <-c.wsem }()
	select {
	case <-c.done:
		return canbus.ErrClosed
	default:
	}
	if c.partial {
		c.buf = append(c.buf[:0], '\r')
	} else {
		c.buf = c.buf[:0]
	}
	rec, err := AppendFrame(c.buf, f)
	if err != nil {
		return err
	}
	c.buf = rec
	n, err := c.writeCtx(ctx, c.buf)
	if n > 0 || err == nil {
		c.partial = err != nil && n < len(c.buf)
	}
	if err != nil {
		c.txErrors.Add(1)
		select {
		case <-c.done:
			return canbus.ErrClosed
		default:
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if _, ok := ctx.Deadline(); ok && timeout(err) {
			return context.DeadlineExceeded // the write deadline was the context's
		}
		return fmt.Errorf("%w: slcan: %v", canbus.ErrInterfaceDown, err)
	}
	c.txFrames.Add(1)
	c.txBytes.Add(uint64(len(f.Payload())))
	return nil
}

// writeDeadliner is a byte stream whose writes can be given a deadline.
type writeDeadliner interface{ SetWriteDeadline(time.Time) error }

// writeCtx writes b, ending the write when ctx ends if rw has write
// deadlines. The caller holds wsem.
func (c *Conn) writeCtx(ctx context.Context, b []byte) (int, error) {
	wd, ok := c.rw.(writeDeadliner)
	if !ok {
		return c.rw.Write(b)
	}
	dl, _ := ctx.Deadline() // zero: no deadline
	if err := wd.SetWriteDeadline(dl); err != nil {
		return c.rw.Write(b) // deadlines not supported after all
	}
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = wd.SetWriteDeadline(time.Unix(1, 0)) // in the past: ends the write
		close(fired)
	})
	defer func() {
		if !stop() {
			<-fired // must not move the next write's deadline
		}
	}()
	return c.rw.Write(b)
}

// closeWriteTimeout bounds the closing "C" that Close sends.
const closeWriteTimeout = 100 * time.Millisecond

// sendClose writes "C" unless a write is in progress, giving it at most
// closeWriteTimeout: Close must not wait on a peer that stopped
// reading. The write may outlive the call; closing rw ends it.
func (c *Conn) sendClose() {
	select {
	case c.wsem <- struct{}{}:
	default:
		return // a Send is writing: the stream is closed under it
	}
	wd, hasDeadline := c.rw.(writeDeadliner)
	if hasDeadline {
		hasDeadline = wd.SetWriteDeadline(time.Now().Add(closeWriteTimeout)) == nil
	}
	written := make(chan struct{})
	go func() {
		defer func() { <-c.wsem }()
		defer close(written)
		b := []byte("C\r")
		if c.partial {
			b = []byte("\rC\r")
		}
		_, _ = c.rw.Write(b)
	}()
	if hasDeadline {
		<-written
		return
	}
	t := time.NewTimer(closeWriteTimeout)
	defer t.Stop()
	select {
	case <-written:
	case <-t.C:
	}
}

// Receive waits for the next frame. Once the device is gone it returns
// an error wrapping [canbus.ErrInterfaceDown].
func (c *Conn) Receive(ctx context.Context, f *canbus.Frame) error {
	select {
	case g := <-c.ch:
		*f = g
		return nil
	default:
	}
	select {
	case g := <-c.ch:
		*f = g
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return canbus.ErrClosed
	case <-c.ended:
		select {
		case g := <-c.ch:
			*f = g
			return nil
		case <-c.done:
			return canbus.ErrClosed
		default:
		}
		return fmt.Errorf("%w: slcan: %v", canbus.ErrInterfaceDown, c.readErr)
	}
}

// Close sends "C" (unless NoCommands) and closes the device. It does not
// wait on the peer: the "C" is skipped while a Send is writing and given
// at most 100 ms otherwise; the device is closed regardless, which ends
// a write still blocked. Once the device is gone, errors of closing it
// are not reported.
func (c *Conn) Close() error {
	err := canbus.ErrClosed
	c.closeOnce.Do(func() {
		close(c.done)
		if !c.cfg.NoCommands {
			c.sendClose()
		}
		// Once the device is gone, closing it may fail (a hung-up tty
		// cannot restore its settings): that is not reported.
		if c.polled {
			<-c.ended // the reader sees done within one read timeout
			err = c.rw.Close()
			if c.readErr != nil {
				err = nil
			}
		} else {
			var lost bool
			select {
			case <-c.ended:
				lost = true
			default:
			}
			err = c.rw.Close() // unblocks the reader
			<-c.ended
			if lost {
				err = nil
			}
		}
	})
	if errors.Is(err, canbus.ErrClosed) {
		return nil
	}
	return err
}

// Name returns the interface name.
func (c *Conn) Name() string { return c.cfg.Name }

// Capabilities describes slcan: CAN FD (with adapters that support it)
// and error frames (from adapters that send "e" and "s" records); no
// hardware filters, timestamps or echoes.
func (c *Conn) Capabilities() canbus.Capabilities {
	return canbus.Capabilities{FD: true, ErrorFrames: true, Physical: true}
}

// Stats returns the counters. RxErrors counts delivered error frames,
// RxFailure malformed records.
func (c *Conn) Stats() canbus.Stats {
	return canbus.Stats{TxFrames: c.txFrames.Load(), TxBytes: c.txBytes.Load(), RxFrames: c.rxFrames.Load(), RxErrors: c.rxErrors.Load(),
		RxBytes: c.rxBytes.Load(), Dropped: c.dropped.Load(), TxErrors: c.txErrors.Load(), RxFailure: c.rxFailures.Load()}
}

// Drops returns the frames lost because Receive was not called fast
// enough.
func (c *Conn) Drops() (uint64, canbus.DropReason) {
	return c.dropped.Load(), canbus.DropQueueFull
}
