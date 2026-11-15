// SPDX-License-Identifier: MIT

//go:build linux && (amd64 || arm64 || arm || riscv64 || ppc64le || loong64)

package socketcan

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/isotp"
)

// Linux ISO-TP socket options (linux/can/isotp.h).
const (
	solCANISOTP     = 106
	isotpOpts       = 1
	isotpRecvFC     = 2
	isotpTxSTmin    = 3
	isotpLLOpts     = 5
	isotpListenMode = 0x001
	isotpExtendAddr = 0x002
	isotpTxPadding  = 0x004
	isotpRxExtAddr  = 0x200
	isotpForceTxSTm = 0x080
	isotpWaitTxDone = 0x400
)

// maxKernelISOTPMessage is the largest Config.MaxMessageSize DialISOTP
// accepts: the kernel's upper bound for its max_pdu_size module
// parameter (MAX_PDU_SIZE, 1025 KiB in net/can/isotp.c). The kernel
// itself refuses messages beyond max_pdu_size (default 8300 bytes).
const maxKernelISOTPMessage = 1025 * 1024

// kernelTimeout is the kernel's fixed N_Bs and N_Cr.
const kernelTimeout = time.Second

// Close interrupts the kernel's wait for a transfer in progress with
// SIGURG to the closing thread: the first signal after closeSignalAfter,
// then one every closeSignalEvery until the descriptor is closed.
const (
	closeSignalAfter = 5 * time.Millisecond
	closeSignalEvery = 20 * time.Millisecond
)

// ISOTPConn is a kernel ISO-TP socket (CAN_ISOTP, Linux ≥ 5.10 or the
// can-isotp module). It implements [isotp.Endpoint].
type ISOTPConn struct {
	name   string
	file   *os.File
	rc     syscall.RawConn
	maxMsg int
	rmu    sync.Mutex
	closed atomic.Bool

	// wsem is the turn to send: one transfer at a time. A Send waiting
	// for it ends at its ctx or at Close (done).
	wsem chan struct{}
	done chan struct{}

	// sock is a second descriptor of the socket. It keeps the socket
	// open when file is closed, so that Close, not whichever goroutine
	// drops file's last reference, releases it (see closeSocket).
	sock      int
	closeOnce sync.Once
	closeErr  error

	// waitTxDone: the kernel's poll does not report a transfer in
	// progress, so Send uses CAN_ISOTP_WAIT_TX_DONE (see sendWaitTxDone).
	waitTxDone bool
}

// pollTracksTx reports whether the kernel's ISO-TP sockets poll
// writable only while no transfer is in progress. net/can/isotp.c has
// its own poll function (isotp_poll) for that since Linux 6.3; the
// 5.10, 5.15 and 6.1 stable series received it later. Before, the
// sockets used datagram_poll, which reports POLLOUT during a transfer.
// The function is looked up by name in /proc/kallsyms; a kernel whose
// symbols cannot be read counts as one without it.
var pollTracksTx = sync.OnceValue(func() bool { return kernelSymbol("/proc/kallsyms", "isotp_poll") })

// kernelSymbol reports whether the kallsyms file at path names the text
// symbol name; false when the file cannot be read.
func kernelSymbol(path, name string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return hasKernelSymbol(f, name)
}

// hasKernelSymbol reports whether the kallsyms listing r names the text
// symbol name ("address type name [module]").
func hasKernelSymbol(r io.Reader, name string) bool {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 3 && f[2] == name && (f[1] == "t" || f[1] == "T") {
			return true
		}
	}
	return false
}

// DialISOTP opens a kernel ISO-TP socket on the interface with the same
// configuration the userspace isotp.Conn takes. The kernel does not
// support Config.SendFlowWait, MaxWait other than its default (10), or
// N_Bs/N_Cr values other than its fixed 1 s; a Config asking for them is
// rejected with [canbus.ErrUnsupported]. MaxMessageSize (default 4095)
// is at most 1025 KiB, the kernel's own bound; a larger one is rejected
// with [canbus.ErrInvalidOption].
func DialISOTP(ifname string, cfg isotp.Config) (*ISOTPConn, error) {
	return dialISOTP(ifname, cfg, false)
}

