// SPDX-License-Identifier: MIT

package canbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Overflow is the policy of a subscription whose queue is full.
type Overflow uint8

// Overflow policies.
const (
	// DropNewest discards the frame that does not fit (the default). The
	// reader never waits for a slow subscriber.
	DropNewest Overflow = iota
	// DropOldest discards the oldest queued frame to make room.
	DropOldest
	// Block makes the bus reader wait until the subscriber takes the
	// frame. A slow subscriber then delays every other subscription and
	// lets the transport's own queue overflow instead: use it only when
	// losing frames in this process is worse.
	Block
)

// DefaultQueueSize is the queue capacity of a subscription that does not
// set one.
const DefaultQueueSize = 1024

// Bus shares one [Transport] between many users. A single reader
// goroutine receives every frame and hands a copy to each [Subscription]
// whose filters it passes; Send may be called from any goroutine.
//
// The Bus owns the transport: Close closes it. Subscriptions can be added
// and closed at any time, also while frames arrive. A frame read while no
// subscription exists reaches nobody: to receive from the first frame on,
// create the bus with [WithStartOnSubscribe].
//
// On an [XLTransport] the bus also carries CAN XL frames: [Bus.SendXL]
// sends them and subscriptions opened [WithXL] receive them.
type Bus struct {
	t         Transport
	xt        XLTransport // t, when it carries CAN XL
	iface     string
	log       *slog.Logger
	metrics   Metrics
	xlMetrics XLMetrics // metrics, when it observes CAN XL

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the reader has stopped

	mu   sync.Mutex
	subs atomic.Pointer[[]*Subscription] // copy on write
	err  error                           // why the reader stopped

	state     atomic.Uint32
	closeOnce sync.Once
	startOnce sync.Once
	lazy      bool // WithStartOnSubscribe

	txFrames, txBytes, rxFrames, rxBytes, rxErrors, dropped, txErrors, rxFailures atomic.Uint64
}

// Option configures a [Bus].
type Option func(*Bus) error

// WithLogger logs bus events (state changes, receive errors, drops) to l.
// A nil logger, the default, logs nothing.
func WithLogger(l *slog.Logger) Option {
	return func(b *Bus) error { b.log = l; return nil }
}

// WithMetrics reports frames, drops, errors and state changes to m.
func WithMetrics(m Metrics) Option {
	return func(b *Bus) error { b.metrics = m; return nil }
}

// WithStartOnSubscribe defers the bus's reader until the first
// [Bus.Subscribe] (or [Bus.Close]). Until then frames wait in the
// transport (a SocketCAN socket's receive buffer), so the first
// subscription sees them all; without this option the reader starts in
// NewBus and frames that arrive before the first subscription are lost.
// Error state ([Bus.State]) is tracked once the reader runs.
func WithStartOnSubscribe() Option {
	return func(b *Bus) error { b.lazy = true; return nil }
}

// WithInterfaceName sets the name reported to logs and metrics. By default
// it is the transport's [Namer] name, if any.
func WithInterfaceName(name string) Option {
	return func(b *Bus) error { b.iface = name; return nil }
}

// NewBus starts a bus on t. The reader goroutine starts at once and keeps
// draining the transport even while there are no subscriptions, so that
// a transport's own receive queue does not overflow.
func NewBus(t Transport, opts ...Option) (*Bus, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: nil transport", ErrInvalidOption)
	}
	b := &Bus{t: t, done: make(chan struct{})}
	if n, ok := t.(Namer); ok {
		b.iface = n.Name()
	}
	for _, o := range opts {
		if err := o(b); err != nil {
			return nil, err
		}
	}
	if b.log == nil {
		b.log = slog.New(discardHandler{})
	}
	if b.metrics == nil {
		b.metrics = NopMetrics{}
	}
	b.xlMetrics, _ = b.metrics.(XLMetrics)
	b.xt, _ = t.(XLTransport)
	empty := []*Subscription{}
	b.subs.Store(&empty)
	b.ctx, b.cancel = context.WithCancel(context.Background())
	b.setState(StateActive)
	if !b.lazy {
		b.start()
	}
	return b, nil
}

func (b *Bus) start() { b.startOnce.Do(func() { go b.run() }) }

// Name returns the interface name of the bus, possibly "".
func (b *Bus) Name() string { return b.iface }

// Transport returns the transport of the bus. Do not call its Receive: the
// bus reader does.
func (b *Bus) Transport() Transport { return b.t }

