// SPDX-License-Identifier: MIT

package chaos_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canbus/internal/leak"
	"github.com/otfabric/go-canbus/isotp"
	"github.com/otfabric/go-canbus/virtual"
)

// iterations is the number of seeds each monkey test runs:
// CANBUS_CHAOS_ITERATIONS overrides it (the nightly job runs many more).
func iterations(def int) int {
	if n, err := strconv.Atoi(os.Getenv("CANBUS_CHAOS_ITERATIONS")); err == nil && n > 0 {
		return n
	}
	return def
}

// randomFaults returns a fault mix for a seed. With physical false it
// leaves out what a single CAN bus cannot do to a sender's frames and
// ISO-TP cannot detect: corruption (a real bus rejects it by CRC; ISO-TP
// has no checksum) and reordering (a bus keeps each sender's order; a
// consecutive frame carries no message identity, so a stale one with the
// expected sequence number is indistinguishable — found by this suite).
func randomFaults(r *rand.Rand, physical bool) chaos.Config {
	c := chaos.Config{
		Seed:      r.Int63(),
		Drop:      r.Float64() * 0.05,
		Duplicate: r.Float64() * 0.05,
		Delay:     r.Float64() * 0.05,
		DelayBy:   time.Duration(r.Intn(3)+1) * time.Millisecond,
	}
	if !physical {
		c.Reorder = r.Float64() * 0.03
		c.Corrupt = r.Float64() * 0.05
	}
	return c
}

type link struct {
	a, b      *canbus.Bus
	bridge    *chaos.Bridge
	stop      func()
	setFaults func(chaos.Config)
	cfg       chaos.Config
	mu        sync.Mutex
}

// newLink joins two virtual segments with a bridge whose faults can be
// replaced (a new bridge is started).
func newLink(t *testing.T, cfg chaos.Config) *link {
	t.Helper()
	x := virtual.New(virtual.Config{Name: "x"})
	y := virtual.New(virtual.Config{Name: "y"})
	xb, _ := x.Open(virtual.WithFD(), virtual.WithQueueSize(16384))
	yb, _ := y.Open(virtual.WithFD(), virtual.WithQueueSize(16384))
	xn, _ := x.Open(virtual.WithFD(), virtual.WithQueueSize(16384))
	yn, _ := y.Open(virtual.WithFD(), virtual.WithQueueSize(16384))
	a, _ := canbus.NewBus(xn)
	b, _ := canbus.NewBus(yn)
	l := &link{a: a, b: b}
	var cancel context.CancelFunc
	var done chan struct{}
	start := func(c chaos.Config) {
		br, err := chaos.New(c)
		if err != nil {
			t.Fatal(err)
		}
		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())
		done = make(chan struct{})
		l.bridge = br
		go func() { _ = br.Both(ctx, xb, yb); close(done) }()
	}
	start(cfg)
	l.setFaults = func(c chaos.Config) { cancel(); <-done; start(c) }
	l.stop = func() {
		cancel()
		<-done
		a.Close()
		b.Close()
		x.Close()
		y.Close()
	}
	t.Cleanup(l.stop)
	return l
}

