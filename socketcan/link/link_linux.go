// SPDX-License-Identifier: MIT

//go:build linux

package link

import (
	"fmt"
	"net"
	"sync/atomic"
	"syscall"

	"github.com/otfabric/go-canbus"
)

var seq atomic.Uint32

// request sends one netlink request and collects the reply messages up to
// the ACK, NLMSG_DONE or an error.
func request(msg []byte, dump bool) ([]nlMsg, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("link: netlink socket: %w", err)
	}
	defer syscall.Close(fd)
	// Every request asks for an ACK or a dump end; the timeout only guards
	// against a kernel that never answers.
	tv := syscall.Timeval{Sec: 5}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return nil, fmt.Errorf("link: netlink timeout: %w", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("link: netlink bind: %w", err)
	}
	s := ne.Uint32(msg[8:])
	if err := syscall.Sendto(fd, msg, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("link: netlink send: %w", err)
	}
	var out []nlMsg
	buf := make([]byte, 1<<16)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, fmt.Errorf("link: netlink receive: %w", err)
		}
		msgs, err := parseMessages(append([]byte(nil), buf[:n]...))
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			if m.seq != s {
				continue
			}
			switch m.typ {
			case nlmsgDone:
				return out, nil
			case nlmsgError:
				if e := errnoOf(m.body); e != 0 {
					return out, mapErrno(syscall.Errno(-e))
				}
				if !dump {
					return out, nil
				}
			default:
				out = append(out, m)
			}
		}
	}
}

func mapErrno(e syscall.Errno) error {
	switch e {
	case syscall.EPERM, syscall.EACCES:
		return fmt.Errorf("link: %w (CAP_NET_ADMIN needed)", e)
	case syscall.EOPNOTSUPP, syscall.EINVAL:
		return fmt.Errorf("%w: %v", canbus.ErrUnsupported, e)
	case syscall.ENODEV:
		return fmt.Errorf("%w: %v", canbus.ErrInterfaceDown, e)
	}
	return fmt.Errorf("link: %w", e)
}

// List returns the CAN interfaces (link type ARPHRD_CAN).
func List() ([]Interface, error) {
	msgs, err := request(message(rtmGetLink, nlmFDump, seq.Add(1), ifinfomsg(0, 0, 0)), true)
	if err != nil {
		return nil, err
	}
	var out []Interface
	for _, m := range msgs {
		if m.typ != rtmNewLink {
			continue
		}
		if it, ok := parseLink(m.body); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func index(name string) (int32, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", canbus.ErrInterfaceDown, name, err)
	}
	return int32(ifi.Index), nil
}

// Get returns one CAN interface.
func Get(name string) (Interface, error) {
	idx, err := index(name)
	if err != nil {
		return Interface{}, err
	}
	msgs, err := request(message(rtmGetLink, nlmFAck, seq.Add(1), ifinfomsg(idx, 0, 0)), false)
	if err != nil {
		return Interface{}, err
	}
	for _, m := range msgs {
		if it, ok := parseLink(m.body); ok && m.typ == rtmNewLink {
			return it, nil
		}
	}
	return Interface{}, fmt.Errorf("%w: %s is not a CAN interface", canbus.ErrUnsupported, name)
}

func change(name string, body func(idx int32) []byte) error {
	idx, err := index(name)
	if err != nil {
		return err
	}
	_, err = request(message(rtmNewLink, nlmFAck, seq.Add(1), body(idx)), false)
	return err
}

// SetUp brings the interface up.
func SetUp(name string) error {
	return change(name, func(idx int32) []byte { return ifinfomsg(idx, iffUp, iffUp) })
}

// SetDown brings the interface down.
func SetDown(name string) error {
	return change(name, func(idx int32) []byte { return ifinfomsg(idx, 0, iffUp) })
}

// SetMTU sets the MTU: 16 for classical CAN, 72 for CAN FD. Controllers
// accept 72 only in CAN FD mode.
func SetMTU(name string, mtu int) error {
	return change(name, func(idx int32) []byte {
		return append(ifinfomsg(idx, 0, 0), attr(iflaMTU, u32(uint32(mtu)))...)
	})
}

// Configure applies bit timing, control modes and restart-ms to a CAN
// controller interface. The interface must be down. vcan and vxcan
// interfaces have no bit timing: the kernel reports EOPNOTSUPP, returned
// as [canbus.ErrUnsupported].
func Configure(name string, cfg Config) error {
	if cfg.Empty() {
		return nil
	}
	if cfg.Set&cfg.Clear != 0 {
		return fmt.Errorf("%w: modes %s both set and cleared", canbus.ErrInvalidOption, cfg.Set&cfg.Clear)
	}
	return change(name, func(idx int32) []byte { return configureBody(idx, cfg) })
}

// Restart restarts a controller that is bus-off (IFLA_CAN_RESTART).
func Restart(name string) error {
	return change(name, func(idx int32) []byte {
		return append(ifinfomsg(idx, 0, 0), nested(iflaLinkinfo, attr(iflaInfoKind, cstr("can")),
			nested(iflaInfoData, attr(iflaCANRestart, u32(1))))...)
	})
}

// AddVCAN creates a vcan interface with the CAN FD MTU (72) when fd is
// set, else the classical MTU (16). (The kernel's own default differs
// between versions: 72, or 2060 since CAN XL.)
func AddVCAN(name string, fd bool) error {
	mtu := uint32(16)
	if fd {
		mtu = 72
	}
	attrs := append(ifinfomsg(0, 0, 0), attr(iflaIfname, cstr(name))...)
	attrs = append(attrs, attr(iflaMTU, u32(mtu))...)
	attrs = append(attrs, nested(iflaLinkinfo, attr(iflaInfoKind, cstr("vcan")))...)
	_, err := request(message(rtmNewLink, nlmFAck|nlmFCreate|nlmFExcl, seq.Add(1), attrs), false)
	return err
}

// AddVXCAN creates a vxcan tunnel: two interfaces, name and peer, where a
// frame sent on one is received on the other. Moving the peer into another
// network namespace connects two namespaces (containers).
func AddVXCAN(name, peer string) error {
	peerInfo := append(ifinfomsg(0, 0, 0), attr(iflaIfname, cstr(peer))...)
	attrs := append(ifinfomsg(0, 0, 0), attr(iflaIfname, cstr(name))...)
	attrs = append(attrs, nested(iflaLinkinfo, attr(iflaInfoKind, cstr("vxcan")),
		nested(iflaInfoData, nested(1 /* VXCAN_INFO_PEER */, peerInfo)))...)
	_, err := request(message(rtmNewLink, nlmFAck|nlmFCreate|nlmFExcl, seq.Add(1), attrs), false)
	return err
}

// Delete deletes an interface (for example one made by AddVCAN).
func Delete(name string) error {
	idx, err := index(name)
	if err != nil {
		return err
	}
	_, err = request(message(rtmDelLink, nlmFAck, seq.Add(1), ifinfomsg(idx, 0, 0)), false)
	return err
}