// Send validates the frame and transmits it. It is safe for concurrent
// use.
func (b *Bus) Send(ctx context.Context, f *Frame) error {
	if err := f.Validate(); err != nil {
		return err
	}
	select {
	case <-b.ctx.Done():
		return ErrClosed
	default:
	}
	if err := b.t.Send(ctx, f); err != nil {
		if ctx.Err() == nil || !errors.Is(err, ctx.Err()) {
			b.txErrors.Add(1)
			b.metrics.OnError(b.iface, "send", err)
		}
		return err
	}
	b.txFrames.Add(1)
	b.txBytes.Add(uint64(len(f.Payload())))
	b.metrics.OnFrameSent(b.iface, f)
	return nil
}

// SendXL validates a CAN XL frame and transmits it. It fails with
// [ErrUnsupported] when the transport does not carry CAN XL (is not an
// [XLTransport]). It is safe for concurrent use.
func (b *Bus) SendXL(ctx context.Context, x *XLFrame) error {
	if err := x.Validate(); err != nil {
		return err
	}
	if b.xt == nil {
		return fmt.Errorf("%w: the transport does not carry CAN XL frames", ErrUnsupported)
	}
	select {
	case <-b.ctx.Done():
		return ErrClosed
	default:
	}
	if err := b.xt.SendXL(ctx, x); err != nil {
		if ctx.Err() == nil || !errors.Is(err, ctx.Err()) {
			b.txErrors.Add(1)
			b.metrics.OnError(b.iface, "send", err)
		}
		return err
	}
	b.txFrames.Add(1)
	b.txBytes.Add(uint64(len(x.Data)))
	if b.xlMetrics != nil {
		b.xlMetrics.OnXLFrameSent(b.iface, x)
	}
	return nil
}

// State returns the current bus state.
func (b *Bus) State() State { return State(b.state.Load()) }

func (b *Bus) setState(s State) {
	old := State(b.state.Swap(uint32(s)))
	if old != s {
		b.metrics.OnStateChange(b.iface, old, s)
		if old != StateUnknown {
			b.log.Info("canbus: state changed", "interface", b.iface, "from", old.String(), "to", s.String())
		}
	}
}

// Stats returns the bus counters.
func (b *Bus) Stats() Stats {
	return Stats{
		TxFrames: b.txFrames.Load(), TxBytes: b.txBytes.Load(),
		RxFrames: b.rxFrames.Load(), RxBytes: b.rxBytes.Load(), RxErrors: b.rxErrors.Load(),
		Dropped: b.dropped.Load(), TxErrors: b.txErrors.Load(), RxFailure: b.rxFailures.Load(),
	}
}

// Err returns why the reader stopped: nil while it runs, [ErrClosed] after
// Close, or the transport error that ended it.
func (b *Bus) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Done returns a channel closed when the bus reader has stopped.
func (b *Bus) Done() <-chan struct{} { return b.done }

// Close stops the reader, closes every subscription and the transport.
// It waits for the reader to stop and is safe to call more than once.
func (b *Bus) Close() error {
	var err error
	b.closeOnce.Do(func() {
		b.mu.Lock()
		if b.err == nil {
			b.err = ErrClosed
		}
		b.mu.Unlock()
		b.cancel()
		err = b.t.Close()
		b.start() // a deferred reader ends at once and closes the subscriptions
		<-b.done
		b.setState(StateClosed)
	})
	return err
}

func (b *Bus) run() {
	defer close(b.done)
	defer b.closeSubs()
	var f Frame
	var x XLFrame
	var lastDrops uint64
	dc, hasDrops := b.t.(DropCounter)
	backoff := time.Duration(0)
	for {
		var xl bool
		var err error
		if b.xt != nil {
			xl, err = b.xt.ReceiveAny(b.ctx, &f, &x)
		} else {
			err = b.t.Receive(b.ctx, &f)
		}
		if hasDrops {
			if n, reason := dc.Drops(); n > lastDrops {
				d := n - lastDrops
				lastDrops = n
				b.dropped.Add(d)
				b.metrics.OnDrop(b.iface, reason, d)
				b.log.Warn("canbus: frames lost", "interface", b.iface, "reason", reason.String(), "count", d)
			}
		}
		if err != nil {
			if b.ctx.Err() != nil || errors.Is(err, ErrClosed) {
				b.mu.Lock()
				if b.err == nil {
					b.err = err
				}
				b.mu.Unlock()
				return
			}
			b.rxFailures.Add(1)
			b.metrics.OnError(b.iface, "receive", err)
			b.log.Warn("canbus: receive failed", "interface", b.iface, "error", err)
			if errors.Is(err, ErrInterfaceDown) {
				b.setState(StateDown)
			}
			// Back off on repeated failures so a failing transport does not
			// spin, but stay responsive to Close.
			backoff = min(max(2*backoff, time.Millisecond), time.Second)
			t := time.NewTimer(backoff)
			select {
			case <-b.ctx.Done():
				t.Stop()
			case <-t.C:
			}
			continue
		}
		backoff = 0
		if b.State() == StateDown {
			b.setState(StateActive)
		}
		b.rxFrames.Add(1)
		if xl {
			b.rxBytes.Add(uint64(len(x.Data)))
			if b.xlMetrics != nil {
				b.xlMetrics.OnXLFrameReceived(b.iface, &x)
			}
			for _, s := range *b.subs.Load() {
				s.deliverXL(&x)
			}
			continue
		}
		if f.Flags&FlagError != 0 {
			b.rxErrors.Add(1)
			b.trackErrorState(&f)
		} else {
			b.rxBytes.Add(uint64(len(f.Payload())))
		}
		b.metrics.OnFrameReceived(b.iface, &f)
		for _, s := range *b.subs.Load() {
			s.deliver(&f)
		}
	}
}

