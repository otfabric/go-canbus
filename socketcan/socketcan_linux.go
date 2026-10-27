// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/otfabric/go-canbus"
)

// Linux constants (linux/can.h, linux/can/raw.h, asm-generic/socket.h).
const (
	afCAN    = 29
	canRAW   = 1
	canISOTP = 6

	solCANRaw      = 101
	rawFilter      = 1
	rawErrFilter   = 2
	rawLoopback    = 3
	rawRecvOwnMsgs = 4
	rawFDFrames    = 5
	rawJoinFilters = 6

	soTimestampNS = 35 // SO_TIMESTAMPNS_OLD: struct timespec of the native long size
	soRxqOvfl     = 40
)

// sockaddrCAN is struct sockaddr_can (24 bytes).
type sockaddrCAN struct {
	family  uint16
	_       [2]byte
	ifindex int32
	rxID    uint32
	txID    uint32
	_       [8]byte
}

// Supported reports whether SocketCAN is available on this platform.
func Supported() bool { return true }

// Conn is a SocketCAN CAN_RAW socket bound to one interface (or to all CAN
// interfaces, for the name "any"). It implements [canbus.Transport],
// [canbus.Filterer], [canbus.Statser], [canbus.DropCounter],
// [canbus.Namer] and [canbus.Capabilitier].
type Conn struct {
	name    string
	ifindex int
	cfg     config

	file *os.File
	rc   syscall.RawConn

	// Receive and Send state, kept here so the poller callbacks (bound
	// once in Open) do not allocate per call.
	rmu    sync.Mutex
	rbuf   [canFDMTU]byte
	oob    [128]byte
	rsa    sockaddrCAN
	riov   syscall.Iovec
	rmsg   syscall.Msghdr
	rn     int
	rflags int
	rerr   error
	readFn func(fd uintptr) bool

	wmu     sync.Mutex
	wbuf    [canFDMTU]byte
	wn      int
	werr    error
	writeFn func(fd uintptr) bool

	closed atomic.Bool
	names  sync.Map // ifindex → name, for "any"

	ovflLast  uint32
	drops     atomic.Uint64
	txFrames  atomic.Uint64
	txBytes   atomic.Uint64
	rxFrames  atomic.Uint64
	rxBytes   atomic.Uint64
	rxErrors  atomic.Uint64
	txErrors  atomic.Uint64
	rxFailure atomic.Uint64
}

// Open opens a CAN_RAW socket on the interface ifname ("any" or "" for all
// CAN interfaces). It does not change the interface's configuration.
func Open(ifname string, opts ...Option) (*Conn, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	opErr := func(err error) error { return &canbus.OpError{Op: "open", Interface: ifname, Err: err} }
	c := &Conn{name: ifname, cfg: cfg}
	c.readFn = c.doRead
	c.writeFn = c.doWrite
	if ifname != "" && ifname != "any" {
		ifi, err := net.InterfaceByName(ifname)
		if err != nil {
			return nil, opErr(fmt.Errorf("%w: %v", canbus.ErrInterfaceDown, err))
		}
		c.ifindex = ifi.Index
		if cfg.fd && ifi.MTU != canFDMTU {
			return nil, opErr(fmt.Errorf("%w: interface MTU is %d, CAN FD needs %d (ip link set %s mtu 72)", canbus.ErrUnsupported, ifi.MTU, canFDMTU, ifname))
		}
	} else {
		c.name = "any"
	}
	fd, err := syscall.Socket(afCAN, syscall.SOCK_RAW|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, canRAW)
	if err != nil {
		return nil, opErr(fmt.Errorf("socket: %w", err))
	}
	ok := false
	defer func() {
		if !ok {
			syscall.Close(fd)
		}
	}()
	set := func(level, opt, v int) error {
		if err := syscall.SetsockoptInt(fd, level, opt, v); err != nil {
			return opErr(fmt.Errorf("setsockopt %d/%d: %w", level, opt, err))
		}
		return nil
	}
	if cfg.fd {
		if err := set(solCANRaw, rawFDFrames, 1); err != nil {
			return nil, err
		}
	}
	if cfg.noLoopback {
		if err := set(solCANRaw, rawLoopback, 0); err != nil {
			return nil, err
		}
	}
	if cfg.recvOwn {
		if err := set(solCANRaw, rawRecvOwnMsgs, 1); err != nil {
			return nil, err
		}
	}
	if !cfg.noTimestamp {
		if err := set(syscall.SOL_SOCKET, soTimestampNS, 1); err != nil {
			return nil, err
		}
	}
	if err := set(syscall.SOL_SOCKET, soRxqOvfl, 1); err != nil {
		return nil, err
	}
	if cfg.rcvBuf > 0 {
		if err := set(syscall.SOL_SOCKET, syscall.SO_RCVBUF, cfg.rcvBuf); err != nil {
			return nil, err
		}
	}
	if cfg.sndBuf > 0 {
		if err := set(syscall.SOL_SOCKET, syscall.SO_SNDBUF, cfg.sndBuf); err != nil {
			return nil, err
		}
	}
	if cfg.filters != nil {
		if err := setFilters(fd, *cfg.filters); err != nil {
			return nil, opErr(err)
		}
	}
	sa := sockaddrCAN{family: afCAN, ifindex: int32(c.ifindex)}
	if _, _, e := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa)); e != 0 {
		return nil, opErr(fmt.Errorf("bind: %w", mapErrno(e)))
	}
	c.file = os.NewFile(uintptr(fd), "can:"+c.name)
	c.rc, err = c.file.SyscallConn()
	if err != nil {
		c.file.Close()
		ok = true // the file owns fd now
		return nil, opErr(err)
	}
	ok = true
	return c, nil
}

