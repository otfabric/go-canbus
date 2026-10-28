// SPDX-License-Identifier: MIT

// Package chaos injects faults between two CAN transports.
//
// A [Bridge] forwards frames from one transport to another (for example
// between two vcan interfaces, or a vcan interface and a virtual bus) and,
// with a seeded pseudo-random generator, drops, duplicates, delays,
// reorders and corrupts them. Put it between two peers that are each
// connected to only one side, and every frame between them crosses it:
// this tests real implementations (the Linux kernel's ISO-TP, other
// stacks, devices on a bench bus) under faults no physical bus would
// produce on demand.
//
// Between two [canbus.XLTransport]s (CAN XL sockets, virtual bus ports
// opened with CAN XL) CAN XL frames are forwarded too, with the same
// faults.
//
// Faults are userspace emulation of what a receiver sees. They do not
// model bit errors, arbitration or the retransmissions a real controller
// performs after a CRC error: corruption here delivers a frame that a real
// bus would have rejected.
package chaos

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
)

// Config sets the fault probabilities (0..1) of a Bridge.
type Config struct {
	Seed      int64         // seeds the generator (default 1)
	Drop      float64       // a frame is not forwarded
	Duplicate float64       // a frame is forwarded twice
	Delay     float64       // a frame is held for DelayBy (later frames wait behind it)
	DelayBy   time.Duration // default 5 ms
	Reorder   float64       // a frame is held back and sent after the next one
	Corrupt   float64       // one payload bit is flipped
	// Filter limits the faults to matching frames; others pass untouched.
	Filter *canbus.FilterSet
}

// Stats counts what a bridge did.
type Stats struct {
	Received   uint64 `json:"received"`
	Forwarded  uint64 `json:"forwarded"`
	Dropped    uint64 `json:"dropped"`
	Duplicated uint64 `json:"duplicated"`
	Delayed    uint64 `json:"delayed"`
	Reordered  uint64 `json:"reordered"`
	Corrupted  uint64 `json:"corrupted"`
	// SkippedXL counts CAN XL frames the destination cannot send (it is no
	// XLTransport, or reports canbus.ErrUnsupported).
	SkippedXL uint64 `json:"skippedXL"`
}

// Bridge forwards frames from one transport to another with faults.
type Bridge struct {
	cfg Config
	rng *rand.Rand
	mu  sync.Mutex
	st  struct {
		received, forwarded, dropped, duplicated, delayed, reordered, corrupted, skippedXL atomic.Uint64
	}
}

// New returns a bridge. Probabilities outside 0..1 are an error.
func New(cfg Config) (*Bridge, error) {
	for _, p := range []float64{cfg.Drop, cfg.Duplicate, cfg.Delay, cfg.Reorder, cfg.Corrupt} {
		if p < 0 || p > 1 {
			return nil, fmt.Errorf("%w: chaos: probability %v outside 0..1", canbus.ErrInvalidOption, p)
		}
	}
	if cfg.Seed == 0 {
		cfg.Seed = 1
	}
	if cfg.DelayBy <= 0 {
		cfg.DelayBy = 5 * time.Millisecond
	}
	return &Bridge{cfg: cfg, rng: rand.New(rand.NewSource(cfg.Seed))}, nil
}

// Stats returns the counters.
func (b *Bridge) Stats() Stats {
	return Stats{b.st.received.Load(), b.st.forwarded.Load(), b.st.dropped.Load(), b.st.duplicated.Load(),
		b.st.delayed.Load(), b.st.reordered.Load(), b.st.corrupted.Load(), b.st.skippedXL.Load()}
}

// draw returns the fault decisions for one frame. All five values are
// drawn every time so the sequence does not depend on which faults are on.
func (b *Bridge) draw() (drop, dup, delay, reorder, corrupt bool, bit int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := &b.cfg
	drop = b.rng.Float64() < c.Drop
	dup = b.rng.Float64() < c.Duplicate
	delay = b.rng.Float64() < c.Delay
	reorder = b.rng.Float64() < c.Reorder
	corrupt = b.rng.Float64() < c.Corrupt
	bit = b.rng.Intn(1 << 14) // any bit of a 2048-byte CAN XL payload
	return
}

// packet is one frame in flight: f, or the CAN XL frame x.
type packet struct {
	f  canbus.Frame
	x  canbus.XLFrame
	xl bool
}

