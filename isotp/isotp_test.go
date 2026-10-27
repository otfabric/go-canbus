// SPDX-License-Identifier: MIT

package isotp

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leak"
	"github.com/otfabric/go-canbus/virtual"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// pair returns two connected endpoints: a sends on 0x7E0 and receives on
// 0x7E8, b the reverse. mod adjusts both configs (b's addresses are
// swapped automatically).
func pair(t *testing.T, mod func(a, b *Config)) (*Conn, *Conn, *virtual.Bus) {
	t.Helper()
	vb := virtual.New(virtual.Config{})
	pa, _ := vb.Open(virtual.WithFD(), virtual.WithQueueSize(8192))
	pb, _ := vb.Open(virtual.WithFD(), virtual.WithQueueSize(8192))
	ba, _ := canbus.NewBus(pa)
	bb, _ := canbus.NewBus(pb)
	ca := Config{TxID: 0x7E0, RxID: 0x7E8, TxAddress: 0x11, RxAddress: 0x22}
	cb := Config{TxID: 0x7E8, RxID: 0x7E0, TxAddress: 0x22, RxAddress: 0x11}
	if mod != nil {
		mod(&ca, &cb)
	}
	a, err := Dial(ba, ca)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Dial(bb, cb)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close(); ba.Close(); bb.Close(); vb.Close() })
	return a, b, vb
}

func payload(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i % 251)
	}
	return p
}

func exchange(t *testing.T, a, b *Conn, n int) {
	t.Helper()
	ctx := ctxT(t)
	want := payload(n)
	errc := make(chan error, 1)
	go func() { errc <- a.Send(ctx, want) }()
	got, err := b.Receive(ctx)
	if err != nil {
		t.Fatalf("%d bytes: receive: %v", n, err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("%d bytes: send: %v", n, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%d bytes: got %d bytes, differ", n, len(got))
	}
}

func TestSizesAndModes(t *testing.T) {
	leak.Check(t)
	sizes := []int{1, 6, 7, 8, 62, 63, 64, 100, 4095, 4096, 10000}
	modes := []struct {
		name string
		mod  func(a, b *Config)
	}{
		{"normal", nil},
		{"extended-ids", func(a, b *Config) {
			a.ExtendedIDs, b.ExtendedIDs = true, true
			a.TxID, a.RxID, b.TxID, b.RxID = 0x18DA10F1, 0x18DAF110, 0x18DAF110, 0x18DA10F1
		}},
		{"extended-addressing", func(a, b *Config) { a.Addressing, b.Addressing = Extended, Extended }},
		{"mixed-addressing", func(a, b *Config) { a.Addressing, b.Addressing = Mixed, Mixed }},
		{"padding", func(a, b *Config) { a.Padding, b.Padding = true, true }},
		{"blocks", func(a, b *Config) { b.BlockSize, b.STmin = 3, 100*time.Microsecond }},
		{"fd64", func(a, b *Config) { a.FD, b.FD, a.BRS = true, true, true }},
		{"fd12", func(a, b *Config) { a.FD, b.FD, a.TxDL, b.TxDL = true, true, 12, 12 }},
		{"fd-extended-addressing", func(a, b *Config) { a.FD, b.FD, a.Addressing, b.Addressing = true, true, Extended, Extended }},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			a, b, _ := pair(t, func(a, b *Config) {
				a.MaxMessageSize, b.MaxMessageSize = 1<<16, 1<<16
				if m.mod != nil {
					m.mod(a, b)
				}
			})
			for _, n := range sizes {
				exchange(t, a, b, n)
				exchange(t, b, a, n)
			}
		})
	}
}