// TestMonkeyISOTP transfers messages through random faults. Invariants:
// every call returns within its deadline; a received message is always
// one that was sent, byte for byte; once the faults stop, transfers
// succeed again.
func TestMonkeyISOTP(t *testing.T) {
	leak.Check(t)
	seeds := iterations(12)
	for seed := 0; seed < seeds; seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		cfg := randomFaults(r, true)
		l := newLink(t, cfg)
		mode := isotp.Config{BlockSize: uint8(r.Intn(4)), TimeoutBs: 150 * time.Millisecond, TimeoutCr: 150 * time.Millisecond, MaxMessageSize: 4095}
		if r.Intn(2) == 0 {
			mode.FD = true
		}
		ca, cb := mode, mode
		ca.TxID, ca.RxID, cb.TxID, cb.RxID = 0x700, 0x708, 0x708, 0x700
		a, err := isotp.Dial(l.a, ca)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := isotp.Dial(l.b, cb)
		sent := map[string]bool{}
		var ok, failed int
		for i := 0; i < 8; i++ {
			msg := make([]byte, 1+r.Intn(800))
			r.Read(msg)
			sent[string(msg)] = true
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := a.Send(ctx, msg); err != nil {
				if ctx.Err() != nil {
					t.Fatalf("seed %d: send hung until the deadline", seed)
				}
				failed++
			} else {
				ok++
			}
			cancel()
		}
		// Drain what arrived: every message must be one that was sent.
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			got, err := b.Receive(ctx)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
			if err == nil && !sent[string(got)] {
				for m := range sent {
					if len(m) == len(got) {
						for i := 0; i < len(m); i++ {
							if m[i] != got[i] {
								t.Logf("same length as a sent message; first difference at byte %d (FF carries %d bytes, CF %d)", i, map[bool]int{true: 62 - 2, false: 6}[mode.FD], map[bool]int{true: 63, false: 7}[mode.FD])
								break
							}
						}
					}
				}
				t.Fatalf("seed %d (%+v, fd=%v bs=%d): received %d bytes that were never sent", seed, cfg, mode.FD, mode.BlockSize, len(got))
			}
		}
		// Recovery.
		l.setFaults(chaos.Config{})
		msg := []byte("recovered after chaos")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := a.Send(ctx, msg); err != nil {
			t.Fatalf("seed %d: no recovery: %v", seed, err)
		}
		for {
			got, err := b.Receive(ctx)
			if ctx.Err() != nil {
				t.Fatalf("seed %d: no recovery: %v", seed, err)
			}
			// Errors of receptions the faults broke may still be queued.
			if err == nil && bytes.Equal(got, msg) {
				break
			}
		}
		cancel()
		t.Logf("seed %d %+v: %d sent, %d failed, bridge %+v", seed, cfg, ok, failed, l.bridge.Stats())
		a.Close()
		b.Close()
		l.stop()
	}
}

// TestMonkeyCorruption sends through every fault including corruption and
// reordering. Invariant: nothing panics or hangs, every call returns.
func TestMonkeyCorruption(t *testing.T) {
	leak.Check(t)
	for seed := 0; seed < iterations(6); seed++ {
		r := rand.New(rand.NewSource(int64(1000 + seed)))
		cfg := randomFaults(r, false)
		cfg.Corrupt = 0.2
		l := newLink(t, cfg)
		a, _ := isotp.Dial(l.a, isotp.Config{TxID: 1, RxID: 2, TimeoutBs: 100 * time.Millisecond, TimeoutCr: 100 * time.Millisecond, MaxMessageSize: 4095})
		b, _ := isotp.Dial(l.b, isotp.Config{TxID: 2, RxID: 1, TimeoutBs: 100 * time.Millisecond, TimeoutCr: 100 * time.Millisecond, MaxMessageSize: 4095})
		for i := 0; i < 6; i++ {
			// A corrupted flow control may ask for STmin 127 ms or WAIT:
			// the call may run into its deadline, but must return then.
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			start := time.Now()
			_ = a.Send(ctx, make([]byte, 50+r.Intn(500)))
			if d := time.Since(start); d > 800*time.Millisecond {
				t.Fatalf("seed %d: send returned %v after its deadline", seed, d-500*time.Millisecond)
			}
			cancel()
		}
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err := b.Receive(ctx)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
		}
		a.Close()
		b.Close()
		l.stop()
	}
}