func mapErrno(e error) error {
	switch {
	case errors.Is(e, syscall.ENETDOWN), errors.Is(e, syscall.ENODEV), errors.Is(e, syscall.ENXIO):
		return fmt.Errorf("%w: %v", canbus.ErrInterfaceDown, e)
	case errors.Is(e, syscall.ENOBUFS):
		return fmt.Errorf("%w: %v", canbus.ErrBufferOverflow, e)
	case errors.Is(e, syscall.EINVAL):
		return fmt.Errorf("%w: %v", canbus.ErrUnsupported, e)
	}
	return e
}

func setFilters(fd int, fs canbus.FilterSet) error {
	if err := syscall.SetsockoptString(fd, solCANRaw, rawFilter, string(kernelFilters(fs))); err != nil {
		return fmt.Errorf("CAN_RAW_FILTER: %w", err)
	}
	join := 0
	if fs.Join {
		join = 1
	}
	if err := syscall.SetsockoptInt(fd, solCANRaw, rawJoinFilters, join); err != nil {
		return fmt.Errorf("CAN_RAW_JOIN_FILTERS: %w", err)
	}
	if err := syscall.SetsockoptInt(fd, solCANRaw, rawErrFilter, int(fs.ErrorMask&canbus.ErrClassAll)); err != nil {
		return fmt.Errorf("CAN_RAW_ERR_FILTER: %w", err)
	}
	return nil
}

// SetFilters replaces the kernel filters and the error mask.
func (c *Conn) SetFilters(fs canbus.FilterSet) error {
	var serr error
	err := c.rc.Control(func(fd uintptr) { serr = setFilters(int(fd), fs) })
	if err == nil {
		err = serr
	}
	if err != nil {
		return &canbus.OpError{Op: "filter", Interface: c.name, Err: c.mapClosed(err)}
	}
	return nil
}

// Name returns the interface name ("any" for all interfaces).
func (c *Conn) Name() string { return c.name }

// Capabilities describes the socket.
func (c *Conn) Capabilities() canbus.Capabilities {
	return canbus.Capabilities{FD: c.cfg.fd, ErrorFrames: true, KernelFilters: true,
		Timestamps: !c.cfg.noTimestamp, RecvOwn: true, Physical: true}
}

var longAgo = time.Unix(1, 0)

// watch makes the file's read (or write) deadline end a blocked call when
// ctx ends. The returned function must be called when the call returns.
// A context without Done channel costs nothing (no allocation).
func watch(ctx context.Context, f *os.File, read bool) func() {
	if ctx.Done() == nil {
		return noop
	}
	set := f.SetWriteDeadline
	if read {
		set = f.SetReadDeadline
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = set(dl)
	}
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = set(longAgo)
		close(fired)
	})
	return func() {
		if !stop() {
			<-fired // the deadline must not be set after the reset below
		}
		_ = set(time.Time{})
	}
}