// TestWireFormat checks the exact frames of a 25-byte transfer with block
// size 2 and padding on the sender only.
func TestWireFormat(t *testing.T) {
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	spy, _ := vb.Open(virtual.WithQueueSize(64))
	a, b, _ := func() (*Conn, *Conn, *virtual.Bus) {
		pa, _ := vb.Open()
		pb, _ := vb.Open()
		ba, _ := canbus.NewBus(pa)
		bb, _ := canbus.NewBus(pb)
		t.Cleanup(func() { ba.Close(); bb.Close() })
		a, _ := Dial(ba, Config{TxID: 0x7E0, RxID: 0x7E8, Padding: true})
		b, _ := Dial(bb, Config{TxID: 0x7E8, RxID: 0x7E0, BlockSize: 2, STmin: 5 * time.Millisecond})
		return a, b, vb
	}()
	defer a.Close()
	defer b.Close()
	ctx := ctxT(t)
	go func() { _ = a.Send(ctx, payload(25)) }()
	if _, err := b.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < 6; i++ {
		var f canbus.Frame
		c, cancel := context.WithTimeout(ctx, time.Second)
		err := spy.Receive(c, &f)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, f.String())
	}
	exp := []string{
		"7E0#1019000102030405",
		"7E8#300205",
		"7E0#21060708090A0B0C",
		"7E0#220D0E0F10111213",
		"7E8#300205",
		"7E0#231415161718CCCC",
	}
	for i := range exp {
		if got[i] != exp[i] {
			t.Errorf("frame %d: %s, want %s", i, got[i], exp[i])
		}
	}
}

func TestSTminIsHonoured(t *testing.T) {
	a, b, _ := pair(t, func(a, b *Config) { b.STmin = 10 * time.Millisecond })
	start := time.Now()
	exchange(t, a, b, 7*6+6) // FF + 6 CFs: 5 gaps
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Fatalf("took %v, STmin ignored", d)
	}
	forced := time.Duration(0)
	a.cfg.ForceTxSTmin = &forced
	start = time.Now()
	exchange(t, a, b, 7*6+6)
	if d := time.Since(start); d > 40*time.Millisecond {
		t.Fatalf("took %v, ForceTxSTmin ignored", d)
	}
}

func TestFlowControlWaitAndOverflow(t *testing.T) {
	a, b, _ := pair(t, func(a, b *Config) { b.SendFlowWait = 2 })
	exchange(t, a, b, 100)
	a2, b2, _ := pair(t, func(a, b *Config) { b.SendFlowWait = 3; a.MaxWait = 2 })
	ctx := ctxT(t)
	if err := a2.Send(ctx, payload(100)); !errors.Is(err, ErrWaitLimit) {
		t.Fatal(err)
	}
	a3, b3, _ := pair(t, func(a, b *Config) { a.MaxMessageSize = 5000 })
	if err := a3.Send(ctx, payload(4096)); !errors.Is(err, ErrOverflow) {
		t.Fatal(err)
	}
	if _, err := b3.Receive(ctx); !errors.Is(err, ErrTooLarge) {
		t.Fatal("receiver must report the refused message:", err)
	}
	_, _, _ = b, b2, b3
	if err := a3.Send(ctx, payload(5001)); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	if err := a3.Send(ctx, nil); !errors.Is(err, canbus.ErrInvalidOption) {
		t.Fatal(err)
	}
}

