// SPDX-License-Identifier: MIT

// Package virtual is a deterministic in-memory CAN bus for tests and
// simulation, on every operating system.
//
// A [Bus] has any number of ports; each port is a [canbus.Transport]. All
// frames, from every port, pass through one ordered delivery queue, so
// every port observes the same global order. Delivery follows SocketCAN's
// rules: a frame goes to every other port while the sending port has
// loopback on (the default) and back to the sender only with
// receive-own-messages; a port opened without CAN FD neither sends nor
// receives CAN FD frames.
//
// Faults are injected with a seeded pseudo-random generator, so a run is
// reproducible for a given seed and order of sends: dropped, duplicated,
// delayed and corrupted frames, error frames, the interface going down and
// up, ports closing abruptly, and scripted frames from a simulated node
// ([Bus.Inject]), including frames that violate the frame rules
// ([Bus.InjectRaw]).
//
// The bus models frame delivery only. There is no arbitration, bit timing,
// acknowledgement, error confinement, automatic retransmission or
// electrical behaviour: a corrupted frame is delivered corrupted, where a
// real bus would reject it by its CRC and retransmit it.
package virtual

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
)

// Config configures a [Bus].
type Config struct {
	// Name is the interface name ports report and frames carry
	// (default "virtual0").
	Name string
	// Seed seeds the fault generator (default 1).
	Seed int64
	// QueueSize is the capacity of the bus's delivery queue (default
	// 4096). Send blocks while it is full.
	QueueSize int
	// Clock returns the receive timestamp of frames (default time.Now).
	Clock func() time.Time
}

// Faults are probabilities and parameters of injected faults. Each frame
// passing the delivery queue draws from the seeded generator in a fixed
// order: drop, duplicate, delay, corrupt.
type Faults struct {
	Drop      float64       // probability a frame is lost
	Duplicate float64       // probability a frame is delivered twice
	Delay     float64       // probability a frame is held for DelayBy
	DelayBy   time.Duration // hold time of a delayed frame; later frames wait behind it (the bus is serial)
	Corrupt   float64       // probability one payload bit is flipped
	// DropNext drops the next n frames regardless of the probabilities.
	DropNext int
}

// Bus is an in-memory CAN bus. Its methods are safe for concurrent use.
type Bus struct {
	name  string
	clock func() time.Time

	sendMu sync.Mutex // orders fault decisions with queueing
	rng    *rand.Rand
	faults Faults

	queue chan item
	stop  chan struct{}
	done  chan struct{}

	mu     sync.Mutex
	ports  []*Port
	up     bool
	closed bool
	once   sync.Once

	delivered, dropped, duplicated, delayed, corrupted atomic.Uint64
}

type item struct {
	f     canbus.Frame
	from  *Port // nil for injected frames
	delay time.Duration
}

// New returns a running bus.
func New(cfg Config) *Bus {
	if cfg.Name == "" {
		cfg.Name = "virtual0"
	}
	if cfg.Seed == 0 {
		cfg.Seed = 1
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 4096
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	b := &Bus{
		name:  cfg.Name,
		clock: cfg.Clock,
		rng:   rand.New(rand.NewSource(cfg.Seed)),
		queue: make(chan item, cfg.QueueSize),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		up:    true,
	}
	go b.dispatch()
	return b
}

// Name returns the interface name of the bus.
func (b *Bus) Name() string { return b.name }

// SetFaults replaces the fault configuration.
func (b *Bus) SetFaults(f Faults) {
	b.sendMu.Lock()
	b.faults = f
	b.sendMu.Unlock()
}

// FaultStats reports how many frames were delivered and affected by
// faults so far.
type FaultStats struct {
	Delivered  uint64 `json:"delivered"`
	Dropped    uint64 `json:"dropped"`
	Duplicated uint64 `json:"duplicated"`
	Delayed    uint64 `json:"delayed"`
	Corrupted  uint64 `json:"corrupted"`
}

// FaultStats returns the fault counters.
func (b *Bus) FaultStats() FaultStats {
	return FaultStats{b.delivered.Load(), b.dropped.Load(), b.duplicated.Load(), b.delayed.Load(), b.corrupted.Load()}
}

// SetUp brings the interface up or down. While it is down, Send fails with
// [canbus.ErrInterfaceDown], every port's next Receive returns that error
// once (as a SocketCAN socket reports ENETDOWN), and no frame is
// delivered.
func (b *Bus) SetUp(up bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.up == up {
		return
	}
	b.up = up
	if !up {
		for _, p := range b.ports {
			select {
			case p.errs <- canbus.ErrInterfaceDown:
			default:
			}
		}
	}
}

// usable reports ErrClosed or ErrInterfaceDown when frames cannot be
// queued.
func (b *Bus) usable() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.closed:
		return canbus.ErrClosed
	case !b.up:
		return canbus.ErrInterfaceDown
	}
	return nil
}

