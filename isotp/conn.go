// SPDX-License-Identifier: MIT

package isotp

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
)

// PCI types (high nibble of the first PCI byte).
const (
	pciSF = 0
	pciFF = 1
	pciCF = 2
	pciFC = 3

	fcCTS      = 0
	fcWait     = 1
	fcOverflow = 2
)

type result struct {
	data []byte
	err  error
}

type flowControl struct {
	status, bs, stmin byte
}

// Conn is a userspace ISO-TP connection on a [canbus.Bus]. Send may be
// called from several goroutines (calls are serialized), Receive from
// one at a time. The connection does not own the bus.
type Conn struct {
	cfg Config
	bus *canbus.Bus
	sub *canbus.Subscription
	off int // 1 with extended or mixed addressing

	rx   chan result
	fc   chan flowControl
	mu   sync.Mutex // serializes Send
	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once

	// Reception state, owned by the reader goroutine.
	rxBuf   []byte
	rxLen   int
	rxSN    byte
	rxBlock int
	rxStart time.Time
	rxOn    bool
}

// Dial starts an ISO-TP connection on bus: it subscribes to RxID and
// starts the goroutine that receives frames.
func Dial(bus *canbus.Bus, cfg Config) (*Conn, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	f := canbus.ExactStandard(cfg.RxID)
	if cfg.ExtendedIDs {
		f = canbus.ExactExtended(cfg.RxID)
	}
	f.Remote = canbus.RemoteDataOnly
	sub, err := bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{f}}),
		canbus.WithoutEcho(), canbus.WithQueueSize(4096))
	if err != nil {
		return nil, err
	}
	c := &Conn{cfg: cfg, bus: bus, sub: sub, rx: make(chan result, cfg.QueueSize),
		fc: make(chan flowControl, 64), done: make(chan struct{})}
	if cfg.Addressing != Normal {
		c.off = 1
	}
	c.wg.Add(1)
	go c.run()
	return c, nil
}

// Close stops the connection. Blocked Send and Receive calls return
// [canbus.ErrClosed].
func (c *Conn) Close() error {
	c.once.Do(func() {
		close(c.done)
		_ = c.sub.Close()
		c.wg.Wait()
	})
	return nil
}