// dialISOTP is DialISOTP; waitTxDone selects the Send of kernels whose
// poll does not report a transfer in progress also on others (tests).
func dialISOTP(ifname string, cfg isotp.Config, waitTxDone bool) (*ISOTPConn, error) {
	opErr := func(err error) error { return &canbus.OpError{Op: "open isotp", Interface: ifname, Err: err} }
	if cfg.SendFlowWait != 0 {
		return nil, opErr(fmt.Errorf("%w: the kernel cannot send WAIT flow controls", canbus.ErrUnsupported))
	}
	if cfg.MaxWait != 0 && cfg.MaxWait != 10 {
		return nil, opErr(fmt.Errorf("%w: the kernel's WAIT limit is fixed, MaxWait %d", canbus.ErrUnsupported, cfg.MaxWait))
	}
	for _, d := range []time.Duration{cfg.TimeoutBs, cfg.TimeoutCr} {
		if d != 0 && d != kernelTimeout {
			return nil, opErr(fmt.Errorf("%w: the kernel's N_Bs and N_Cr are fixed at %v, got %v", canbus.ErrUnsupported, kernelTimeout, d))
		}
	}
	if cfg.MaxMessageSize > maxKernelISOTPMessage {
		return nil, opErr(fmt.Errorf("%w: MaxMessageSize %d exceeds %d", canbus.ErrInvalidOption, cfg.MaxMessageSize, maxKernelISOTPMessage))
	}
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, opErr(fmt.Errorf("%w: %v", canbus.ErrInterfaceDown, err))
	}
	st, err := isotp.EncodeSTmin(cfg.STmin)
	if err != nil {
		return nil, opErr(err)
	}
	var ns uint32 // CAN_ISOTP_TX_STMIN
	if cfg.ForceTxSTmin != nil {
		if ns, err = forceTxSTminNS(*cfg.ForceTxSTmin); err != nil {
			return nil, opErr(err)
		}
	}
	fd, err := syscall.Socket(afCAN, syscall.SOCK_DGRAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, canISOTP)
	if err != nil {
		return nil, opErr(fmt.Errorf("socket: %w (is the can-isotp module available?)", err))
	}
	ok := false
	defer func() {
		if !ok {
			_ = syscall.Close(fd)
		}
	}()
	// struct can_isotp_options
	opts := make([]byte, 12)
	// Without CAN_ISOTP_WAIT_TX_DONE the non-blocking write returns once
	// the first frame is sent, and Send waits for the end of the
	// transfer through the poller. That needs a kernel whose poll reports
	// a transfer in progress (the socket exists now, so its module is
	// loaded); with other kernels Send waits inside the write.
	waitTxDone = waitTxDone || !pollTracksTx()
	var flags uint32
	if waitTxDone {
		flags |= isotpWaitTxDone
	}
	if cfg.ListenOnly {
		flags |= isotpListenMode
	}
	if cfg.Addressing != isotp.Normal {
		flags |= isotpExtendAddr | isotpRxExtAddr
		opts[8] = cfg.TxAddress  // ext_address
		opts[11] = cfg.RxAddress // rx_ext_address
	}
	pad := byte(0xCC)
	if cfg.PadByte != nil {
		pad = *cfg.PadByte
	}
	if cfg.Padding {
		flags |= isotpTxPadding
	}
	opts[9], opts[10] = pad, pad
	if cfg.ForceTxSTmin != nil {
		flags |= isotpForceTxSTm
	}
	binary.NativeEndian.PutUint32(opts[0:], flags)
	if err := syscall.SetsockoptString(fd, solCANISOTP, isotpOpts, string(opts)); err != nil {
		return nil, opErr(fmt.Errorf("CAN_ISOTP_OPTS: %w", err))
	}
	if cfg.ForceTxSTmin != nil {
		b := make([]byte, 4)
		binary.NativeEndian.PutUint32(b, ns)
		if err := syscall.SetsockoptString(fd, solCANISOTP, isotpTxSTmin, string(b)); err != nil {
			return nil, opErr(fmt.Errorf("CAN_ISOTP_TX_STMIN: %w", err))
		}
	}
	const wft = 10 // MaxWait is 0 or 10 (checked above)
	if err := syscall.SetsockoptString(fd, solCANISOTP, isotpRecvFC, string([]byte{cfg.BlockSize, st, byte(wft)})); err != nil {
		return nil, opErr(fmt.Errorf("CAN_ISOTP_RECV_FC: %w", err))
	}
	if cfg.FD {
		txdl := cfg.TxDL
		if txdl == 0 {
			txdl = 64
		}
		var txf byte
		if cfg.BRS {
			txf = 0x01 // CANFD_BRS
		}
		if err := syscall.SetsockoptString(fd, solCANISOTP, isotpLLOpts, string([]byte{canFDMTU, byte(txdl), txf})); err != nil {
			return nil, opErr(fmt.Errorf("%w: CAN_ISOTP_LL_OPTS: %v", canbus.ErrUnsupported, err))
		}
	}
	idOf := func(id canbus.ID) uint32 {
		if cfg.ExtendedIDs {
			return uint32(id) | effFlag
		}
		return uint32(id)
	}
	if e := bindCAN(fd, &sockaddrCAN{family: afCAN, ifindex: ifindex32(ifi.Index), rxID: idOf(cfg.RxID), txID: idOf(cfg.TxID)}); e != 0 {
		return nil, opErr(fmt.Errorf("bind: %w", mapErrno(e)))
	}
	sock, err := dupCloexec(fd)
	if err != nil {
		return nil, opErr(fmt.Errorf("dup: %w", err))
	}
	c := &ISOTPConn{name: ifname, maxMsg: cfg.MaxMessageSize, sock: sock, waitTxDone: waitTxDone,
		wsem: make(chan struct{}, 1), done: make(chan struct{})}
	if c.maxMsg <= 0 {
		c.maxMsg = 4095
	}
	c.file = os.NewFile(uintptr(fd), "isotp:"+ifname)
	ok = true
	if c.rc, err = c.file.SyscallConn(); err != nil {
		_ = c.Close()
		return nil, opErr(err)
	}
	return c, nil
}

