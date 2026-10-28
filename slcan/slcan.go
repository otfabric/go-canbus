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
// The protocol has no error frames, no error state indicator and no echo
// of sent frames; receive timestamps are taken on arrival.
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
	wmu   sync.Mutex
	buf   []byte

	closeOnce sync.Once
	readErr   error // why the reader stopped, set before ended closes
	// polled: rw's reads time out, so Close waits for the reader before
	// closing it (a serial port must not be closed during a read).
	polled bool

	txFrames, txBytes, rxFrames, rxBytes, dropped, txErrors, rxFailures atomic.Uint64
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
	c := &Conn{rw: rw, cfg: cfg, polled: polled, ch: make(chan canbus.Frame, cfg.QueueSize), done: make(chan struct{}), ended: make(chan struct{})}
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
			n = 0 // go-serial returns -1 with the error of a failed read
		}
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
	ok, err := ParseFrame(line, &f)
	if err != nil {
		c.rxFailures.Add(1)
		return
	}
	if !ok {
		return
	}
	f.Timestamp = time.Now()
	f.Interface = c.cfg.Name
	c.rxFrames.Add(1)
	c.rxBytes.Add(uint64(len(f.Payload())))
	select {
	case c.ch <- f:
	default:
		c.dropped.Add(1)
	}
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
	c.wmu.Lock()
	defer c.wmu.Unlock()
	var err error
	c.buf, err = AppendFrame(c.buf[:0], f)
	if err != nil {
		return err
	}
	if _, err := c.rw.Write(c.buf); err != nil {
		c.txErrors.Add(1)
		select {
		case <-c.done:
			return canbus.ErrClosed
		default:
		}
		return fmt.Errorf("%w: slcan: %v", canbus.ErrInterfaceDown, err)
	}
	c.txFrames.Add(1)
	c.txBytes.Add(uint64(len(f.Payload())))
	return nil
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

// Close sends "C" (unless NoCommands) and closes the device. Once the
// device is gone, errors of closing it are not reported.
func (c *Conn) Close() error {
	err := canbus.ErrClosed
	c.closeOnce.Do(func() {
		close(c.done)
		if !c.cfg.NoCommands {
			c.wmu.Lock()
			_, _ = c.rw.Write([]byte("C\r"))
			c.wmu.Unlock()
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

// Capabilities describes slcan: CAN FD (with adapters that support it),
// no error frames, filters, timestamps or echoes.
func (c *Conn) Capabilities() canbus.Capabilities {
	return canbus.Capabilities{FD: true, Physical: true}
}

// Stats returns the counters. RxFailure counts malformed records.
func (c *Conn) Stats() canbus.Stats {
	return canbus.Stats{TxFrames: c.txFrames.Load(), TxBytes: c.txBytes.Load(), RxFrames: c.rxFrames.Load(),
		RxBytes: c.rxBytes.Load(), Dropped: c.dropped.Load(), TxErrors: c.txErrors.Load(), RxFailure: c.rxFailures.Load()}
}

// Drops returns the frames lost because Receive was not called fast
// enough.
func (c *Conn) Drops() (uint64, canbus.DropReason) {
	return c.dropped.Load(), canbus.DropQueueFull
}