// Run forwards frames from src to dst until ctx ends or src fails; it
// returns nil when ctx ended. Error frames and src's own echoes are not
// forwarded. CAN XL frames are forwarded when src and dst are
// [canbus.XLTransport]s. Run may be started once per direction.
func (b *Bridge) Run(ctx context.Context, src, dst canbus.Transport) (err error) {
	defer func() {
		if errors.Is(err, errEnded) {
			err = nil // ctx ended during a send
		}
	}()
	xsrc, _ := src.(canbus.XLTransport)
	xdst, _ := dst.(canbus.XLTransport)
	var held *packet // a frame waiting to be reordered
	sendCtx := func(ctx context.Context, p *packet) error {
		var err error
		if p.xl {
			if xdst == nil {
				b.st.skippedXL.Add(1)
				return nil
			}
			out := p.x
			out.Timestamp, out.Interface, out.Echo = time.Time{}, "", false
			err = xdst.SendXL(ctx, &out)
			if errors.Is(err, canbus.ErrUnsupported) {
				b.st.skippedXL.Add(1)
				return nil
			}
		} else {
			out := p.f
			out.Timestamp, out.Interface, out.Echo = time.Time{}, "", false
			err = dst.Send(ctx, &out)
		}
		if err != nil {
			if ended(ctx, err) {
				return errEnded
			}
			return err
		}
		b.st.forwarded.Add(1)
		return nil
	}
	send := func(p *packet) error { return sendCtx(ctx, p) }
	var p packet
	for {
		var err error
		if xsrc != nil {
			p.xl, err = xsrc.ReceiveAny(ctx, &p.f, &p.x)
		} else {
			p.xl, err = false, src.Receive(ctx, &p.f)
		}
		if err != nil {
			if ended(ctx, err) || errors.Is(err, canbus.ErrClosed) {
				if held != nil { // flush the reordered frame with a fresh deadline
					fctx, cancel := context.WithTimeout(context.Background(), time.Second)
					_ = sendCtx(fctx, held)
					cancel()
				}
				return nil
			}
			return err
		}
		if p.xl && p.x.Echo || !p.xl && (p.f.IsError() || p.f.Echo) {
			continue
		}
		b.st.received.Add(1)
		if f := b.cfg.Filter; f != nil && (p.xl && !f.MatchXL(&p.x) || !p.xl && !f.Match(&p.f)) {
			if err := send(&p); err != nil {
				return err
			}
			continue
		}
		drop, dup, delay, reorder, corrupt, bit := b.draw()
		if drop {
			b.st.dropped.Add(1)
			continue
		}
		switch {
		case corrupt && p.xl:
			bit %= len(p.x.Data) * 8
			p.x.Data[bit/8] ^= 1 << (bit % 8)
			b.st.corrupted.Add(1)
		case corrupt && !p.f.IsRemote() && p.f.Len > 0:
			bit %= int(p.f.Len) * 8
			p.f.Data[bit/8] ^= 1 << (bit % 8)
			b.st.corrupted.Add(1)
		}
		if delay {
			b.st.delayed.Add(1)
			t := time.NewTimer(b.cfg.DelayBy)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil
			case <-t.C:
			}
		}
		if reorder && held == nil {
			b.st.reordered.Add(1)
			c := p
			c.x = p.x.Clone() // p.x.Data is reused by the next receive
			held = &c
			continue
		}
		if err := send(&p); err != nil {
			return err
		}
		if held != nil {
			if err := send(held); err != nil {
				return err
			}
			held = nil
		}
		if dup {
			b.st.duplicated.Add(1)
			if err := send(&p); err != nil {
				return err
			}
		}
	}
}

// Both runs the bridge in both directions between x and y until ctx
// ends or one direction fails, and returns the first error.
func (b *Bridge) Both(ctx context.Context, x, y canbus.Transport) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- b.Run(ctx, x, y) }()
	go func() { errc <- b.Run(ctx, y, x) }()
	err := <-errc
	cancel() // one direction ended: stop the other
	if err2 := <-errc; err == nil {
		err = err2
	}
	return err
}

var errEnded = errors.New("chaos: ended")

// ended reports that ctx is done, or that err is the deadline of ctx
// seen by a transport a moment before the context's own timer fires.
func ended(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, canbus.ErrTimeout) {
		return false
	}
	dl, ok := ctx.Deadline()
	return ok && !time.Now().Before(dl.Add(-time.Millisecond))
}
