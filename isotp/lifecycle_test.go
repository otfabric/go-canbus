// SPDX-License-Identifier: MIT

package isotp

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
)

// The lifecycle contract tests of Conn (TESTING.md, rows L1 to L16). They
// run in a testing/synctest bubble: the virtual bus, the canbus.Bus and
// the Conn block on channels and timers only, so synctest.Wait returns
// when every goroutine waits (the interleaving is decided by the test, not
// by a sleep) and timeouts take fake time.

// bubbleConn returns a Conn on a virtual bus, created in the bubble of the
// caller, a second port that sees every frame the Conn sends and injects
// the peer's, and a function that closes everything.
func bubbleConn(t *testing.T, cfg Config) (c *Conn, peer *virtual.Port, closeAll func()) {
	t.Helper()
	vb := virtual.New(virtual.Config{})
	port, err := vb.Open(virtual.WithFD())
	if err != nil {
		t.Fatal(err)
	}
	peer, err = vb.Open(virtual.WithFD())
	if err != nil {
		t.Fatal(err)
	}
	bus, err := canbus.NewBus(port)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TxID == 0 {
		cfg.TxID, cfg.RxID = 0x7E0, 0x7E8
	}
	c, err = Dial(bus, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, peer, func() {
		_ = c.Close()
		_ = bus.Close()
		_ = vb.Close()
	}
}

// sent returns the payloads of the frames the peer port has received and
// not yet read, in hex, once every goroutine of the bubble waits. Reading
// "nothing more" takes a millisecond of fake time.
func sent(t *testing.T, peer *virtual.Port) []string {
	t.Helper()
	synctest.Wait()
	var out []string
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		var f canbus.Frame
		err := peer.Receive(ctx, &f)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, hex.EncodeToString(f.Payload()))
	}
}

// TestSendWaitingFollowsContext (L6): a Send that waits for another Send
// to finish ends at its own deadline, exactly, and sends nothing; the
// transfer it waited for goes on; a cancelled transfer frees the turn and
// the next Send goes out.
func TestSendWaitingFollowsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, peer, closeAll := bubbleConn(t, Config{TimeoutBs: time.Hour})
		defer closeAll()
		// A transfer that waits for a flow control nobody sends.
		transfer, cancelTransfer := context.WithCancel(context.Background())
		transferErr := make(chan error, 1)
		go func() { transferErr <- c.Send(transfer, payload(20)) }()
		synctest.Wait() // the first frame is out; Send waits for the flow control

		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := c.Send(ctx, []byte{0xAA}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Send behind a transfer: %v, want context.DeadlineExceeded", err)
		}
		if d := time.Since(start); d != 50*time.Millisecond {
			t.Fatalf("Send behind a transfer returned after %v, want its 50ms", d)
		}
		// A context that has ended already: the same, without waiting.
		if err := c.Send(ctx, []byte{0xAB}); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 50*time.Millisecond {
			t.Fatalf("Send with an ended context: %v after %v", err, time.Since(start))
		}
		select {
		case err := <-transferErr:
			t.Fatalf("the transfer ended with the Send that waited for it: %v", err)
		default:
		}
		cancelTransfer()
		if err := <-transferErr; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled transfer: %v", err)
		}
		// The turn is free again.
		if err := c.Send(context.Background(), []byte{0xBB}); err != nil {
			t.Fatalf("Send after the cancelled transfer: %v", err)
		}
		if got := sent(t, peer); len(got) != 2 || got[0][:4] != "1014" || got[1] != "01bb" {
			t.Fatalf("frames on the bus: %v, want the first frame of the transfer and 01bb", got)
		}
	})
}