// Inject delivers a frame to every port as if a node outside this process
// had sent it. The frame is validated. Faults apply.
func (b *Bus) Inject(ctx context.Context, f canbus.Frame) error {
	if err := f.Validate(); err != nil {
		return err
	}
	return b.enqueue(ctx, f, nil)
}

// InjectRaw delivers a frame without validating it, to test how receivers
// handle frames a conforming transport never produces (a classical frame
// with 9 bytes, an unknown flag, garbage beyond Len).
func (b *Bus) InjectRaw(ctx context.Context, f canbus.Frame) error {
	return b.enqueue(ctx, f, nil)
}

// InjectError delivers an error frame to every port whose error mask
// selects it.
func (b *Bus) InjectError(ctx context.Context, info canbus.ErrorInfo) error {
	return b.enqueue(ctx, canbus.NewErrorFrame(info), nil)
}

func (b *Bus) enqueue(ctx context.Context, f canbus.Frame, from *Port) error {
	if err := b.usable(); err != nil {
		return err
	}
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	it := item{f: f, from: from}
	fl := &b.faults
	if fl.DropNext > 0 {
		fl.DropNext--
		b.dropped.Add(1)
		return nil
	}
	// Draw all four values every time so the sequence does not depend on
	// which faults are enabled.
	pDrop, pDup, pDelay, pCorrupt := b.rng.Float64(), b.rng.Float64(), b.rng.Float64(), b.rng.Float64()
	bit := b.rng.Intn(512)
	if pDrop < fl.Drop {
		b.dropped.Add(1)
		return nil
	}
	if pDelay < fl.Delay {
		it.delay = fl.DelayBy
		b.delayed.Add(1)
	}
	if pCorrupt < fl.Corrupt && it.f.Flags&(canbus.FlagRemote|canbus.FlagError) == 0 && it.f.Len > 0 {
		bit %= int(it.f.Len) * 8
		it.f.Data[bit/8] ^= 1 << (bit % 8)
		b.corrupted.Add(1)
	}
	n := 1
	if pDup < fl.Duplicate {
		n = 2
		b.duplicated.Add(1)
	}
	for i := 0; i < n; i++ {
		select {
		case b.queue <- it:
		case <-b.stop:
			return canbus.ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
		it.delay = 0
	}
	return nil
}

func (b *Bus) dispatch() {
	defer close(b.done)
	for {
		select {
		case <-b.stop:
			return
		case it := <-b.queue:
			if it.delay > 0 {
				t := time.NewTimer(it.delay)
				select {
				case <-t.C:
				case <-b.stop:
					t.Stop()
					return
				}
			}
			b.deliver(&it)
		}
	}
}

func (b *Bus) deliver(it *item) {
	b.mu.Lock()
	if !b.up {
		b.mu.Unlock()
		return
	}
	ports := append([]*Port(nil), b.ports...)
	b.mu.Unlock()
	it.f.Timestamp = b.clock()
	it.f.Interface = b.name
	b.delivered.Add(1)
	for _, p := range ports {
		f := it.f
		if p == it.from {
			if !p.recvOwn {
				continue
			}
			f.Echo = true
		} else {
			f.Echo = false
			if it.from != nil && !it.from.loopback {
				continue
			}
		}
		p.offer(&f)
	}
}

// Close closes every port and stops the bus.
func (b *Bus) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		ports := append([]*Port(nil), b.ports...)
		b.mu.Unlock()
		close(b.stop)
		<-b.done
		for _, p := range ports {
			_ = p.Close()
		}
	})
	return nil
}

// PortOption configures a [Port].
type PortOption func(*Port)

// WithFD opens the port for CAN FD frames.
func WithFD() PortOption { return func(p *Port) { p.fd = true } }

// WithRecvOwn delivers the port's own transmissions back to it, marked
// with [canbus.Frame.Echo].
func WithRecvOwn() PortOption { return func(p *Port) { p.recvOwn = true } }

// WithoutLoopback stops the port's transmissions from reaching the other
// ports of this bus (as CAN_RAW_LOOPBACK off: in SocketCAN they would
// still reach other hosts on a real bus).
func WithoutLoopback() PortOption { return func(p *Port) { p.loopback = false } }

// WithQueueSize sets the port's receive queue capacity (default 1024).
func WithQueueSize(n int) PortOption {
	return func(p *Port) {
		if n > 0 {
			p.capacity = n
		}
	}
}

// WithBlocking makes delivery wait for room in the port's receive queue
// instead of dropping the frame. The whole bus then waits for the
// slowest blocking port.
func WithBlocking() PortOption { return func(p *Port) { p.block = true } }

