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
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

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

// ISOTPConn is a kernel ISO-TP socket (CAN_ISOTP, Linux ≥ 5.10 or the
// can-isotp module). It implements [isotp.Endpoint].
type ISOTPConn struct {
	name   string
	file   *os.File
	rc     syscall.RawConn
	maxMsg int
	rmu    sync.Mutex
	wmu    sync.Mutex
	closed atomic.Bool
}

// DialISOTP opens a kernel ISO-TP socket on the interface with the same
// configuration the userspace isotp.Conn takes. The kernel does not
// support Config.SendFlowWait, MaxWait other than its default, or the
// N_Bs/N_Cr values (it uses 1 s); a Config asking for them is rejected.
func DialISOTP(ifname string, cfg isotp.Config) (*ISOTPConn, error) {
	opErr := func(err error) error { return &canbus.OpError{Op: "open isotp", Interface: ifname, Err: err} }
	if cfg.SendFlowWait != 0 {
		return nil, opErr(fmt.Errorf("%w: the kernel cannot send WAIT flow controls", canbus.ErrUnsupported))
	}
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, opErr(fmt.Errorf("%w: %v", canbus.ErrInterfaceDown, err))
	}
	st, err := isotp.EncodeSTmin(cfg.STmin)
	if err != nil {
		return nil, opErr(err)
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
	// CAN_ISOTP_WAIT_TX_DONE: sendmsg returns when the transfer is done
	// and reports its error. The kernel waits interruptibly, so Send
	// blocks signals on its thread for the duration (see Send).
	flags := uint32(isotpWaitTxDone)
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
		ns := uint32(cfg.ForceTxSTmin.Nanoseconds())
		b := make([]byte, 4)
		binary.NativeEndian.PutUint32(b, ns)
		if err := syscall.SetsockoptString(fd, solCANISOTP, isotpTxSTmin, string(b)); err != nil {
			return nil, opErr(fmt.Errorf("CAN_ISOTP_TX_STMIN: %w", err))
		}
	}
	wft := cfg.MaxWait
	if wft <= 0 || wft > 255 {
		wft = 10
	}
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
	sa := sockaddrCAN{family: afCAN, ifindex: int32(ifi.Index), rxID: idOf(cfg.RxID), txID: idOf(cfg.TxID)}
	if _, _, e := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa)); e != 0 {
		return nil, opErr(fmt.Errorf("bind: %w", mapErrno(e)))
	}
	c := &ISOTPConn{name: ifname, maxMsg: cfg.MaxMessageSize}
	if c.maxMsg <= 0 {
		c.maxMsg = 4095
	}
	c.file = os.NewFile(uintptr(fd), "isotp:"+ifname)
	if c.rc, err = c.file.SyscallConn(); err != nil {
		_ = c.file.Close()
		ok = true
		return nil, opErr(err)
	}
	ok = true
	return c, nil
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
	if errors.Is(err, os.ErrClosed) || c.closed.Load() {
		return canbus.ErrClosed
	}
	// The kernel's codes (net/can/isotp.c): ECOMM when no flow control
	// came in time (N_Bs), EMSGSIZE for an OVERFLOW flow control,
	// ETIMEDOUT when no consecutive frame came in time (N_Cr), EILSEQ for
	// a wrong sequence number, EBADMSG for an invalid flow control.
	var errno syscall.Errno
	if errors.As(err, &errno) {
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
	return &canbus.OpError{Op: op, Interface: c.name, Err: mapErrno(err)}
}

// Send transmits one message and returns when the kernel has finished
// the transfer (or failed it: no flow control, overflow).
//
// The kernel waits for the transfer inside sendmsg, interruptibly. Go's
// runtime signals its threads (SIGURG, for preemption); a signal would
// abort the transfer, and the syscall, restarted (SA_RESTART), would send
// the whole message again. Send therefore runs the write on a locked OS
// thread with all signals blocked; they are delivered when it returns.
// The write cannot be cancelled once started: ctx is checked before, and
// the kernel's own timeouts (N_Bs, 1 s per flow control) bound it.
func (c *ISOTPConn) Send(ctx context.Context, payload []byte) error {
	if c.closed.Load() {
		return canbus.ErrClosed
	}
	if len(payload) == 0 || len(payload) > c.maxMsg {
		return fmt.Errorf("%w: %d bytes", isotp.ErrTooLarge, len(payload))
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	backoff := 100 * time.Microsecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var werr error
		if err := c.rc.Control(func(fd uintptr) { werr = writeSignalsBlocked(int(fd), payload) }); err != nil {
			return c.finish(ctx, "send", err)
		}
		if werr == syscall.EAGAIN { // the transmitter is busy (another writer)
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
		if werr != nil {
			return c.finish(ctx, "send", werr)
		}
		return nil
	}
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
	if _, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, sigBlock, uintptr(unsafe.Pointer(&all)), uintptr(unsafe.Pointer(&old)), 8, 0, 0); e != 0 {
		return e
	}
	_, err := syscall.Write(fd, p)
	_, _, _ = syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, sigSetMask, uintptr(unsafe.Pointer(&old)), 0, 8, 0, 0)
	return err
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

// Close closes the socket.
func (c *ISOTPConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.file.Close()
}

var _ isotp.Endpoint = (*ISOTPConn)(nil)