// dupCloexec returns a second descriptor of fd, closed on exec.
func dupCloexec(fd int) (int, error) {
	r, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_DUPFD_CLOEXEC, 0)
	if e != 0 {
		return -1, e
	}
	return int(r), nil
}

func (c *ISOTPConn) finish(ctx context.Context, op string, err error) error {
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
	// The kernel's codes (net/can/isotp.c): ECOMM when no flow control
	// came in time (N_Bs), EMSGSIZE for an OVERFLOW flow control,
	// ETIMEDOUT when no consecutive frame came in time (N_Cr), EILSEQ for
	// a wrong sequence number, EBADMSG for an invalid flow control.
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		var kind error
		switch errno {
		case syscall.ECOMM, syscall.ETIMEDOUT:
			kind = isotp.ErrTimeout
		case syscall.EMSGSIZE:
			kind = isotp.ErrOverflow
		case syscall.EILSEQ:
			kind = isotp.ErrSequence
		case syscall.EBADMSG:
			kind = isotp.ErrProtocol
		default:
		}
		if kind != nil {
			return &canbus.OpError{Op: op, Interface: c.name, Err: fmt.Errorf("%w: %v", kind, errno)}
		}
	}
	// After the kernel's codes: an error the kernel reported stays that
	// error when Close runs meanwhile.
	if errors.Is(err, os.ErrClosed) || c.closed.Load() {
		return canbus.ErrClosed
	}
	return &canbus.OpError{Op: op, Interface: c.name, Err: mapErrno(err)}
}

