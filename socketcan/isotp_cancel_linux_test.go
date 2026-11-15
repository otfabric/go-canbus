// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/leakcheck"
	"github.com/otfabric/go-canbus/isotp"
)

// Flow controls a stalling peer sends (ISO 15765-2 flow status in the
// low nibble of 0x3X): WAIT, CTS, and CTS with block size 1 and STmin
// 127 ms.
var (
	fcWait      = []byte{0x31, 0x00, 0x00}
	fcCTS       = []byte{0x30, 0x00, 0x00}
	fcSlowBlock = []byte{0x30, 0x01, 0x7F}
)

// stallPeer is a CAN_RAW peer that keeps a kernel ISO-TP sender's
// transfer going without letting it finish: it answers the first frame,
// and then every consecutive frame, with a flow control just before the
// kernel's N_Bs (1 s) would expire, or it sends WAIT flow controls on a
// timer.
type stallPeer struct {
	t      *testing.T
	sock   *Conn
	tx, rx canbus.ID // the peer sends tx and listens to rx
	ff     chan struct{}
	cfs    atomic.Int64 // consecutive frames seen
	stop   chan struct{}
	done   chan struct{}
}

// stallPeer answers a frame this long after it.
const stallAnswerDelay = 900 * time.Millisecond

// startStallPeer starts the peer. With every > 0 it sends fc every
// interval after the first frame; with every == 0 it answers the first
// frame and each consecutive frame with fc after stallAnswerDelay.
func startStallPeer(t *testing.T, ifc string, rx, tx canbus.ID, fc []byte, every time.Duration) *stallPeer {
	t.Helper()
	sock, err := Open(ifc)
	if err != nil {
		t.Fatal(err)
	}
	p := &stallPeer{t: t, sock: sock, tx: tx, rx: rx, ff: make(chan struct{}),
		stop: make(chan struct{}), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(p.done)
		var wg sync.WaitGroup
		defer wg.Wait()
		var f canbus.Frame
		for p.sock.Receive(ctx, &f) == nil {
			if f.ID != rx || f.IsError() || len(f.Payload()) == 0 {
				continue
			}
			switch f.Data[0] >> 4 {
			case 1: // first frame
				close(p.ff)
				if every > 0 {
					wg.Go(func() { p.repeat(ctx, fc, every) })
					continue
				}
				wg.Go(func() { p.answer(ctx, fc) })
			case 2: // consecutive frame
				p.cfs.Add(1)
				if every == 0 {
					wg.Go(func() { p.answer(ctx, fc) })
				}
			default:
			}
		}
	}()
	t.Cleanup(func() {
		p.halt()
		cancel()
		<-p.done
		sock.Close()
	})
	return p
}

func (p *stallPeer) send(ctx context.Context, data []byte) {
	f, err := canbus.NewStandard(p.tx, data)
	if err == nil {
		err = p.sock.Send(ctx, &f)
	}
	if err != nil && ctx.Err() == nil {
		p.t.Errorf("peer: %v", err)
	}
}

func (p *stallPeer) repeat(ctx context.Context, fc []byte, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			p.send(ctx, fc)
		}
	}
}

func (p *stallPeer) answer(ctx context.Context, fc []byte) {
	t := time.NewTimer(stallAnswerDelay)
	defer t.Stop()
	select {
	case <-p.stop:
	case <-ctx.Done():
	case <-t.C:
		p.send(ctx, fc)
	}
}

// halt stops the stalling flow controls.
func (p *stallPeer) halt() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

func (p *stallPeer) waitFirstFrame(t *testing.T) {
	t.Helper()
	select {
	case <-p.ff:
	case <-time.After(5 * time.Second):
		t.Fatal("peer: no first frame")
	}
}

// ended checks that the transfer is over: the peer stops stalling and
// sends a CTS flow control without limits, and no consecutive frame
// follows.
func (p *stallPeer) ended(t *testing.T) {
	t.Helper()
	p.halt()
	time.Sleep(20 * time.Millisecond)
	before := p.cfs.Load()
	p.send(context.Background(), fcCTS)
	time.Sleep(500 * time.Millisecond)
	if n := p.cfs.Load() - before; n != 0 {
		t.Fatalf("%d consecutive frames after the transfer should have ended", n)
	}
}

// needPollTracksTx skips a test of Send's cancellation on a kernel whose
// ISO-TP poll does not report a transfer in progress: there Send waits
// inside the write, as LIMITATIONS.md says.
func needPollTracksTx(t *testing.T, k *ISOTPConn) {
	t.Helper()
	if k.waitTxDone {
		t.Skip("this kernel's ISO-TP poll does not report a transfer in progress (isotp_poll, Linux 6.3 and the stable series after it): Send cannot be cancelled here")
	}
}