// trackErrorState follows the controller state reported by error frames.
func (b *Bus) trackErrorState(f *Frame) {
	info, _ := ParseError(f)
	switch {
	case info.Class&ErrClassBusOff != 0:
		b.setState(StateBusOff)
	case info.Class&ErrClassRestarted != 0:
		b.setState(StateActive)
	case info.Class&ErrClassController != 0:
		c := info.Controller
		switch {
		case c&(CtrlRxPassive|CtrlTxPassive) != 0:
			b.setState(StatePassive)
		case c&(CtrlRxWarning|CtrlTxWarning) != 0:
			b.setState(StateWarning)
		case c&CtrlActive != 0:
			b.setState(StateActive)
		}
	}
}

func (b *Bus) closeSubs() {
	b.mu.Lock()
	subs := *b.subs.Load()
	empty := []*Subscription{}
	b.subs.Store(&empty)
	err := b.err
	b.mu.Unlock()
	for _, s := range subs {
		s.end(err)
	}
}

// SubscribeOption configures a [Subscription].
type SubscribeOption func(*Subscription)

// WithFilters selects the frames the subscription receives. Without it,
// every data and remote frame is received and no error frame.
func WithFilters(fs FilterSet) SubscribeOption {
	return func(s *Subscription) {
		fs.Filters = append([]Filter(nil), fs.Filters...)
		s.filters = fs
	}
}

// WithXL makes the subscription receive CAN XL frames too, through a
// second queue of the same capacity and overflow policy
// ([Subscription.ReceiveXL], [Subscription.XLFrames]). The subscription's
// filters select CAN XL frames as Linux's CAN_RAW_FILTER does
// ([FilterSet.MatchXL]). Without it, CAN XL frames are not delivered, as
// to a SocketCAN socket without CAN_RAW_XL_FRAMES. The two queues are
// independent: order a mixed stream by the frames' timestamps.
func WithXL() SubscribeOption {
	return func(s *Subscription) { s.xl = true }
}

// WithQueueSize sets the queue capacity (default [DefaultQueueSize]).
func WithQueueSize(n int) SubscribeOption {
	return func(s *Subscription) {
		if n > 0 {
			s.capacity = n
		}
	}
}

// WithOverflow sets the policy when the queue is full (default
// [DropNewest]).
func WithOverflow(o Overflow) SubscribeOption {
	return func(s *Subscription) { s.overflow = o }
}

// WithoutEcho drops frames the transport marks as this socket's own
// transmissions ([Frame.Echo]).
func WithoutEcho() SubscribeOption {
	return func(s *Subscription) { s.noEcho = true }
}

// Subscribe adds a subscription. It returns [ErrClosed] when the bus is
// closed.
func (b *Bus) Subscribe(opts ...SubscribeOption) (*Subscription, error) {
	s := &Subscription{bus: b, capacity: DefaultQueueSize, done: make(chan struct{})}
	for _, o := range opts {
		o(s)
	}
	s.ch = make(chan Frame, s.capacity)
	if s.xl {
		s.xch = make(chan XLFrame, s.capacity)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return nil, ErrClosed
	}
	old := *b.subs.Load()
	next := make([]*Subscription, len(old), len(old)+1)
	copy(next, old)
	next = append(next, s)
	b.subs.Store(&next)
	b.start()
	return s, nil
}

func (b *Bus) unsubscribe(s *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	old := *b.subs.Load()
	next := make([]*Subscription, 0, len(old))
	for _, o := range old {
		if o != s {
			next = append(next, o)
		}
	}
	b.subs.Store(&next)
}