// TestCloseWithSendWaiting (L4): Close ends the transfer in progress and
// the Send that waits for it; both return ErrClosed, and no frame of
// either reaches the bus after Close returned. A Send after Close fails
// at once.
func TestCloseWithSendWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, peer, closeAll := bubbleConn(t, Config{TimeoutBs: time.Hour})
		defer closeAll()
		errs := make(chan error, 2)
		go func() { errs <- c.Send(context.Background(), payload(20)) }()
		synctest.Wait() // the transfer waits for the flow control
		go func() { errs <- c.Send(context.Background(), []byte{0xAA}) }()
		synctest.Wait() // the second Send waits for its turn
		if got := sent(t, peer); len(got) != 1 || got[0][:4] != "1014" {
			t.Fatalf("frames on the bus before Close: %v, want the first frame only", got)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := <-errs; !errors.Is(err, canbus.ErrClosed) {
				t.Errorf("Send at Close: %v, want ErrClosed", err)
			}
		}
		if got := sent(t, peer); len(got) != 0 {
			t.Fatalf("frames on the bus after Close returned: %v", got)
		}
		if err := c.Send(context.Background(), []byte{0xCC}); !errors.Is(err, canbus.ErrClosed) {
			t.Fatalf("Send after Close: %v", err)
		}
		if got := sent(t, peer); len(got) != 0 {
			t.Fatalf("frames on the bus after a Send on a closed Conn: %v", got)
		}
	})
}

// TestConnCloseTwice (L1, L2): Close returns nil every time, one call
// after the other and from several goroutines at once, and the Conn does
// not close the bus it was given.
func TestConnCloseTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, closeAll := bubbleConn(t, Config{})
		defer closeAll()
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { errs <- c.Close() })
		}
		wg.Wait()
		errs <- c.Close()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("Close: %v", err)
			}
		}
		select {
		case <-c.bus.Done():
			t.Error("Close of the Conn closed its bus")
		default:
		}
		if _, err := c.Receive(context.Background()); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Receive after Close: %v", err)
		}
	})
}

// TestConnCloseDuringReceive (L3, L6): a Receive that waits ends with its
// context, at its deadline exactly, and with Close, with ErrClosed.
func TestConnCloseDuringReceive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, peer, closeAll := bubbleConn(t, Config{})
		defer closeAll()
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := c.Receive(ctx); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 50*time.Millisecond {
			t.Fatalf("Receive: %v after %v, want context.DeadlineExceeded after 50ms", err, time.Since(start))
		}
		// The Conn still receives after a Receive that ended.
		reply, _ := canbus.NewStandard(0x7E8, []byte{0x02, 0xAB, 0xCD})
		if err := peer.Send(context.Background(), &reply); err != nil {
			t.Fatal(err)
		}
		if got, err := c.Receive(context.Background()); err != nil || hex.EncodeToString(got) != "abcd" {
			t.Fatalf("Receive after a deadline: % X, %v", got, err)
		}
		errc := make(chan error, 1)
		go func() { _, err := c.Receive(context.Background()); errc <- err }()
		synctest.Wait() // Receive waits for a message
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-errc; !errors.Is(err, canbus.ErrClosed) {
			t.Fatalf("Receive at Close: %v, want ErrClosed", err)
		}
	})
}

// hookMetrics calls the function set for a callback, if any.
type hookMetrics struct {
	sent, received func()
	failure        func(op string)
}

func (m *hookMetrics) OnMessageSent(int, time.Duration) {
	if m.sent != nil {
		m.sent()
	}
}

func (m *hookMetrics) OnMessageReceived(int, time.Duration) {
	if m.received != nil {
		m.received()
	}
}

func (m *hookMetrics) OnError(op string, _ error) {
	if m.failure != nil {
		m.failure(op)
	}
}

// TestCallbackSendsReply (L9): a Metrics callback on the receiving
// goroutine sends a message that fits a single frame, as the
// documentation allows; the message that triggered it is delivered.
func TestCallbackSendsReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &hookMetrics{}
		c, peer, closeAll := bubbleConn(t, Config{Metrics: m})
		defer closeAll()
		sendErr := make(chan error, 1)
		m.received = func() { sendErr <- c.Send(context.Background(), []byte{0x7F, 0x22, 0x31}) }
		request, _ := canbus.NewStandard(0x7E8, []byte{0x02, 0x22, 0xF1})
		if err := peer.Send(context.Background(), &request); err != nil {
			t.Fatal(err)
		}
		if got, err := c.Receive(context.Background()); err != nil || hex.EncodeToString(got) != "22f1" {
			t.Fatalf("the message of the callback: % X, %v", got, err)
		}
		if err := <-sendErr; err != nil {
			t.Fatalf("Send in the callback: %v", err)
		}
		if got := sent(t, peer); len(got) != 1 || got[0] != "037f2231" {
			t.Fatalf("frames on the bus: %v, want the reply 037f2231", got)
		}
	})
}