// TestKernelISOTPSendDeadlineWaitStall: a peer that answers the first
// frame with a WAIT flow control every 800 ms, below the kernel's 1 s
// N_Bs, keeps the kernel's transfer open for as long as it likes. Send
// ends at its context's deadline, closes the connection and stops the
// transfer; Close afterwards returns at once.
func TestKernelISOTPSendDeadlineWaitStall(t *testing.T) {
	ifc := testIface(t)
	leakcheck.Check(t)
	k := kernelISOTP(t, ifc, isotp.Config{TxID: 0x6D0, RxID: 0x6D8})
	needPollTracksTx(t, k)
	p := startStallPeer(t, ifc, 0x6D0, 0x6D8, fcWait, 800*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := k.Send(ctx, pattern(4095))
	took := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send: %v after %v, want context.DeadlineExceeded", err, took)
	}
	if took < 2*time.Second || took > 3*time.Second {
		t.Fatalf("Send returned after %v, want about 2s", took)
	}
	start = time.Now()
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	closeTook := time.Since(start)
	t.Logf("Send returned %v after %v; Close returned after %v", err, took, closeTook)
	if closeTook > 200*time.Millisecond {
		t.Fatalf("Close took %v", closeTook)
	}
	if err := k.Send(context.Background(), pattern(1)); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("Send after the abort: %v", err)
	}
	p.ended(t)
}

// TestKernelISOTPSendCancelSlowBlocks: a peer that asks for blocks of
// one consecutive frame and answers each 900 ms later, below N_Bs, would
// stretch a 4095-byte transfer to about 10 minutes. Cancelling Send's
// context ends it.
func TestKernelISOTPSendCancelSlowBlocks(t *testing.T) {
	ifc := testIface(t)
	leakcheck.Check(t)
	k := kernelISOTP(t, ifc, isotp.Config{TxID: 0x6D1, RxID: 0x6D9})
	needPollTracksTx(t, k)
	p := startStallPeer(t, ifc, 0x6D1, 0x6D9, fcSlowBlock, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	start := time.Now()
	go func() { errc <- k.Send(ctx, pattern(4095)) }()
	p.waitFirstFrame(t)
	time.Sleep(2 * time.Second)
	cancel()
	cancelled := time.Now()
	err := <-errc
	t.Logf("Send returned %v %v after the cancellation (%v in all), %d consecutive frames sent",
		err, time.Since(cancelled), time.Since(start), p.cfs.Load())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send: %v, want context.Canceled", err)
	}
	if d := time.Since(cancelled); d > 200*time.Millisecond {
		t.Fatalf("Send returned %v after the cancellation", d)
	}
	if !k.closed.Load() {
		t.Fatal("the connection is still open")
	}
	p.ended(t)
}

// TestKernelISOTPCloseDuringStall: Close from another goroutine during a
// stalled transfer returns promptly and stops the transfer; the Send of
// that transfer, a Send waiting behind it and a later Send return
// ErrClosed. A Send behind the stalled one ends at its own deadline and
// leaves the connection open.
func TestKernelISOTPCloseDuringStall(t *testing.T) {
	ifc := testIface(t)
	leakcheck.Check(t)
	k := kernelISOTP(t, ifc, isotp.Config{TxID: 0x6D2, RxID: 0x6DA})
	needPollTracksTx(t, k)
	p := startStallPeer(t, ifc, 0x6D2, 0x6DA, fcWait, 800*time.Millisecond)

	stalled := make(chan error, 1)
	go func() { stalled <- k.Send(context.Background(), pattern(4095)) }()
	p.waitFirstFrame(t)

	// Behind the stalled transfer: the turn to send waits under ctx.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := k.Send(ctx, pattern(10)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send behind a transfer: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Send behind a transfer returned after %v", d)
	}
	if k.closed.Load() {
		t.Fatal("a Send that did not write closed the connection")
	}
	waiting := make(chan error, 1)
	go func() { waiting <- k.Send(context.Background(), pattern(10)) }()
	time.Sleep(200 * time.Millisecond)

	start = time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- k.Close() })
	}
	wg.Wait()
	closeTook := time.Since(start)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("Close:", err)
		}
	}
	err := <-stalled
	sendTook := time.Since(start)
	t.Logf("Close returned after %v; the stalled Send returned %v after %v", closeTook, err, sendTook)
	if closeTook > 200*time.Millisecond {
		t.Fatalf("Close took %v", closeTook)
	}
	if !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("stalled Send: %v, want ErrClosed", err)
	}
	if err := <-waiting; !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("waiting Send: %v, want ErrClosed", err)
	}
	if err := k.Send(context.Background(), pattern(10)); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("Send after Close: %v", err)
	}
	if _, err := k.Receive(context.Background()); !errors.Is(err, canbus.ErrClosed) {
		t.Fatalf("Receive after Close: %v", err)
	}
	p.ended(t)
}

