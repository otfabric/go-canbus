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
}

// Bridge forwards frames from one transport to another with faults.
type Bridge struct {
	cfg Config
	rng *rand.Rand
	mu  sync.Mutex
	st  struct {
		received, forwarded, dropped, duplicated, delayed, reordered, corrupted atomic.Uint64
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
		b.st.delayed.Load(), b.st.reordered.Load(), b.st.corrupted.Load()}
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
	bit = b.rng.Intn(512)
	return
}

// Run forwards frames from src to dst until ctx ends or src fails; it
// returns nil when ctx ended. Error frames and src's own echoes are not
// forwarded. Run may be started once per direction.
func (b *Bridge) Run(ctx context.Context, src, dst canbus.Transport) error {
	var held *canbus.Frame // a frame waiting to be reordered
	sendCtx := func(ctx context.Context, f *canbus.Frame) error {
		out := *f
		out.Timestamp, out.Interface, out.Echo = time.Time{}, "", false
		if err := dst.Send(ctx, &out); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		b.st.forwarded.Add(1)
		return nil
	}
	send := func(f *canbus.Frame) error { return sendCtx(ctx, f) }
	var f canbus.Frame
	for {
		if err := src.Receive(ctx, &f); err != nil {
			if ctx.Err() != nil || errors.Is(err, canbus.ErrClosed) {
				if held != nil { // flush the reordered frame with a fresh deadline
					fctx, cancel := context.WithTimeout(context.Background(), time.Second)
					_ = sendCtx(fctx, held)
					cancel()
				}
				return nil
			}
			return err
		}
		if f.IsError() || f.Echo {
			continue
		}
		b.st.received.Add(1)
		if b.cfg.Filter != nil && !b.cfg.Filter.Match(&f) {
			if err := send(&f); err != nil {
				return err
			}
			continue
		}
		drop, dup, delay, reorder, corrupt, bit := b.draw()
		if drop {
			b.st.dropped.Add(1)
			continue
		}
		if corrupt && !f.IsRemote() && f.Len > 0 {
			bit %= int(f.Len) * 8
			f.Data[bit/8] ^= 1 << (bit % 8)
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
			c := f
			held = &c
			continue
		}
		if err := send(&f); err != nil {
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
			if err := send(&f); err != nil {
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
