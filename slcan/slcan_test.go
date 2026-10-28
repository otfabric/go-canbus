// SPDX-License-Identifier: MIT

package slcan

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leak"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// peer is the device side of a pipe: it reads the records a Conn writes.
type peer struct {
	conn net.Conn
	r    *bufio.Reader
}

func newPeer(t *testing.T, cfg Config) (*Conn, *peer) {
	t.Helper()
	a, b := net.Pipe()
	p := &peer{conn: b, r: bufio.NewReader(b)}
	type res struct {
		c   *Conn
		err error
	}
	done := make(chan res, 1)
	go func() { c, err := New(a, cfg); done <- res{c, err} }()
	// net.Pipe is synchronous: the setup commands are read below.
	var cmds []string
	if !cfg.NoCommands {
		for {
			s := p.record(t)
			cmds = append(cmds, s)
			if s == "O" || s == "L" {
				break
			}
		}
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() {
		go func() { _, _ = io.Copy(io.Discard, b) }() // take the closing "C"
		_ = r.c.Close()
		_ = b.Close()
	})
	_ = cmds
	return r.c, p
}

func (p *peer) record(t *testing.T) string {
	t.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	s, err := p.r.ReadString('\r')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(s, "\r")
}

func (p *peer) write(t *testing.T, s string) {
	t.Helper()
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := p.conn.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

func TestSetupCommands(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{}, "C O"},
		{Config{Bitrate: 500000}, "C S6 O"},
		{Config{Bitrate: 1000000, DataBitrate: 2000000}, "C S8 Y2 O"},
		{Config{Bitrate: 125000, ListenOnly: true}, "C S4 L"},
	} {
		a, b := net.Pipe()
		got := make(chan string, 1)
		go func() {
			r := bufio.NewReader(b)
			var cmds []string
			for {
				s, err := r.ReadString('\r')
				if err != nil {
					break
				}
				s = strings.TrimSuffix(s, "\r")
				cmds = append(cmds, s)
				if s == "O" || s == "L" {
					got <- strings.Join(cmds, " ")
					cmds = nil
				}
			}
		}()
		conn, err := New(a, c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if g := <-got; g != c.want {
			t.Errorf("%+v: %q, want %q", c.cfg, g, c.want)
		}
		_ = conn.Close()
		_ = b.Close()
	}
}

func TestBadConfig(t *testing.T) {
	for _, cfg := range []Config{{Bitrate: 42}, {DataBitrate: 4000000}} {
		a, b := net.Pipe()
		if _, err := New(a, cfg); !errors.Is(err, canbus.ErrInvalidOption) {
			t.Errorf("%+v: %v", cfg, err)
		}
		_ = a.Close()
		_ = b.Close()
	}
	// A device that refuses the setup.
	a, b := net.Pipe()
	_ = b.Close()
	if _, err := New(a, Config{}); err == nil {
		t.Error("setup on a closed pipe succeeded")
	}
}

func TestSendReceive(t *testing.T) {
	leak.Check(t)
	ctx := ctxT(t)
	c, p := newPeer(t, Config{Bitrate: 500000, Name: "can-usb"})
	f, _ := canbus.NewStandard(0x123, []byte{0xA1, 0xB2})
	sent := make(chan error, 1)
	go func() { sent <- c.Send(ctx, &f) }()
	if s := p.record(t); s != "t1232A1B2" {
		t.Fatalf("sent %q", s)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	// Acks, replies, garbage and a frame with a timestamp.
	p.write(t, "\r\az\rZ\rV1013\rt12\rT18DAF1103021003EA60\r\nr4563\a")
	var g canbus.Frame
	for _, want := range []string{"18DAF110#021003", "456#R3"} {
		if err := c.Receive(ctx, &g); err != nil || g.String() != want || g.Interface != "can-usb" || g.Timestamp.IsZero() {
			t.Fatalf("%v %v, want %s", g, err, want)
		}
	}
	st := c.Stats()
	if st.TxFrames != 1 || st.TxBytes != 2 || st.RxFrames != 2 || st.RxBytes != 3 || st.RxFailure != 1 {
		t.Fatalf("stats %+v", st)
	}
	if c.Name() != "can-usb" || !c.Capabilities().FD {
		t.Fatal(c.Name(), c.Capabilities())
	}
	ef := canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassBusOff})
	if err := c.Send(ctx, &ef); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.Send(cctx, &f); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.Receive(cctx, &g); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLongGarbageAndDrops(t *testing.T) {
	ctx := ctxT(t)
	c, p := newPeer(t, Config{NoCommands: true, QueueSize: 2})
	p.write(t, strings.Repeat("x", 600)+"\r")
	p.write(t, "t0010\rt0020\rt0030\r")
	for c.Stats().RxFrames < 3 {
		time.Sleep(time.Millisecond)
	}
	var g canbus.Frame
	if err := c.Receive(ctx, &g); err != nil || g.ID != 1 {
		t.Fatal(g, err)
	}
	if err := c.Receive(ctx, &g); err != nil || g.ID != 2 {
		t.Fatal(g, err)
	}
	if n, reason := c.Drops(); n != 1 || reason != canbus.DropQueueFull {
		t.Fatal(n, reason)
	}
	if c.Stats().RxFailure == 0 {
		t.Fatal("garbage not counted")
	}
}