// Send transmits one message and returns when the kernel has finished
// the transfer (or failed it: no flow control, overflow).
//
// The write does not block: the kernel returns from it once the first
// (or single) frame is out and carries on with the transfer. Send then
// waits, under ctx, for the kernel to report the transfer finished, and
// reads the transfer's error. One transfer runs at a time: a Send waits
// for the one before it, until its own ctx ends or Close.
//
// The kernel's N_Bs (1 s) bounds each wait for a flow control, not the
// transfer: a peer that keeps sending WAIT flow controls, or announces a
// long STmin, can extend it. The kernel offers no way to abort a
// transfer except closing the socket, so when ctx ends during the
// transfer, Send closes the connection, which stops the transfer, and
// returns ctx's error; the connection is closed afterwards and later
// calls return [canbus.ErrClosed]. When ctx ends before the write, the
// message is not sent and the connection stays open. Close during a
// transfer stops it, and Send returns [canbus.ErrClosed].
//
// This needs a kernel whose ISO-TP sockets report a transfer in progress
// to poll (Linux 6.3 and later, and the 5.10, 5.15 and 6.1 stable series
// once they received that change; LIMITATIONS.md). With an older kernel
// Send waits for the transfer inside the write, which neither ctx nor
// Close ends: ctx is checked before the write only, and Close waits for
// the transfer.
func (c *ISOTPConn) Send(ctx context.Context, payload []byte) error {
	if c.closed.Load() {
		return canbus.ErrClosed
	}
	if len(payload) == 0 || len(payload) > c.maxMsg {
		return fmt.Errorf("%w: %d bytes", isotp.ErrTooLarge, len(payload))
	}
	if err := ctx.Err(); err != nil {
		return err // nothing is sent with a context that has ended
	}
	select {
	case c.wsem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return canbus.ErrClosed
	}
	defer func() { <-c.wsem }()
	if c.waitTxDone {
		return c.sendWaitTxDone(ctx, payload)
	}
	// The deadline ends the write below without sending when ctx has
	// ended meanwhile.
	stop := watch(ctx, c.file, false)
	defer stop()
	// The kernel refuses the write (EAGAIN) while its transmitter is
	// busy; the socket polls writable once it is idle.
	var werr error
	err := c.rc.Write(func(fd uintptr) bool {
		_, werr = syscall.Write(int(fd), payload)
		return werr != syscall.EAGAIN
	})
	if err == nil && werr == nil {
		// The kernel clears POLLOUT while a transfer is in progress
		// (isotp_poll) and wakes the poller when it ends.
		err = c.rc.Write(func(fd uintptr) bool { return pollOut(fd) })
		if errors.Is(err, os.ErrDeadlineExceeded) && !c.idle() {
			_ = c.Close() // the only way to end the kernel's transfer
		} else {
			// The transfer ended (as ctx did, perhaps), or Close ran.
			// Its error, from the kernel: sock_error, as
			// CAN_ISOTP_WAIT_TX_DONE reports it.
			var soErr int
			err = c.rc.Control(func(fd uintptr) {
				soErr, werr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_ERROR)
			})
			if werr == nil && soErr > 0 {
				werr = syscall.Errno(soErr)
			}
		}
	}
	if err == nil {
		err = werr
	}
	if err != nil {
		return c.finish(ctx, "send", err)
	}
	return nil
}

// sendWaitTxDone is Send with CAN_ISOTP_WAIT_TX_DONE: the kernel waits
// for the transfer inside sendmsg, interruptibly. Go's runtime signals
// its threads (SIGURG, for preemption); a signal would abort the
// transfer, and the syscall, restarted (SA_RESTART), would send the
// whole message again. The write therefore runs on a locked OS thread
// with all signals blocked; they are delivered when it returns.
func (c *ISOTPConn) sendWaitTxDone(ctx context.Context, payload []byte) error {
	// One transfer at a time (wsem), each finished when its write
	// returns: the kernel is idle here, and does not answer EAGAIN.
	var werr error
	err := c.rc.Control(func(fd uintptr) { werr = writeSignalsBlocked(int(fd), payload) })
	if err == nil {
		err = werr
	}
	if err != nil {
		return c.finish(ctx, "send", err)
	}
	return nil
}