// Receive returns the next complete message, or a reception error
// (ErrTimeout for N_Cr, ErrSequence, ErrInterrupted) as the kernel's
// ISO-TP sockets report them.
func (c *Conn) Receive(ctx context.Context) ([]byte, error) {
	select {
	case r := <-c.rx:
		return r.data, r.err
	case <-c.done:
		return nil, canbus.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// frame builds a frame of the connection's format from PCI and data.
func (c *Conn) frame(body []byte) canbus.Frame {
	var f canbus.Frame
	f.ID = c.cfg.TxID
	if c.cfg.ExtendedIDs {
		f.Flags |= canbus.FlagExtended
	}
	n := 0
	if c.off == 1 {
		f.Data[0] = c.cfg.TxAddress
		n = 1
	}
	n += copy(f.Data[n:], body)
	size := n
	switch {
	case c.cfg.FD:
		f.Flags |= canbus.FlagFD
		if c.cfg.BRS {
			f.Flags |= canbus.FlagBRS
		}
		if c.cfg.Padding && size < 8 {
			size = 8
		}
		size = canbus.RoundUpFDLen(size)
	case c.cfg.Padding:
		size = 8
	}
	for i := n; i < size; i++ {
		f.Data[i] = *c.cfg.PadByte
	}
	f.Len = uint8(size)
	return f
}

func (c *Conn) send(ctx context.Context, body []byte) error {
	f := c.frame(body)
	return c.bus.Send(ctx, &f)
}

// sfMax returns the largest single frame payload.
func (c *Conn) sfMax() int {
	if c.cfg.TxDL > 8 {
		return c.cfg.TxDL - 2 - c.off
	}
	return 7 - c.off
}

// Send transmits one message and returns when its last frame was sent.
func (c *Conn) Send(ctx context.Context, payload []byte) error {
	start := time.Now()
	err := c.sendMessage(ctx, payload)
	if err != nil {
		c.cfg.Metrics.OnError("send", err)
		c.cfg.Logger.Debug("isotp: send failed", "tx", uint32(c.cfg.TxID), "size", len(payload), "error", err)
		return err
	}
	c.cfg.Metrics.OnMessageSent(len(payload), time.Since(start))
	return nil
}

func (c *Conn) sendMessage(ctx context.Context, p []byte) error {
	n := len(p)
	switch {
	case n == 0:
		return fmt.Errorf("%w: isotp: empty message", canbus.ErrInvalidOption)
	case n > c.cfg.MaxMessageSize:
		return fmt.Errorf("%w: %d bytes, MaxMessageSize is %d", ErrTooLarge, n, c.cfg.MaxMessageSize)
	case c.cfg.ListenOnly:
		return fmt.Errorf("%w: isotp: a listen-only connection cannot send", canbus.ErrUnsupported)
	}
	select {
	case <-c.done:
		return canbus.ErrClosed
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Single frame.
	if n <= 7-c.off {
		return c.send(ctx, append([]byte{byte(n)}, p...))
	}
	if n <= c.sfMax() {
		return c.send(ctx, append([]byte{0, byte(n)}, p...))
	}
	// Drop flow controls left over from an earlier transfer.
	for drained := false; !drained; {
		select {
		case <-c.fc:
		default:
			drained = true
		}
	}
	var first []byte
	if n <= 4095 {
		first = []byte{0x10 | byte(n>>8), byte(n)}
	} else {
		first = []byte{0x10, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(first[2:], uint32(n))
	}
	room := c.cfg.TxDL - c.off - len(first)
	sent := room
	if err := c.send(ctx, append(first, p[:room]...)); err != nil {
		return err
	}
	cfRoom := c.cfg.TxDL - 1 - c.off
	sn := byte(1)
	for sent < n {
		fc, err := c.waitFC(ctx)
		if err != nil {
			return err
		}
		gap := DecodeSTmin(fc.stmin)
		if c.cfg.ForceTxSTmin != nil {
			gap = *c.cfg.ForceTxSTmin
		}
		for blk := 0; sent < n && (fc.bs == 0 || blk < int(fc.bs)); blk++ {
			if blk > 0 && gap > 0 {
				if err := sleep(ctx, c.done, gap); err != nil {
					return err
				}
			}
			end := min(sent+cfRoom, n)
			if err := c.send(ctx, append([]byte{0x20 | sn}, p[sent:end]...)); err != nil {
				return err
			}
			sent = end
			sn = (sn + 1) & 0x0F
		}
	}
	return nil
}

func sleep(ctx context.Context, done <-chan struct{}, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-done:
		return canbus.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitFC waits for a CTS flow control, following WAIT up to MaxWait.
func (c *Conn) waitFC(ctx context.Context) (flowControl, error) {
	waits := 0
	t := time.NewTimer(c.cfg.TimeoutBs)
	defer t.Stop()
	for {
		select {
		case fc := <-c.fc:
			switch fc.status {
			case fcCTS:
				return fc, nil
			case fcWait:
				waits++
				if waits > c.cfg.MaxWait {
					return fc, fmt.Errorf("%w (%d)", ErrWaitLimit, waits)
				}
				t.Reset(c.cfg.TimeoutBs)
			case fcOverflow:
				return fc, ErrOverflow
			default:
				return fc, fmt.Errorf("%w: flow status %d", ErrProtocol, fc.status)
			}
		case <-t.C:
			return flowControl{}, fmt.Errorf("%w: no flow control within N_Bs (%v)", ErrTimeout, c.cfg.TimeoutBs)
		case <-c.done:
			return flowControl{}, canbus.ErrClosed
		case <-ctx.Done():
			return flowControl{}, ctx.Err()
		}
	}
}

func (c *Conn) run() {
	defer c.wg.Done()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var crC <-chan time.Time
	for {
		select {
		case <-c.done:
			return
		case <-c.sub.Done():
			// The bus ended: drain what is queued, then stop.
			for {
				select {
				case f := <-c.sub.Frames():
					c.handle(&f, timer, &crC)
				default:
					c.deliver(nil, canbus.ErrClosed)
					return
				}
			}
		case <-crC:
			crC = nil
			c.rxOn = false
			c.deliver(nil, fmt.Errorf("%w: no consecutive frame within N_Cr (%v)", ErrTimeout, c.cfg.TimeoutCr))
		case f := <-c.sub.Frames():
			c.handle(&f, timer, &crC)
		}
	}
}

func (c *Conn) deliver(data []byte, err error) {
	if err != nil {
		c.cfg.Metrics.OnError("receive", err)
		c.cfg.Logger.Debug("isotp: receive failed", "rx", uint32(c.cfg.RxID), "error", err)
	} else {
		c.cfg.Metrics.OnMessageReceived(len(data), time.Since(c.rxStart))
	}
	select {
	case c.rx <- result{data, err}:
	default:
		c.cfg.Logger.Warn("isotp: receive queue full, message dropped", "rx", uint32(c.cfg.RxID))
	}
}

func (c *Conn) sendFC(status byte) {
	if c.cfg.ListenOnly {
		return
	}
	st, _ := encodeSTmin(c.cfg.STmin)
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.TimeoutBs)
	defer cancel()
	if err := c.send(ctx, []byte{0x30 | status, c.cfg.BlockSize, st}); err != nil {
		c.cfg.Logger.Warn("isotp: flow control not sent", "error", err)
	}
}

func (c *Conn) handle(f *canbus.Frame, timer *time.Timer, crC *<-chan time.Time) {
	data := f.Payload()
	if c.off == 1 {
		if len(data) < 1 || data[0] != c.cfg.RxAddress {
			return
		}
		data = data[1:]
	}
	if len(data) < 1 {
		return
	}
	stopCr := func() {
		timer.Stop()
		*crC = nil
	}
	startCr := func() {
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}
		timer.Reset(c.cfg.TimeoutCr)
		*crC = timer.C
	}
	switch data[0] >> 4 {
	case pciSF:
		var msg []byte
		if l := int(data[0] & 0x0F); l != 0 {
			if l > len(data)-1 || l > 7-c.off {
				return
			}
			msg = data[1 : 1+l]
		} else {
			if len(data) < 2 || data[1] == 0 || int(data[1]) > len(data)-2 || len(data) <= 8 {
				return // escape sequence only in CAN FD frames of more than 8 bytes
			}
			msg = data[2 : 2+int(data[1])]
		}
		if c.rxOn {
			c.rxOn = false
			stopCr()
			c.deliver(nil, ErrInterrupted)
		}
		c.rxStart = time.Now()
		c.deliver(append([]byte(nil), msg...), nil)
	case pciFF:
		if len(data) < 2 {
			return
		}
		l := int(data[0]&0x0F)<<8 | int(data[1])
		hdr := 2
		if l == 0 {
			if len(data) < 6 {
				return
			}
			u := binary.BigEndian.Uint32(data[2:6])
			hdr = 6
			if u <= 4095 {
				return // the escape is for lengths above 4095 only
			}
			if uint64(u) > uint64(c.cfg.MaxMessageSize) {
				if c.rxOn {
					c.rxOn = false
					stopCr()
					c.deliver(nil, ErrInterrupted)
				}
				c.sendFC(fcOverflow)
				c.deliver(nil, fmt.Errorf("%w: first frame announces %d bytes, MaxMessageSize is %d", ErrTooLarge, u, c.cfg.MaxMessageSize))
				return
			}
			l = int(u)
		}
		if l <= len(data)-hdr || l < 8-c.off {
			return // would have fitted a single frame
		}
		if c.rxOn {
			c.rxOn = false
			stopCr()
			c.deliver(nil, ErrInterrupted)
		}
		if l > c.cfg.MaxMessageSize {
			c.sendFC(fcOverflow)
			c.deliver(nil, fmt.Errorf("%w: first frame announces %d bytes, MaxMessageSize is %d", ErrTooLarge, l, c.cfg.MaxMessageSize))
			return
		}
		c.rxStart = time.Now()
		c.rxBuf = append(make([]byte, 0, l), data[hdr:]...)
		c.rxLen, c.rxSN, c.rxBlock, c.rxOn = l, 1, 0, true
		for i := 0; i < c.cfg.SendFlowWait; i++ {
			c.sendFC(fcWait)
		}
		c.sendFC(fcCTS)
		startCr()
	case pciCF:
		if !c.rxOn {
			return
		}
		if sn := data[0] & 0x0F; sn != c.rxSN {
			c.rxOn = false
			stopCr()
			c.deliver(nil, fmt.Errorf("%w: got %d, want %d", ErrSequence, sn, c.rxSN))
			return
		}
		need := c.rxLen - len(c.rxBuf)
		c.rxBuf = append(c.rxBuf, data[1:min(len(data), 1+need)]...)
		c.rxSN = (c.rxSN + 1) & 0x0F
		if len(c.rxBuf) == c.rxLen {
			c.rxOn = false
			stopCr()
			c.deliver(c.rxBuf, nil)
			c.rxBuf = nil
			return
		}
		c.rxBlock++
		if !c.cfg.ListenOnly && c.cfg.BlockSize > 0 && c.rxBlock == int(c.cfg.BlockSize) {
			c.rxBlock = 0
			c.sendFC(fcCTS)
		}
		startCr()
	case pciFC:
		if len(data) < 3 {
			return
		}
		fc := flowControl{status: data[0] & 0x0F, bs: data[1], stmin: data[2]}
		// Every flow control counts (a burst of WAITs must reach the
		// sender's WFTmax check); a flood beyond the buffer is dropped.
		select {
		case c.fc <- fc:
		default:
			c.cfg.Logger.Warn("isotp: flow control frames dropped", "rx", uint32(c.cfg.RxID))
		}
	}
}