// Subscription receives the frames of a [Bus] that pass its filters,
// through a bounded queue. Receive and Frames may be used from one
// goroutine at a time; Close and the counters from any.
type Subscription struct {
	bus      *Bus
	filters  FilterSet
	capacity int
	overflow Overflow
	noEcho   bool
	xl       bool

	ch       chan Frame
	xch      chan XLFrame // WithXL
	done     chan struct{}
	endOnce  sync.Once
	errMu    sync.Mutex
	err      error
	received atomic.Uint64
	dropped  atomic.Uint64
}

func (s *Subscription) deliver(f *Frame) {
	if s.noEcho && f.Echo {
		return
	}
	if !s.filters.Match(f) {
		return
	}
	// Count before queueing: a consumer that has the frame sees it counted.
	s.received.Add(1)
	if !enqueue(s, s.ch, *f) {
		s.received.Add(^uint64(0))
		return
	}
	s.bus.metrics.OnQueue(s.bus.iface, len(s.ch), s.capacity)
}

func (s *Subscription) deliverXL(x *XLFrame) {
	if !s.xl || (s.noEcho && x.Echo) || !s.filters.MatchXL(x) {
		return
	}
	c := x.Clone() // the bus reader reuses x.Data
	s.received.Add(1)
	if !enqueue(s, s.xch, c) {
		s.received.Add(^uint64(0))
		return
	}
	s.bus.metrics.OnQueue(s.bus.iface, len(s.xch), s.capacity)
}

// enqueue queues v following the subscription's overflow policy. It
// reports false when v was not queued.
func enqueue[T any](s *Subscription, ch chan T, v T) bool {
	switch s.overflow {
	case Block:
		select {
		case ch <- v:
			return true
		case <-s.done:
		case <-s.bus.ctx.Done():
		}
		return false
	case DropOldest:
		for {
			select {
			case ch <- v:
				return true
			default:
			}
			select {
			case <-ch:
				s.drop()
			default:
			}
		}
	default:
		select {
		case ch <- v:
			return true
		default:
			s.drop()
			return false
		}
	}
}

func (s *Subscription) drop() {
	s.dropped.Add(1)
	s.bus.dropped.Add(1)
	s.bus.metrics.OnDrop(s.bus.iface, DropQueueFull, 1)
}

// Receive returns the next frame. It returns the context's error when ctx
// ends first, and, once the queue is empty, [ErrClosed] after Close or
// the error that stopped the bus.
func (s *Subscription) Receive(ctx context.Context) (Frame, error) {
	select {
	case f := <-s.ch:
		return f, nil
	default:
	}
	select {
	case f := <-s.ch:
		return f, nil
	case <-s.done:
		select {
		case f := <-s.ch:
			return f, nil
		default:
			return Frame{}, s.Err()
		}
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}

// ReceiveXL returns the next CAN XL frame of a subscription opened
// [WithXL], like [Subscription.Receive]. Without WithXL it returns
// [ErrUnsupported].
func (s *Subscription) ReceiveXL(ctx context.Context) (XLFrame, error) {
	if !s.xl {
		return XLFrame{}, fmt.Errorf("%w: subscription opened without WithXL", ErrUnsupported)
	}
	select {
	case x := <-s.xch:
		return x, nil
	default:
	}
	select {
	case x := <-s.xch:
		return x, nil
	case <-s.done:
		select {
		case x := <-s.xch:
			return x, nil
		default:
			return XLFrame{}, s.Err()
		}
	case <-ctx.Done():
		return XLFrame{}, ctx.Err()
	}
}

// XLFrames returns the CAN XL queue of a subscription opened [WithXL] as
// a channel (nil without WithXL). The channel is never closed.
func (s *Subscription) XLFrames() <-chan XLFrame { return s.xch }

// Frames returns the queue as a channel, for use in select statements
// together with [Subscription.Done]. The channel is never closed.
func (s *Subscription) Frames() <-chan Frame { return s.ch }

// Done returns a channel closed when the subscription ends.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Err returns why the subscription ended, or nil while it is open.
func (s *Subscription) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// Received returns the number of frames queued so far.
func (s *Subscription) Received() uint64 { return s.received.Load() }

// Dropped returns the number of frames lost because the queue was full.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close ends the subscription. Frames still queued can be read with
// Receive until it returns [ErrClosed].
func (s *Subscription) Close() error {
	s.bus.unsubscribe(s)
	s.end(ErrClosed)
	return nil
}

func (s *Subscription) end(err error) {
	s.endOnce.Do(func() {
		if err == nil {
			err = ErrClosed
		}
		s.errMu.Lock()
		s.err = err
		s.errMu.Unlock()
		close(s.done)
	})
}

// discardHandler is a slog.Handler that drops everything (slog.DiscardHandler
// needs Go 1.24).
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }
