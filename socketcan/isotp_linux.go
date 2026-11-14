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
// support Config.SendFlowWait, MaxWait other than its default (10), or
// N_Bs/N_Cr values other than its fixed 1 s; a Config asking for them is
// rejected with [canbus.ErrUnsupported]. MaxMessageSize (default 4095)
// is at most 1025 KiB, the kernel's own bound; a larger one is rejected
// with [canbus.ErrInvalidOption].
func DialISOTP(ifname string, cfg isotp.Config) (*ISOTPConn, error) {
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
// The write cannot be cancelled once started: ctx is checked before
// only. The kernel's N_Bs (1 s) bounds each wait for a flow control, not
// the transfer: a peer that keeps sending WAIT flow controls, or
// announces a long STmin, can extend it, and neither ctx nor Close ends
// it meanwhile.
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
	if e := rtSigprocmask(sigBlock, &all, &old); e != 0 {
		return e
	}
	_, err := syscall.Write(fd, p)
	_ = rtSigprocmask(sigSetMask, &old, nil)
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

// Close closes the socket.
func (c *ISOTPConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.file.Close()
}

var _ isotp.Endpoint = (*ISOTPConn)(nil)