// TestCallbackClosesConn (L10): a Metrics callback on the receiving
// goroutine closes the connection with "go conn.Close()", as the
// documentation says. That Close completes after the callback has
// returned, not before.
func TestCallbackClosesConn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &hookMetrics{}
		c, peer, closeAll := bubbleConn(t, Config{Metrics: m})
		defer closeAll()
		closed := make(chan error, 1)
		release := make(chan struct{})
		m.received = func() {
			go func() { closed <- c.Close() }()
			<-release // the callback is still running
		}
		request, _ := canbus.NewStandard(0x7E8, []byte{0x01, 0x3E})
		if err := peer.Send(context.Background(), &request); err != nil {
			t.Fatal(err)
		}
		synctest.Wait() // the callback waits; Close waits for the receiving goroutine
		select {
		case err := <-closed:
			t.Fatalf("Close returned while the callback ran: %v", err)
		default:
		}
		close(release)
		if err := <-closed; err != nil {
			t.Fatalf("Close started by the callback: %v", err)
		}
		if err := c.Send(context.Background(), []byte{1}); !errors.Is(err, canbus.ErrClosed) {
			t.Errorf("Send after Close: %v", err)
		}
	})
}

// failureLog collects the entries of callbacks that did not return
// normally: callback name → logged stack.
type failureLog struct {
	mu      sync.Mutex
	entries map[string]string
	other   []string
}

func (l *failureLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *failureLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *failureLog) WithGroup(string) slog.Handler            { return l }
func (l *failureLog) Handle(_ context.Context, rec slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rec.Message != "isotp: callback did not return normally" || rec.Level != slog.LevelError {
		l.other = append(l.other, rec.Message)
		return nil
	}
	var name, stack string
	rec.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "callback":
			name = a.Value.String()
		case "stack":
			stack = a.Value.String()
		}
		return true
	})
	if l.entries == nil {
		l.entries = map[string]string{}
	}
	l.entries[name] = stack
	return nil
}

// TestCallbackFailureLogged: a Metrics callback that does not return
// normally is stopped where the connection calls it and logged at error
// level with its stack;
// the connection goes on: the message is delivered, Send returns its own
// result, the next message is received.
func TestCallbackFailureLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fail := func() { panic("callback failed") }
		logs := &failureLog{}
		m := &hookMetrics{sent: fail, received: fail, failure: func(string) { fail() }}
		c, peer, closeAll := bubbleConn(t, Config{Metrics: m, Logger: slog.New(logs), TimeoutBs: time.Second})
		defer closeAll()
		for _, want := range []string{"aa", "bb"} {
			b, _ := hex.DecodeString("01" + want)
			f, _ := canbus.NewStandard(0x7E8, b)
			if err := peer.Send(context.Background(), &f); err != nil {
				t.Fatal(err)
			}
			if got, err := c.Receive(context.Background()); err != nil || hex.EncodeToString(got) != want {
				t.Fatalf("message after a failed OnMessageReceived: % X, %v", got, err)
			}
		}
		if err := c.Send(context.Background(), []byte{1, 2, 3}); err != nil {
			t.Fatalf("Send with a failing OnMessageSent: %v", err)
		}
		// A transfer nobody answers: OnError for "send", after N_Bs.
		if err := c.Send(context.Background(), payload(20)); !errors.Is(err, ErrTimeout) {
			t.Fatalf("Send with a failing OnError: %v, want ErrTimeout", err)
		}
		// A first frame without consecutive frames: OnError for "receive".
		ff, _ := canbus.NewStandard(0x7E8, []byte{0x10, 0x14, 1, 2, 3, 4, 5, 6})
		if err := peer.Send(context.Background(), &ff); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Receive(context.Background()); !errors.Is(err, ErrTimeout) {
			t.Fatalf("Receive with a failing OnError: %v, want ErrTimeout", err)
		}
		synctest.Wait()
		logs.mu.Lock()
		defer logs.mu.Unlock()
		for _, name := range []string{"Metrics.OnMessageSent", "Metrics.OnMessageReceived", "Metrics.OnError"} {
			stack, ok := logs.entries[name]
			if !ok {
				t.Errorf("no log entry for a failed %s (other entries: %v)", name, logs.other)
				continue
			}
			if !strings.Contains(stack, "hookMetrics") || !strings.Contains(stack, "goroutine ") {
				t.Errorf("%s: the logged stack does not show the callback:\n%s", name, stack)
			}
		}
	})
}