// writeSignalsBlocked writes on the current, locked OS thread with every
// signal blocked (rt_sigprocmask), so the kernel's interruptible wait for
// the transfer is not interrupted.
func writeSignalsBlocked(fd int, p []byte) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	all := ^uint64(0)
	var old uint64
	const sigBlock, sigSetMask = 0, 2
	if e := rtSigprocmask(sigBlock, &all, &old); e != 0 {
		return e
	}
	_, err := syscall.Write(fd, p)
	_ = rtSigprocmask(sigSetMask, &old, nil)
	return err
}

// idle reports whether the kernel has no transfer in progress.
func (c *ISOTPConn) idle() bool {
	idle := false
	_ = c.rc.Control(func(fd uintptr) { idle = pollOut(fd) })
	return idle
}

// Receive returns the next message or a reception error reported by the
// kernel.
func (c *ISOTPConn) Receive(ctx context.Context) ([]byte, error) {
	if c.closed.Load() {
		return nil, canbus.ErrClosed
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	stop := watch(ctx, c.file, true)
	defer stop()
	// maxMsg is at most maxKernelISOTPMessage (DialISOTP): the extra
	// byte detects a longer message without overflowing on 32-bit.
	buf := make([]byte, c.maxMsg+1)
	var n int
	var rerr error
	err := c.rc.Read(func(fd uintptr) bool {
		n, rerr = syscall.Read(int(fd), buf)
		return rerr != syscall.EAGAIN
	})
	if err == nil {
		err = rerr
	}
	if err != nil {
		return nil, c.finish(ctx, "receive", err)
	}
	if n > c.maxMsg {
		return nil, fmt.Errorf("%w: more than %d bytes", isotp.ErrTooLarge, c.maxMsg)
	}
	return buf[:n], nil
}

// Close closes the socket and stops a transfer in progress: a blocked
// Receive or Send returns [canbus.ErrClosed]. Close is safe to call more
// than once and from multiple goroutines; the first call reports the
// error, a later one returns nil once the first has finished.
//
// The kernel releases an ISO-TP socket only once its transfer is idle,
// unless a signal interrupts the wait. Close therefore closes the socket
// on a locked OS thread and, while that close is pending, sends SIGURG
// to that thread (the signal Go's runtime uses for preemption and
// tolerates at any time); a program that subscribes to SIGURG with
// os/signal sees these signals.
func (c *ISOTPConn) Close() error {
	first := false
	c.closeOnce.Do(func() {
		first = true
		c.closed.Store(true)
		close(c.done)
		// file shares the socket with c.sock: closing it ends the
		// pending Receive and Send calls without releasing the socket.
		err := c.file.Close()
		if e := closeSocket(c.sock); err == nil {
			err = e
		}
		c.closeErr = err
	})
	if !first {
		return nil
	}
	return c.closeErr
}

// closeSocket closes fd, the last descriptor of an ISO-TP socket, on a
// locked OS thread, and interrupts the kernel's wait for a transfer in
// progress (isotp_release) with SIGURG to that thread until the close
// returns. The signal goes to this thread only, and only while it is
// locked to this goroutine.
func closeSocket(fd int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pid, tid := syscall.Getpid(), syscall.Gettid()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTimer(closeSignalAfter)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = syscall.Tgkill(pid, tid, syscall.SIGURG)
				t.Reset(closeSignalEvery)
			}
		}
	}()
	err := syscall.Close(fd)
	close(done)
	<-stopped // no signal is sent after the thread is unlocked
	return err
}

var _ isotp.Endpoint = (*ISOTPConn)(nil)