func TestListenOnly(t *testing.T) {
	c, _ := newPeer(t, Config{ListenOnly: true})
	f, _ := canbus.NewStandard(1, nil)
	if err := c.Send(ctxT(t), &f); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestCloseAndPeerGone(t *testing.T) {
	leak.Check(t)
	ctx := ctxT(t)
	c, p := newPeer(t, Config{NoCommands: true})
	p.write(t, "t0010\r")
	_ = p.conn.Close() // the device disappears
	var g canbus.Frame
	if err := c.Receive(ctx, &g); err != nil || g.ID != 1 {
		t.Fatal("frames before the loss are delivered:", g, err)
	}
	if err := c.Receive(ctx, &g); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
	f, _ := canbus.NewStandard(1, nil)
	if err := c.Send(ctx, &f); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
	if c.Stats().TxErrors != 1 {
		t.Fatal(c.Stats())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	if err := c.Receive(ctx, &g); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if err := c.Send(ctx, &f); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
}

func TestCloseUnblocksReceive(t *testing.T) {
	leak.Check(t)
	c, p := newPeer(t, Config{})
	errc := make(chan error, 1)
	go func() { errc <- c.Receive(context.Background(), &canbus.Frame{}) }()
	time.Sleep(20 * time.Millisecond)
	go func() { _, _ = io.Copy(io.Discard, p.conn) }()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
}

// TestBusOverSlcan: two Conns joined back to back carry frames through
// a Bus.
func TestBusOverSlcan(t *testing.T) {
	leak.Check(t)
	ctx := ctxT(t)
	a, b := net.Pipe()
	ca, err := New(a, Config{NoCommands: true, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := New(b, Config{NoCommands: true, Name: "b"})
	bus, _ := canbus.NewBus(cb, canbus.WithStartOnSubscribe())
	defer bus.Close()
	defer ca.Close()
	sub, _ := bus.Subscribe()
	var sent []canbus.Frame
	for i := 0; i < 100; i++ {
		f, err := canbus.NewFD(canbus.ID(i), i%2 == 0, i%3 == 0, make([]byte, i%65/4*4%49))
		if err != nil {
			continue // not a CAN FD length
		}
		sent = append(sent, f)
	}
	go func() {
		for i := range sent {
			_ = ca.Send(ctx, &sent[i])
		}
	}()
	for i := range sent {
		g, err := sub.Receive(ctx)
		if err != nil || g.String() != sent[i].String() || g.Flags != sent[i].Flags || g.Interface != "b" {
			t.Fatalf("frame %d: %v %v, want %v", i, g, err, sent[i])
		}
	}
}

// zeroReader reads nothing, without error, like a hung-up tty.
type zeroReader struct{ net.Conn }

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

func TestHungUp(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c, err := New(zeroReader{a}, Config{NoCommands: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Receive(ctxT(t), &canbus.Frame{}); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
}

// failCloser fails to close.
type failCloser struct{ net.Conn }

func (f failCloser) Close() error { _ = f.Conn.Close(); return errors.New("close failed") }

func TestCloseError(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c, _ := New(failCloser{a}, Config{NoCommands: true})
	if err := c.Close(); err == nil || err.Error() != "close failed" {
		t.Fatal(err)
	}
}