// TestMonkeyBus throws random operations at a Bus while frames flow:
// subscriptions come and go, the interface goes down and up, error frames
// arrive, senders run concurrently, and the bus is closed at a random
// moment. Invariants: no deadlock, no goroutine leak, every subscription
// sees each sender's frames in order (duplicates aside: none here).
func TestMonkeyBus(t *testing.T) {
	leak.Check(t)
	for seed := 0; seed < iterations(10); seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		vb := virtual.New(virtual.Config{Seed: int64(seed) + 1})
		vb.SetFaults(virtual.Faults{Drop: r.Float64() * 0.1, Delay: r.Float64() * 0.05, DelayBy: time.Millisecond})
		rx, _ := vb.Open(virtual.WithQueueSize(256), virtual.WithFilters(canbus.FilterSet{ErrorMask: canbus.ErrClassAll}))
		bus, _ := canbus.NewBus(rx)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for s := 0; s < 3; s++ {
			port, _ := vb.Open()
			wg.Add(1)
			go func(id canbus.ID, p *virtual.Port) {
				defer wg.Done()
				for i := uint32(0); ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					var d [4]byte
					binary.BigEndian.PutUint32(d[:], i)
					f, _ := canbus.NewStandard(id, d[:])
					if err := p.Send(ctx, &f); errors.Is(err, canbus.ErrClosed) || ctx.Err() != nil {
						return
					}
				}
			}(canbus.ID(0x10+s), port)
		}
		// Ordered consumers.
		for c := 0; c < 2; c++ {
			sub, err := bus.Subscribe(canbus.WithQueueSize(64), canbus.WithOverflow(canbus.Overflow(r.Intn(2))))
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				last := map[canbus.ID]int64{}
				for {
					f, err := sub.Receive(ctx)
					if err != nil {
						return
					}
					if f.IsError() || f.ID == 0x7FF { // error frames and the injected raw frames
						continue
					}
					seq := int64(binary.BigEndian.Uint32(f.Data[:4]))
					if prev, ok := last[f.ID]; ok && seq <= prev {
						t.Errorf("seed %d: sender %X went from %d to %d", seed, f.ID, prev, seq)
						return
					}
					last[f.ID] = seq
				}
			}()
		}
		// The monkey.
		for step := 0; step < 60; step++ {
			switch r.Intn(5) {
			case 0:
				if s, err := bus.Subscribe(canbus.WithQueueSize(1 + r.Intn(8))); err == nil {
					time.AfterFunc(time.Duration(r.Intn(5))*time.Millisecond, func() { s.Close() })
				}
			case 1:
				vb.SetUp(false)
				time.Sleep(time.Duration(r.Intn(3)) * time.Millisecond)
				vb.SetUp(true)
			case 2:
				_ = vb.InjectError(ctx, canbus.ErrorInfo{Class: canbus.ErrClassController, Controller: canbus.CtrlRxWarning})
			case 3:
				_ = vb.InjectRaw(ctx, canbus.Frame{ID: 0x7FF, Len: 9}) // invalid on purpose
			default:
				time.Sleep(time.Millisecond)
			}
		}
		if r.Intn(2) == 0 {
			bus.Close() // close while traffic flows
		}
		close(stop)
		bus.Close()
		vb.Close()
		wg.Wait()
		cancel()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("seed %d: deadlock", seed)
		}
	}
}

// TestMonkeyReplay replays a trace through a faulty bridge and cancels
// at a random moment: Replay must return promptly with the context error
// or nil, never hang, and never send more than it read.
func TestMonkeyReplay(t *testing.T) {
	leak.Check(t)
	var trace bytes.Buffer
	w := capture.NewJSONLWriter(&trace)
	base := time.Unix(1700000000, 0)
	for i := 0; i < 400; i++ {
		f, _ := canbus.NewStandard(canbus.ID(i%16), []byte{byte(i)})
		f.Timestamp = base.Add(time.Duration(i) * 200 * time.Microsecond)
		_ = w.Write(&f)
	}
	_ = w.Close()
	for seed := 0; seed < iterations(8); seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		l := newLink(t, randomFaults(r, false))
		after := time.Duration(r.Intn(60)) * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), after)
		start := time.Now()
		st, err := capture.Replay(ctx, capture.NewJSONLReader(bytes.NewReader(trace.Bytes())), l.a, capture.ReplayOptions{Speed: float64(1 + r.Intn(4))})
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if time.Since(start) > after+500*time.Millisecond || st.Sent > st.Read {
			t.Fatalf("seed %d: %+v after %v", seed, st, time.Since(start))
		}
		l.stop()
	}
}
