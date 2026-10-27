// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leak"
)

// testIface returns the vcan interface for the integration tests:
// $CANBUS_TEST_IFACE or vcan0, which must exist, be up and have the CAN FD
// MTU (ip link add vcan0 type vcan; ip link set vcan0 mtu 72 up). Without
// it the tests skip, unless $CANBUS_REQUIRE_VCAN=1, where they fail: CI
// sets it so that a missing interface is never mistaken for a pass.
func testIface(t testing.TB) string {
	t.Helper()
	name := os.Getenv("CANBUS_TEST_IFACE")
	if name == "" {
		name = "vcan0"
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil || ifi.Flags&net.FlagUp == 0 || ifi.MTU != canFDMTU {
		msg := "no usable " + name + " (needs a vcan interface, up, mtu 72)"
		if os.Getenv("CANBUS_REQUIRE_VCAN") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return name
}

func open(t testing.TB, name string, opts ...Option) *Conn {
	t.Helper()
	c, err := Open(name, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ctxT(t testing.TB, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func uniqueID(t testing.TB) canbus.ID {
	// Tests may share the interface with other processes: filter on an
	// identifier derived from the test name.
	var h uint32 = 2166136261
	for _, c := range []byte(t.Name()) {
		h = (h ^ uint32(c)) * 16777619
	}
	return canbus.ID(h & 0x1FFFFF00)
}

func onlyID(id canbus.ID, ext bool) Option {
	f := canbus.Filter{ID: id, Mask: 0x1FFFFF00, Format: canbus.FormatExtended}
	if !ext {
		f = canbus.Filter{ID: id & 0x700, Mask: 0x700, Format: canbus.FormatStandard}
	}
	return WithFilters(canbus.FilterSet{Filters: []canbus.Filter{f}})
}

func TestSendReceiveAllFormats(t *testing.T) {
	leak.Check(t)
	ifc := testIface(t)
	base := uniqueID(t)
	tx := open(t, ifc, WithFD())
	rx := open(t, ifc, WithFD(), onlyID(base, true))
	ctx := ctxT(t, 10*time.Second)
	var frames []canbus.Frame
	for l := 0; l <= 8; l++ {
		f, _ := canbus.NewExtended(base|canbus.ID(l), bytes.Repeat([]byte{byte(l)}, l))
		frames = append(frames, f)
	}
	for _, l := range []int{0, 1, 8, 12, 16, 20, 24, 32, 48, 64} {
		f, _ := canbus.NewFD(base|0x40|canbus.ID(l), true, l%2 == 0, bytes.Repeat([]byte{0xA5}, l))
		if l == 24 {
			f.Flags |= canbus.FlagESI
		}
		frames = append(frames, f)
	}
	r, _ := canbus.NewRemote(base|0xF0, true, 6)
	frames = append(frames, r, canbus.Frame{ID: base | 0xF1, Flags: canbus.FlagExtended, Len: 8, Len8DLC: 15})
	for i := range frames {
		if err := tx.Send(ctx, &frames[i]); err != nil {
			t.Fatalf("send %v: %v", frames[i], err)
		}
	}
	for _, want := range frames {
		var got canbus.Frame
		if err := rx.Receive(ctx, &got); err != nil {
			t.Fatal(err)
		}
		if got.ID != want.ID || got.Flags != want.Flags || got.Len != want.Len || got.Data != want.Data || got.Len8DLC != want.Len8DLC {
			t.Errorf("got %v (%+v), want %v", got, got.Flags, want)
		}
		if got.Interface != ifc || got.Timestamp.IsZero() || time.Since(got.Timestamp) > time.Minute || got.Echo {
			t.Errorf("metadata %q %v %v", got.Interface, got.Timestamp, got.Echo)
		}
	}
	if st := tx.Stats(); st.TxFrames != uint64(len(frames)) {
		t.Errorf("tx stats %+v", st)
	}
	if st := rx.Stats(); st.RxFrames != uint64(len(frames)) {
		t.Errorf("rx stats %+v", st)
	}
	caps := tx.Capabilities()
	if !caps.FD || !caps.KernelFilters || !caps.Physical || tx.Name() != ifc {
		t.Errorf("capabilities %+v", caps)
	}
}

func TestStandardIDs(t *testing.T) {
	ifc := testIface(t)
	tx := open(t, ifc)
	rx := open(t, ifc, WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(0x7FF), canbus.ExactStandard(0)}}))
	ctx := ctxT(t, 5*time.Second)
	for _, id := range []canbus.ID{0, 0x7FE, 0x7FF} {
		f, _ := canbus.NewStandard(id, []byte{1})
		if err := tx.Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []canbus.ID{0, 0x7FF} {
		var g canbus.Frame
		if err := rx.Receive(ctx, &g); err != nil || g.ID != want || g.IsExtended() {
			t.Fatal(g, err)
		}
	}
}

func TestClassicalSocketRejectsFD(t *testing.T) {
	ifc := testIface(t)
	c := open(t, ifc)
	f, _ := canbus.NewFD(1, false, false, []byte{1})
	if err := c.Send(ctxT(t, time.Second), &f); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal(err)
	}
	// A classical socket does not receive FD frames.
	base := uniqueID(t)
	rx := open(t, ifc, onlyID(base, true))
	fdtx := open(t, ifc, WithFD())
	fd, _ := canbus.NewFD(base, true, false, make([]byte, 12))
	cl, _ := canbus.NewExtended(base|1, []byte{1})
	ctx := ctxT(t, 5*time.Second)
	_ = fdtx.Send(ctx, &fd)
	_ = fdtx.Send(ctx, &cl)
	var g canbus.Frame
	if err := rx.Receive(ctx, &g); err != nil || g.ID != base|1 {
		t.Fatal("classical socket got", g, err)
	}
}

func TestLoopbackAndRecvOwn(t *testing.T) {
	ifc := testIface(t)
	base := uniqueID(t)
	own := open(t, ifc, WithRecvOwn(), onlyID(base, true))
	quiet := open(t, ifc, WithoutLoopback(), onlyID(base, true))
	other := open(t, ifc, onlyID(base, true))
	ctx := ctxT(t, 5*time.Second)
	f, _ := canbus.NewExtended(base, []byte{1})
	if err := own.Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	var g canbus.Frame
	if err := own.Receive(ctx, &g); err != nil || !g.Echo {
		t.Fatal("own echo", g, err)
	}
	if err := other.Receive(ctx, &g); err != nil || g.Echo {
		t.Fatal("other", err)
	}
	f.ID = base | 1
	if err := quiet.Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	short := ctxT(t, 100*time.Millisecond)
	if err := other.Receive(short, &g); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("frame of a socket without loopback was received:", g, err)
	}
}

func TestKernelFilterSemantics(t *testing.T) {
	ifc := testIface(t)
	base := uniqueID(t)
	tx := open(t, ifc)
	inv := open(t, ifc, WithFilters(canbus.FilterSet{Join: true, Filters: []canbus.Filter{
		{ID: base, Mask: 0x1FFFFF00, Format: canbus.FormatExtended},
		{ID: base | 1, Mask: 0x1FFFFFFF, Format: canbus.FormatExtended, Invert: true},
	}}))
	rtr := open(t, ifc, WithFilters(canbus.FilterSet{Filters: []canbus.Filter{
		{ID: base, Mask: 0x1FFFFF00, Format: canbus.FormatExtended, Remote: canbus.RemoteOnly},
	}}))
	ctx := ctxT(t, 5*time.Second)
	var sent []canbus.Frame
	for i := canbus.ID(0); i < 4; i++ {
		f, _ := canbus.NewExtended(base|i, nil)
		sent = append(sent, f)
	}
	r, _ := canbus.NewRemote(base|9, true, 1)
	sent = append(sent, r)
	for i := range sent {
		if err := tx.Send(ctx, &sent[i]); err != nil {
			t.Fatal(err)
		}
	}
	collect := func(c *Conn) []canbus.ID {
		var ids []canbus.ID
		for {
			var g canbus.Frame
			if err := c.Receive(ctxT(t, 100*time.Millisecond), &g); err != nil {
				return ids
			}
			ids = append(ids, g.ID-base)
		}
	}
	got := collect(inv)
	if len(got) != 4 || got[0] != 0 || got[1] != 2 || got[2] != 3 || got[3] != 9 {
		t.Errorf("joined/inverted: %v", got)
	}
	// The software matcher must agree with the kernel on the same frames.
	fs := canbus.FilterSet{Join: true, Filters: []canbus.Filter{
		{ID: base, Mask: 0x1FFFFF00, Format: canbus.FormatExtended},
		{ID: base | 1, Mask: 0x1FFFFFFF, Format: canbus.FormatExtended, Invert: true},
	}}
	var soft []canbus.ID
	for i := range sent {
		if fs.Match(&sent[i]) {
			soft = append(soft, sent[i].ID-base)
		}
	}
	if len(soft) != len(got) {
		t.Errorf("software %v, kernel %v", soft, got)
	}
	if got := collect(rtr); len(got) != 1 || got[0] != 9 {
		t.Errorf("remote only: %v", got)
	}
	if err := rtr.SetFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactExtended(base | 2)}}); err != nil {
		t.Fatal(err)
	}
	_ = tx.Send(ctx, &sent[2])
	if got := collect(rtr); len(got) != 1 || got[0] != 2 {
		t.Errorf("after SetFilters: %v", got)
	}
}