// TestKernelISOTPSendSequence: Sends that follow each other wait for the
// transfer before them (the kernel's write returns after the first
// frame); a Send whose context ended before the write sends nothing and
// leaves the connection usable.
func TestKernelISOTPSendSequence(t *testing.T) {
	ifc := testIface(t)
	startSpy(t, ifc)
	k := kernelISOTP(t, ifc, isotp.Config{TxID: 0x6D3, RxID: 0x6DB, MaxMessageSize: 8000})
	u := userISOTP(t, ifc, isotp.Config{TxID: 0x6DB, RxID: 0x6D3, MaxMessageSize: 8000})

	done, cancel := context.WithCancel(context.Background())
	cancel()
	if err := k.Send(done, pattern(100)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send with an ended context: %v", err)
	}

	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	sizes := []int{1, 100, 4095, 7, 8000, 62}
	got := make(chan []byte, len(sizes))
	rerr := make(chan error, 1)
	go func() {
		for range sizes {
			b, err := u.Receive(ctx)
			if err != nil {
				rerr <- err
				return
			}
			got <- b
		}
		rerr <- nil
	}()
	for _, n := range sizes {
		if err := k.Send(ctx, pattern(n)); err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
	}
	if err := <-rerr; err != nil {
		t.Fatal(err)
	}
	for _, n := range sizes {
		if b := <-got; len(b) != n {
			t.Fatalf("received %d bytes, want %d", len(b), n)
		}
	}
}

// TestKernelISOTPWaitTxDone: the Send of kernels whose poll does not
// report a transfer in progress (CAN_ISOTP_WAIT_TX_DONE), on any kernel:
// transfers, the kernel's N_Bs error, Close.
func TestKernelISOTPWaitTxDone(t *testing.T) {
	ifc := testIface(t)
	startSpy(t, ifc)
	k, err := dialISOTP(ifc, isotp.Config{TxID: 0x6D4, RxID: 0x6DC, MaxMessageSize: 8000}, true)
	if errors.Is(err, syscall.EPROTONOSUPPORT) && os.Getenv("CANBUS_REQUIRE_ISOTP") != "1" {
		t.Skip("kernel ISO-TP (can-isotp) is not available; CANBUS_REQUIRE_ISOTP=1 makes this a failure")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if !k.waitTxDone {
		t.Fatal("waitTxDone not set")
	}
	u := userISOTP(t, ifc, isotp.Config{TxID: 0x6DC, RxID: 0x6D4, MaxMessageSize: 8000})
	for _, n := range []int{1, 100, 4095, 8000} {
		transfer(t, k, u, n)
	}
	// No receiver: N_Bs.
	nobody, err := dialISOTP(ifc, isotp.Config{TxID: 0x6D5, RxID: 0x6DD}, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := nobody.Send(ctx, pattern(100)); !errors.Is(err, isotp.ErrTimeout) {
		t.Fatal("N_Bs:", err)
	}
	if err := nobody.Close(); err != nil {
		t.Fatal(err)
	}
	if err := nobody.Send(ctx, pattern(1)); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
}

// TestHasKernelSymbol: the kallsyms lookup that tells the kernels whose
// ISO-TP poll reports a transfer in progress.
func TestHasKernelSymbol(t *testing.T) {
	const syms = "0000000000000000 T _stext\n" +
		"0000000000000000 t isotp_sendmsg\t[can_isotp]\n" +
		"0000000000000000 d isotp_poll_data\n" +
		"bad line\n"
	for _, tc := range []struct {
		list string
		want bool
	}{
		{syms + "0000000000000000 t isotp_poll\t[can_isotp]\n", true},
		{syms + "ffffffff81000000 T isotp_poll\n", true},
		{syms, false},
		{"0000000000000000 d isotp_poll\n", false},
		{"", false},
	} {
		if got := hasKernelSymbol(strings.NewReader(tc.list), "isotp_poll"); got != tc.want {
			t.Errorf("%q: %v, want %v", tc.list, got, tc.want)
		}
	}
	t.Logf("this kernel: isotp_poll %v", pollTracksTx())
}

// TestKernelISOTPFinishOrder: an error the kernel reported keeps its kind
// when Close runs before it is mapped; no socket is needed.
func TestKernelISOTPFinishOrder(t *testing.T) {
	c := &ISOTPConn{name: "none0"}
	c.closed.Store(true)
	ctx := context.Background()
	for _, tc := range []struct {
		err  error
		want error
	}{
		{syscall.ECOMM, isotp.ErrTimeout},
		{syscall.ETIMEDOUT, isotp.ErrTimeout},
		{syscall.EMSGSIZE, isotp.ErrOverflow},
		{os.ErrClosed, canbus.ErrClosed},
		{syscall.EBADF, canbus.ErrClosed},
	} {
		if err := c.finish(ctx, "send", tc.err); !errors.Is(err, tc.want) {
			t.Errorf("%v: %v, want %v", tc.err, err, tc.want)
		}
	}
}