// WithFilters sets the port's filters, as a SocketCAN socket's kernel
// filters (see [canbus.FilterSet]).
func WithFilters(fs canbus.FilterSet) PortOption {
	return func(p *Port) { p.setFilters(fs) }
}

// Open adds a port to the bus.
func (b *Bus) Open(opts ...PortOption) (*Port, error) {
	p := &Port{bus: b, loopback: true, capacity: 1024, closed: make(chan struct{}), errs: make(chan error, 1)}
	p.setFilters(canbus.FilterSet{})
	for _, o := range opts {
		o(p)
	}
	p.rx = make(chan canbus.Frame, p.capacity)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, canbus.ErrClosed
	}
	b.ports = append(b.ports, p)
	return p, nil
}

func (b *Bus) remove(p *Port) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, q := range b.ports {
		if q == p {
			b.ports = append(b.ports[:i:i], b.ports[i+1:]...)
			return
		}
	}
}

// Port is one endpoint on a virtual bus. It implements [canbus.Transport],
// [canbus.Filterer], [canbus.Statser], [canbus.DropCounter],
// [canbus.Namer] and [canbus.Capabilitier].
type Port struct {
	bus      *Bus
	fd       bool
	recvOwn  bool
	loopback bool
	block    bool
	capacity int
	filters  atomic.Pointer[canbus.FilterSet]

	rx       chan canbus.Frame
	errs     chan error
	closed   chan struct{}
	closeErr sync.Once

	txFrames, txBytes, rxFrames, rxBytes, drops atomic.Uint64
}

func (p *Port) setFilters(fs canbus.FilterSet) {
	fs.Filters = append([]canbus.Filter(nil), fs.Filters...)
	p.filters.Store(&fs)
}

// SetFilters replaces the port's filters.
func (p *Port) SetFilters(fs canbus.FilterSet) error {
	p.setFilters(fs)
	return nil
}

func (p *Port) offer(f *canbus.Frame) {
	if f.Flags&canbus.FlagFD != 0 && !p.fd {
		return
	}
	if !p.filters.Load().Match(f) {
		return
	}
	if p.block {
		select {
		case p.rx <- *f:
		case <-p.closed:
		case <-p.bus.stop:
		}
		return
	}
	select {
	case p.rx <- *f:
	default:
		p.drops.Add(1)
	}
}

// Name returns the bus name.
func (p *Port) Name() string { return p.bus.name }

// Capabilities describes the port.
func (p *Port) Capabilities() canbus.Capabilities {
	return canbus.Capabilities{FD: p.fd, ErrorFrames: true, Timestamps: true, RecvOwn: true}
}

// Send queues a frame for delivery. A CAN FD frame on a port opened
// without [WithFD] fails with [canbus.ErrUnsupported].
func (p *Port) Send(ctx context.Context, f *canbus.Frame) error {
	select {
	case <-p.closed:
		return canbus.ErrClosed
	default:
	}
	if err := f.Validate(); err != nil {
		return err
	}
	if f.Flags&canbus.FlagError != 0 {
		return fmt.Errorf("%w: error frames cannot be sent", canbus.ErrInvalidFrame)
	}
	if f.Flags&canbus.FlagFD != 0 && !p.fd {
		return fmt.Errorf("%w: CAN FD frame on a classical port", canbus.ErrUnsupported)
	}
	if err := p.bus.enqueue(ctx, *f, p); err != nil {
		return err
	}
	p.txFrames.Add(1)
	p.txBytes.Add(uint64(f.Len))
	return nil
}

// Receive waits for the next frame.
func (p *Port) Receive(ctx context.Context, f *canbus.Frame) error {
	select {
	case err := <-p.errs:
		return err
	case *f = <-p.rx:
	case <-p.closed:
		return canbus.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	p.rxFrames.Add(1)
	p.rxBytes.Add(uint64(len(f.Payload())))
	return nil
}

// Close detaches the port from the bus; blocked calls return
// [canbus.ErrClosed].
func (p *Port) Close() error {
	p.closeErr.Do(func() {
		close(p.closed)
		p.bus.remove(p)
	})
	return nil
}

// Stats returns the port's counters.
func (p *Port) Stats() canbus.Stats {
	return canbus.Stats{TxFrames: p.txFrames.Load(), TxBytes: p.txBytes.Load(),
		RxFrames: p.rxFrames.Load(), RxBytes: p.rxBytes.Load(), Dropped: p.drops.Load()}
}

// Drops returns the frames lost to a full receive queue.
func (p *Port) Drops() (uint64, canbus.DropReason) {
	return p.drops.Load(), canbus.DropQueueFull
}