// raw returns a bus port to inject frames on a pair's bus.
func raw(t *testing.T, vb *virtual.Bus) *virtual.Port {
	p, err := vb.Open(virtual.WithFD())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func inject(t *testing.T, p *virtual.Port, s string) {
	t.Helper()
	var f canbus.Frame
	id, data, _ := bytes.Cut([]byte(s), []byte("#"))
	var v uint32
	fmt.Sscanf(string(id), "%X", &v)
	d, _ := hex.DecodeString(string(data))
	f.ID = canbus.ID(v)
	f.SetPayload(d)
	if err := p.Send(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

func TestReceptionErrors(t *testing.T) {
	_, b, vb := pair(t, func(a, b *Config) { b.TimeoutCr = 50 * time.Millisecond })
	p := raw(t, vb)
	ctx := ctxT(t)
	// N_Cr: a first frame and nothing more.
	inject(t, p, "7E0#1014000102030405")
	if _, err := b.Receive(ctx); !errors.Is(err, ErrTimeout) {
		t.Fatal("N_Cr:", err)
	}
	// Wrong sequence number.
	inject(t, p, "7E0#1014000102030405")
	inject(t, p, "7E0#2206070809")
	if _, err := b.Receive(ctx); !errors.Is(err, ErrSequence) {
		t.Fatal("sequence:", err)
	}
	// A new single frame interrupts a reception.
	inject(t, p, "7E0#1014000102030405")
	inject(t, p, "7E0#03AABBCC")
	if _, err := b.Receive(ctx); !errors.Is(err, ErrInterrupted) {
		t.Fatal(err)
	}
	if got, err := b.Receive(ctx); err != nil || hex.EncodeToString(got) != "aabbcc" {
		t.Fatal(got, err)
	}
	// Malformed and stray frames are ignored.
	for _, s := range []string{"7E0#", "7E0#08", "7E0#00", "7E0#0005", "7E0#10", "7E0#1005", "7E0#1000000000",
		"7E0#100000000FFF00", "7E0#2101", "7E0#30", "7E0#40"} {
		inject(t, p, s)
	}
	inject(t, p, "7E0#0101")
	if got, err := b.Receive(ctx); err != nil || len(got) != 1 {
		t.Fatal("stray frames not ignored:", got, err)
	}
}

func TestSequenceWrap(t *testing.T) {
	a, b, _ := pair(t, nil)
	exchange(t, a, b, 6+7*40) // 40 CFs: SN wraps twice
}

func TestFlowControlTimeoutAndClose(t *testing.T) {
	leak.Check(t)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	port, _ := vb.Open()
	bus, _ := canbus.NewBus(port)
	defer bus.Close()
	c, err := Dial(bus, Config{TxID: 1, RxID: 2, TimeoutBs: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	if err := c.Send(ctx, payload(50)); !errors.Is(err, ErrTimeout) {
		t.Fatal("N_Bs:", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := c.Receive(cctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := c.Receive(context.Background())
		errc <- err
	}()
	time.Sleep(10 * time.Millisecond)
	c.Close()
	c.Close()
	if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if err := c.Send(ctx, payload(3)); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	// The bus closing ends the connection too.
	c2, _ := Dial(bus, Config{TxID: 1, RxID: 2})
	defer c2.Close()
	bus.Close()
	if _, err := c2.Receive(ctx); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
}

func TestExtendedAddressFiltering(t *testing.T) {
	_, b, vb := pair(t, func(a, b *Config) { a.Addressing, b.Addressing = Extended, Extended })
	p := raw(t, vb)
	inject(t, p, "7E0#990141") // wrong target address
	inject(t, p, "7E0#110142")
	got, err := b.Receive(ctxT(t))
	if err != nil || len(got) != 1 || got[0] != 0x42 {
		t.Fatal(got, err)
	}
}

type countMetrics struct {
	NopMetrics
	sent, recv, errs atomic.Int32
}

func (m *countMetrics) OnMessageSent(int, time.Duration)     { m.sent.Add(1) }
func (m *countMetrics) OnMessageReceived(int, time.Duration) { m.recv.Add(1) }
func (m *countMetrics) OnError(string, error)                { m.errs.Add(1) }

func TestMetricsAndConcurrentSend(t *testing.T) {
	ma, mb := &countMetrics{}, &countMetrics{}
	a, b, _ := pair(t, func(a, b *Config) { a.Metrics, b.Metrics = ma, mb; b.QueueSize = 64 })
	ctx := ctxT(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := a.Send(ctx, payload(100+i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	seen := map[int]bool{}
	for i := 0; i < 8; i++ {
		got, err := b.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload(len(got))) {
			t.Fatal("interleaved transfers corrupted a message")
		}
		seen[len(got)] = true
	}
	wg.Wait()
	if len(seen) != 8 || ma.sent.Load() != 8 || mb.recv.Load() != 8 {
		t.Fatal(seen, ma.sent.Load(), mb.recv.Load())
	}
}

func TestConfigValidation(t *testing.T) {
	bad := []Config{
		{TxID: 0x800},
		{RxID: 0x20000000, ExtendedIDs: true},
		{Addressing: 7},
		{TxDL: 12},
		{FD: true, TxDL: 13},
		{FD: true, TxDL: 4},
		{BRS: true},
		{STmin: 1500 * time.Microsecond},
		{STmin: 200 * time.Millisecond},
		{STmin: -1},
		{ForceTxSTmin: new(time.Duration)},
	}
	*bad[len(bad)-1].ForceTxSTmin = time.Second
	if strconv.IntSize == 64 {
		big := int64(1) << 33
		bad = append(bad, Config{MaxMessageSize: int(big)})
	}
	for i, c := range bad {
		if _, err := c.withDefaults(); !errors.Is(err, canbus.ErrInvalidOption) {
			t.Errorf("config %d accepted", i)
		}
	}
	for d, b := range map[time.Duration]byte{0: 0, 5 * time.Millisecond: 5, 127 * time.Millisecond: 0x7F, 100 * time.Microsecond: 0xF1, 900 * time.Microsecond: 0xF9} {
		got, err := EncodeSTmin(d)
		if err != nil || got != b || DecodeSTmin(b) != d {
			t.Errorf("STmin %v: %x %v", d, got, err)
		}
	}
	for _, r := range []byte{0x80, 0xF0, 0xFA, 0xFF} {
		if DecodeSTmin(r) != 127*time.Millisecond {
			t.Errorf("reserved STmin %x", r)
		}
	}
	if Mixed.String() != "mixed" || Addressing(9).String() != "mixed" {
		t.Error("names")
	}
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	p, _ := vb.Open()
	bus, _ := canbus.NewBus(p)
	if _, err := Dial(bus, Config{TxID: 0x800}); err == nil {
		t.Fatal("Dial accepted a bad config")
	}
	bus.Close()
	if _, err := Dial(bus, Config{}); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
}

// FuzzReceiver feeds arbitrary frames to a receiver: it must never panic
// and only deliver messages within MaxMessageSize.
func FuzzReceiver(f *testing.F) {
	f.Add([]byte{0x10, 0x14, 0, 1, 2, 3, 4, 5}, []byte{0x21, 6, 7, 8, 9, 10, 11, 12}, []byte{0x22, 13, 14, 15, 16, 17, 18, 19})
	f.Add([]byte{0x03, 1, 2, 3}, []byte{0x30, 0, 0}, []byte{0x10, 0, 0, 0, 0x10, 0, 1, 2})
	f.Fuzz(func(t *testing.T, a, b, c []byte) {
		vb := virtual.New(virtual.Config{})
		defer vb.Close()
		port, _ := vb.Open(virtual.WithFD())
		bus, _ := canbus.NewBus(port)
		defer bus.Close()
		conn, err := Dial(bus, Config{TxID: 1, RxID: 2, MaxMessageSize: 64, FD: true, TimeoutCr: 5 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		inj, _ := vb.Open(virtual.WithFD())
		for _, d := range [][]byte{a, b, c} {
			if len(d) > 64 {
				d = d[:64]
			}
			var fr canbus.Frame
			fr.ID = 2
			fr.SetPayload(d)
			if len(d) > 8 {
				fr.Flags = canbus.FlagFD
				fr.Len = uint8(canbus.RoundUpFDLen(len(d)))
			}
			_ = inj.Send(context.Background(), &fr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		for {
			msg, err := conn.Receive(ctx)
			if errors.Is(err, context.DeadlineExceeded) {
				return
			}
			if len(msg) > 64 {
				t.Fatalf("message of %d bytes beyond MaxMessageSize", len(msg))
			}
		}
	})
}

func TestNopMetricsAndDiscard(t *testing.T) {
	var m NopMetrics
	m.OnMessageSent(1, 0)
	m.OnMessageReceived(1, 0)
	m.OnError("x", nil)
	var h discard
	_ = h.Handle(context.Background(), slog.Record{})
	_ = h.WithAttrs(nil).WithGroup("g")
}