func TestCancelAndClose(t *testing.T) {
	leak.Check(t)
	ifc := testIface(t)
	c := open(t, ifc, WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactExtended(uniqueID(t))}}))
	var f canbus.Frame
	start := time.Now()
	if err := c.Receive(ctxT(t, 50*time.Millisecond), &f); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("deadline not honoured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- c.Receive(ctx, &canbus.Frame{}) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// The socket stays usable after a cancelled Receive.
	if err := c.Receive(ctxT(t, 20*time.Millisecond), &f); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	go func() { errc <- c.Receive(context.Background(), &canbus.Frame{}) }()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	g, _ := canbus.NewStandard(1, nil)
	if err := c.Send(context.Background(), &g); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if err := c.Receive(context.Background(), &f); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
	if err := c.SetFilters(canbus.FilterSet{}); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
}

func TestOverflowAccounting(t *testing.T) {
	ifc := testIface(t)
	base := uniqueID(t)
	tx := open(t, ifc, WithSendBuffer(1<<20))
	rx := open(t, ifc, WithReceiveBuffer(1024), onlyID(base, true))
	ctx := ctxT(t, 20*time.Second)
	const n = 5000
	for i := 0; i < n; i++ {
		var d [4]byte
		binary.BigEndian.PutUint32(d[:], uint32(i))
		f, _ := canbus.NewExtended(base, d[:])
		if err := tx.Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	got := 0
	last := -1
	for {
		var f canbus.Frame
		if err := rx.Receive(ctxT(t, 200*time.Millisecond), &f); err != nil {
			break
		}
		seq := int(binary.BigEndian.Uint32(f.Data[:4]))
		if seq <= last {
			t.Fatalf("reordered: %d after %d", seq, last)
		}
		last = seq
		got++
	}
	// The kernel reports drops with the next frame it queues.
	marker, _ := canbus.NewExtended(base|1, nil)
	if err := tx.Send(ctx, &marker); err != nil {
		t.Fatal(err)
	}
	var m canbus.Frame
	if err := rx.Receive(ctx, &m); err != nil || m.ID != base|1 {
		t.Fatal("marker", m, err)
	}
	drops, reason := rx.Drops()
	t.Logf("received %d of %d, kernel reported %d dropped", got, n, drops)
	if reason != canbus.DropKernelOverflow || drops == 0 || uint64(got)+drops != n {
		t.Fatalf("received %d + dropped %d != sent %d", got, drops, n)
	}
}

func TestBusOverSocketCAN(t *testing.T) {
	leak.Check(t)
	ifc := testIface(t)
	base := uniqueID(t)
	c := open(t, ifc, WithFD(), onlyID(base, true))
	b, err := canbus.NewBus(c)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s, _ := b.Subscribe()
	tx := open(t, ifc, WithFD())
	ctx := ctxT(t, 5*time.Second)
	f, _ := canbus.NewFD(base, true, true, make([]byte, 48))
	if err := tx.Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	g, err := s.Receive(ctx)
	if err != nil || g.Len != 48 || g.Flags&canbus.FlagBRS == 0 {
		t.Fatal(g, err)
	}
	if b.Name() != ifc {
		t.Fatal(b.Name())
	}
}

func TestAnyInterface(t *testing.T) {
	ifc := testIface(t)
	base := uniqueID(t)
	all := open(t, "any", onlyID(base, true))
	if all.Name() != "any" {
		t.Fatal(all.Name())
	}
	f, _ := canbus.NewExtended(base, nil)
	if err := all.Send(context.Background(), &f); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal("send on any:", err)
	}
	tx := open(t, ifc)
	ctx := ctxT(t, 5*time.Second)
	_ = tx.Send(ctx, &f)
	var g canbus.Frame
	if err := all.Receive(ctx, &g); err != nil || g.Interface != ifc {
		t.Fatal(g, err)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open("nosuchcan0"); !errors.Is(err, canbus.ErrInterfaceDown) {
		t.Fatal(err)
	}
	if _, err := Open("lo", WithFD()); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal("FD on lo:", err)
	}
	if !Supported() {
		t.Fatal("Supported")
	}
}

func BenchmarkSendReceive(b *testing.B) {
	ifc := testIface(b)
	base := uniqueID(b)
	tx := open(b, ifc)
	rx := open(b, ifc, onlyID(base, true), WithReceiveBuffer(1<<20))
	f, _ := canbus.NewExtended(base, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	ctx := context.Background()
	var g canbus.Frame
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := tx.Send(ctx, &f); err != nil {
			b.Fatal(err)
		}
		if err := rx.Receive(ctx, &g); err != nil {
			b.Fatal(err)
		}
	}
}