func noop() {}

func (c *Conn) mapClosed(err error) error {
	if errors.Is(err, os.ErrClosed) || c.closed.Load() {
		return canbus.ErrClosed
	}
	return err
}

func (c *Conn) finish(ctx context.Context, op string, err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The file deadline can expire a moment before the context's
		// timer fires.
		if dl, ok := ctx.Deadline(); ok && !time.Now().Before(dl) {
			return context.DeadlineExceeded
		}
		return canbus.ErrTimeout
	}
	err = c.mapClosed(err)
	if errors.Is(err, canbus.ErrClosed) {
		return err
	}
	return &canbus.OpError{Op: op, Interface: c.name, Err: mapErrno(err)}
}

// Send transmits a frame. A CAN FD frame needs a socket opened with
// [WithFD]. When the interface's transmit queue is full (ENOBUFS), Send
// retries until ctx ends.
func (c *Conn) Send(ctx context.Context, f *canbus.Frame) error {
	if c.closed.Load() {
		return canbus.ErrClosed
	}
	if err := f.Validate(); err != nil {
		return err
	}
	if f.Flags&canbus.FlagError != 0 {
		return fmt.Errorf("%w: error frames cannot be sent", canbus.ErrInvalidFrame)
	}
	if f.Flags&canbus.FlagFD != 0 && !c.cfg.fd {
		return &canbus.OpError{Op: "send", Interface: c.name, Err: fmt.Errorf("%w: CAN FD frame on a socket opened without WithFD", canbus.ErrUnsupported)}
	}
	if c.ifindex == 0 {
		return &canbus.OpError{Op: "send", Interface: c.name, Err: fmt.Errorf("%w: a socket bound to all interfaces cannot send", canbus.ErrUnsupported)}
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	n := marshal(f, &c.wbuf)
	stop := watch(ctx, c.file, false)
	defer stop()
	backoff := 50 * time.Microsecond
	for {
		c.wn = n
		err := c.rc.Write(c.writeFn)
		werr := c.werr
		if err == nil && werr == syscall.ENOBUFS {
			// The device queue is full; the kernel does not poll for it.
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
			backoff = min(2*backoff, 10*time.Millisecond)
			continue
		}
		if err == nil {
			err = werr
		}
		if err != nil {
			err = c.finish(ctx, "send", err)
			if !errors.Is(err, canbus.ErrClosed) && ctx.Err() == nil {
				c.txErrors.Add(1)
			}
			return err
		}
		c.txFrames.Add(1)
		c.txBytes.Add(uint64(len(f.Payload())))
		return nil
	}
}

// Receive waits for the next frame. Error frames arrive only when the
// error mask selects them ([WithFilters]). Frames carry the kernel's
// receive timestamp, the interface name and the Echo mark.
func (c *Conn) Receive(ctx context.Context, f *canbus.Frame) error {
	if c.closed.Load() {
		return canbus.ErrClosed
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	stop := watch(ctx, c.file, true)
	defer stop()
	for {
		err := c.rc.Read(c.readFn)
		if err == nil {
			err = c.rerr
		}
		if err != nil {
			err = c.finish(ctx, "receive", err)
			if !errors.Is(err, canbus.ErrClosed) && ctx.Err() == nil {
				c.rxFailure.Add(1)
			}
			return err
		}
		if err := unmarshal(c.rbuf[:c.rn], f); err != nil {
			c.rxFailure.Add(1)
			continue // not a CAN frame we know; the socket stays usable
		}
		f.Echo = c.rflags&syscall.MSG_CONFIRM != 0
		f.Timestamp = time.Time{}
		c.parseControl(c.oob[:int(c.rmsg.Controllen)], f)
		if f.Timestamp.IsZero() {
			f.Timestamp = time.Now()
		}
		f.Interface = c.ifName(int(c.rsa.ifindex))
		c.rxFrames.Add(1)
		if f.Flags&canbus.FlagError != 0 {
			c.rxErrors.Add(1)
		} else {
			c.rxBytes.Add(uint64(len(f.Payload())))
		}
		return nil
	}
}

// doRead is the poller callback of Receive: one recvmsg(2) into the
// Conn's buffers. It reports false to wait for readability.
func (c *Conn) doRead(fd uintptr) bool {
	c.riov.Base = &c.rbuf[0]
	c.riov.SetLen(len(c.rbuf))
	c.rmsg = syscall.Msghdr{}
	c.rmsg.Name = (*byte)(unsafe.Pointer(&c.rsa))
	c.rmsg.Namelen = uint32(unsafe.Sizeof(c.rsa))
	c.rmsg.Iov = &c.riov
	c.rmsg.Iovlen = 1
	c.rmsg.Control = &c.oob[0]
	c.rmsg.SetControllen(len(c.oob))
	r, _, e := syscall.Syscall(syscall.SYS_RECVMSG, fd, uintptr(unsafe.Pointer(&c.rmsg)), 0)
	if e != 0 {
		c.rerr = e
		c.rmsg.SetControllen(0)
		return e != syscall.EAGAIN
	}
	c.rerr = nil
	c.rn = int(r)
	c.rflags = int(c.rmsg.Flags)
	return true
}

// doWrite is the poller callback of Send. It reports false to wait for
// writability.
func (c *Conn) doWrite(fd uintptr) bool {
	_, c.werr = syscall.Write(int(fd), c.wbuf[:c.wn])
	return c.werr != syscall.EAGAIN
}

// parseControl reads the timestamp and drop counter from the control
// messages without allocating (syscall.ParseSocketControlMessage would).
func (c *Conn) parseControl(oob []byte, f *canbus.Frame) {
	const hdr = int(unsafe.Sizeof(syscall.Cmsghdr{}))
	align := func(n int) int { return (n + int(unsafe.Sizeof(uintptr(0))) - 1) &^ (int(unsafe.Sizeof(uintptr(0))) - 1) }
	for len(oob) >= hdr {
		h := (*syscall.Cmsghdr)(unsafe.Pointer(&oob[0]))
		l := int(h.Len)
		if l < hdr || l > len(oob) {
			return
		}
		data := oob[align(hdr):l]
		if h.Level == syscall.SOL_SOCKET {
			switch h.Type {
			case soTimestampNS:
				if len(data) >= int(unsafe.Sizeof(syscall.Timespec{})) {
					ts := (*syscall.Timespec)(unsafe.Pointer(&data[0]))
					f.Timestamp = time.Unix(int64(ts.Sec), int64(ts.Nsec))
				}
			case soRxqOvfl:
				if len(data) >= 4 {
					total := binary.NativeEndian.Uint32(data)
					if d := total - c.ovflLast; d != 0 { // wraps like the kernel's counter
						c.drops.Add(uint64(d))
						c.ovflLast = total
					}
				}
			}
		}
		if align(l) >= len(oob) {
			return
		}
		oob = oob[align(l):]
	}
}

func (c *Conn) ifName(idx int) string {
	if idx == c.ifindex && c.ifindex != 0 {
		return c.name
	}
	if v, ok := c.names.Load(idx); ok {
		return v.(string)
	}
	name := ""
	if ifi, err := net.InterfaceByIndex(idx); err == nil {
		name = ifi.Name
		c.names.Store(idx, name)
	}
	return name
}

// Drops returns the frames the kernel dropped because the socket's
// receive queue was full (SO_RXQ_OVFL). The kernel reports its counter
// with each received frame, so drops become visible with the first frame
// queued after them.
func (c *Conn) Drops() (uint64, canbus.DropReason) {
	return c.drops.Load(), canbus.DropKernelOverflow
}

// Stats returns the socket's counters.
func (c *Conn) Stats() canbus.Stats {
	return canbus.Stats{TxFrames: c.txFrames.Load(), TxBytes: c.txBytes.Load(),
		RxFrames: c.rxFrames.Load(), RxBytes: c.rxBytes.Load(), RxErrors: c.rxErrors.Load(),
		Dropped: c.drops.Load(), TxErrors: c.txErrors.Load(), RxFailure: c.rxFailure.Load()}
}

// Close closes the socket. Blocked Send and Receive calls return
// [canbus.ErrClosed].
func (c *Conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.file.Close()
}
