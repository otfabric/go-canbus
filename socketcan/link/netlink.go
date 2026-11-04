// SPDX-License-Identifier: MIT

package link

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"syscall"

	"github.com/otfabric/go-canbus"
)

// Netlink and rtnetlink constants (linux/netlink.h, linux/rtnetlink.h,
// linux/if_link.h, linux/can/netlink.h). Declared here so that the message
// encoding is portable and testable everywhere.
const (
	nlmsgHdrLen  = 16
	ifinfoLen    = 16
	nlmsgError   = 2
	nlmsgDone    = 3
	rtmNewLink   = 16
	rtmDelLink   = 17
	rtmGetLink   = 18
	nlmFRequest  = 0x1
	nlmFMulti    = 0x2
	nlmFAck      = 0x4
	nlmFDump     = 0x300
	nlmFCreate   = 0x400
	nlmFExcl     = 0x200
	nlaFNested   = 0x8000
	iffUp        = 0x1
	arphrdCAN    = 280
	iflaIfname   = 3
	iflaMTU      = 4
	iflaTxQLen   = 13
	iflaOperst   = 16
	iflaLinkinfo = 18
	iflaStats64  = 23

	iflaInfoKind   = 1
	iflaInfoData   = 2
	iflaInfoXstats = 4

	iflaCANBittiming     = 1
	iflaCANClock         = 3
	iflaCANState         = 4
	iflaCANCtrlmode      = 5
	iflaCANRestartMs     = 6
	iflaCANRestart       = 7
	iflaCANBerrCounter   = 8
	iflaCANDataBittiming = 9
)

var ne = binary.NativeEndian

func align4(n int) int { return (n + 3) &^ 3 }

// maxAttrData is the largest route attribute payload: nla_len is 16 bits
// and includes the 4-byte header.
const maxAttrData = math.MaxUint16 - 4

// attr builds one route attribute. Callers bound data (interface names
// by checkName, the rest is fixed-size); a longer payload would not be
// representable and is a programming error.
func attr(typ uint16, data []byte) []byte {
	l := 4 + len(data)
	if l > math.MaxUint16 {
		panic(fmt.Sprintf("link: route attribute of %d bytes exceeds %d", len(data), maxAttrData))
	}
	b := make([]byte, align4(l))
	ne.PutUint16(b[0:], uint16(l))
	ne.PutUint16(b[2:], typ)
	copy(b[4:], data)
	return b
}

func nested(typ uint16, children ...[]byte) []byte {
	var data []byte
	for _, c := range children {
		data = append(data, c...)
	}
	return attr(typ|nlaFNested, data)
}

func u32(v uint32) []byte { b := make([]byte, 4); ne.PutUint32(b, v); return b }

func cstr(s string) []byte { return append([]byte(s), 0) }

// ifinfomsg encodes struct ifinfomsg.
func ifinfomsg(index int32, flags, change uint32) []byte {
	b := make([]byte, ifinfoLen)
	// family 0, pad, type 0
	ne.PutUint32(b[4:], uint32(int64(index)&0xFFFFFFFF)) // int ifi_index, same bits
	ne.PutUint32(b[8:], flags)
	ne.PutUint32(b[12:], change)
	return b
}

// message frames a netlink request.
func message(typ, flags uint16, seq uint32, body ...[]byte) []byte {
	n := nlmsgHdrLen
	for _, p := range body {
		n += len(p)
	}
	if int64(n) > math.MaxUint32 {
		panic(fmt.Sprintf("link: netlink message of %d bytes", n))
	}
	b := make([]byte, nlmsgHdrLen, n)
	ne.PutUint32(b[0:], uint32(int64(n)&0xFFFFFFFF)) // checked above
	ne.PutUint16(b[4:], typ)
	ne.PutUint16(b[6:], flags|nlmFRequest)
	ne.PutUint32(b[8:], seq)
	for _, p := range body {
		b = append(b, p...)
	}
	return b
}

type rawAttr struct {
	typ  uint16
	data []byte
}

// parseAttrs splits a buffer of route attributes. Malformed trailing
// bytes end the list.
func parseAttrs(b []byte) []rawAttr {
	var out []rawAttr
	for len(b) >= 4 {
		l := int(ne.Uint16(b[0:]))
		if l < 4 || l > len(b) {
			break
		}
		out = append(out, rawAttr{typ: ne.Uint16(b[2:]) &^ nlaFNested, data: b[4:l]})
		if align4(l) >= len(b) {
			break
		}
		b = b[align4(l):]
	}
	return out
}

type nlMsg struct {
	typ  uint16
	seq  uint32
	body []byte
}

// parseMessages splits a netlink receive buffer.
func parseMessages(b []byte) ([]nlMsg, error) {
	var out []nlMsg
	for len(b) >= nlmsgHdrLen {
		l := int(ne.Uint32(b[0:]))
		if l < nlmsgHdrLen || l > len(b) {
			return out, errors.New("link: truncated netlink message")
		}
		out = append(out, nlMsg{typ: ne.Uint16(b[4:]), seq: ne.Uint32(b[8:]), body: b[nlmsgHdrLen:l]})
		if align4(l) >= len(b) {
			break
		}
		b = b[align4(l):]
	}
	return out, nil
}

// errnoOf decodes the error field of an NLMSG_ERROR body: 0 for an ACK,
// else the errno the kernel reports as a negative int. A body too short
// to hold it, or a positive code, is a malformed reply.
func errnoOf(body []byte) (syscall.Errno, error) {
	if len(body) < 4 {
		return 0, errors.New("link: truncated netlink error message")
	}
	v := ne.Uint32(body)
	switch {
	case v == 0:
		return 0, nil
	case v < 1<<31:
		return 0, fmt.Errorf("link: netlink error code %d is not negative", v)
	}
	return syscall.Errno(-v), nil // two's complement: -v is the magnitude
}

// ifNameMax is IFNAMSIZ less the terminating NUL.
const ifNameMax = 15

// checkName rejects interface names the kernel cannot hold.
func checkName(name string) error {
	if name == "" || len(name) > ifNameMax {
		return fmt.Errorf("%w: interface name %q (1 to %d bytes)", canbus.ErrInvalidOption, name, ifNameMax)
	}
	return nil
}

// checkMTU converts an MTU for IFLA_MTU (a u32).
func checkMTU(mtu int) (uint32, error) {
	if mtu <= 0 {
		return 0, fmt.Errorf("%w: MTU %d", canbus.ErrInvalidOption, mtu)
	}
	if int64(mtu) > math.MaxUint32 {
		return 0, fmt.Errorf("%w: MTU %d", canbus.ErrInvalidOption, mtu)
	}
	return uint32(int64(mtu) & 0xFFFFFFFF), nil // checked above
}

// ifindex32 converts an interface index (a C int in the kernel).
func ifindex32(idx int) (int32, error) {
	if idx < 0 {
		return 0, fmt.Errorf("%w: interface index %d", canbus.ErrInvalidOption, idx)
	}
	if idx > math.MaxInt32 {
		return 0, fmt.Errorf("%w: interface index %d", canbus.ErrInvalidOption, idx)
	}
	return int32(idx), nil
}

func readU32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return ne.Uint32(b)
}

// parseLink decodes an RTM_NEWLINK body (ifinfomsg + attributes). It
// reports false for interfaces that are not CAN interfaces.
func parseLink(body []byte) (Interface, bool) {
	var it Interface
	if len(body) < ifinfoLen {
		return it, false
	}
	if ne.Uint16(body[2:]) != arphrdCAN {
		return it, false
	}
	idx := ne.Uint32(body[4:])
	if idx > math.MaxInt32 {
		return it, false // a negative ifindex is not a real interface
	}
	it.Index = int(idx)
	it.Up = ne.Uint32(body[8:])&iffUp != 0
	for _, a := range parseAttrs(body[ifinfoLen:]) {
		switch a.typ {
		case iflaIfname:
			it.Name = cstring(a.data)
		case iflaMTU:
			it.MTU = int(readU32(a.data))
		case iflaTxQLen:
			it.TxQueueLen = int(readU32(a.data))
		case iflaOperst:
			if len(a.data) > 0 {
				it.OperState = operState(a.data[0])
			}
		case iflaStats64:
			if len(a.data) >= 64 {
				d := a.data
				it.Stats = Stats{ne.Uint64(d[0:]), ne.Uint64(d[8:]), ne.Uint64(d[16:]), ne.Uint64(d[24:]),
					ne.Uint64(d[32:]), ne.Uint64(d[40:]), ne.Uint64(d[48:]), ne.Uint64(d[56:])}
			}
		case iflaLinkinfo:
			parseLinkinfo(a.data, &it)
		}
	}
	return it, true
}

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func operState(v byte) string {
	names := []string{"UNKNOWN", "NOTPRESENT", "DOWN", "LOWERLAYERDOWN", "TESTING", "DORMANT", "UP"}
	if int(v) < len(names) {
		return names[v]
	}
	return fmt.Sprintf("STATE-%d", v)
}

func parseLinkinfo(b []byte, it *Interface) {
	attrs := parseAttrs(b)
	for _, a := range attrs {
		if a.typ == iflaInfoKind {
			it.Kind = cstring(a.data)
		}
	}
	for _, a := range attrs {
		switch a.typ {
		case iflaInfoData:
			if it.Kind == "can" {
				parseCANData(a.data, it)
			}
		case iflaInfoXstats:
			if it.Kind == "can" && len(a.data) >= 24 {
				d := a.data
				it.DeviceStats = &DeviceStats{ne.Uint32(d[0:]), ne.Uint32(d[4:]), ne.Uint32(d[8:]), ne.Uint32(d[12:]), ne.Uint32(d[16:]), ne.Uint32(d[20:])}
			}
		}
	}
}

func parseBitTiming(d []byte) *BitTiming {
	if len(d) < 32 {
		return nil
	}
	return &BitTiming{ne.Uint32(d[0:]), ne.Uint32(d[4:]), ne.Uint32(d[8:]), ne.Uint32(d[12:]),
		ne.Uint32(d[16:]), ne.Uint32(d[20:]), ne.Uint32(d[24:]), ne.Uint32(d[28:])}
}

func parseCANData(b []byte, it *Interface) {
	for _, a := range parseAttrs(b) {
		switch a.typ {
		case iflaCANBittiming:
			it.BitTiming = parseBitTiming(a.data)
		case iflaCANDataBittiming:
			it.DataBitTiming = parseBitTiming(a.data)
		case iflaCANClock:
			it.ClockHz = readU32(a.data)
		case iflaCANState:
			if len(a.data) >= 4 {
				s := ControllerState(readU32(a.data))
				it.State = &s
			}
		case iflaCANCtrlmode:
			if len(a.data) >= 8 {
				it.CtrlModeSupported = CtrlMode(ne.Uint32(a.data[0:]))
				it.CtrlMode = CtrlMode(ne.Uint32(a.data[4:]))
			}
		case iflaCANRestartMs:
			if len(a.data) >= 4 {
				v := readU32(a.data)
				it.RestartMs = &v
			}
		case iflaCANBerrCounter:
			if len(a.data) >= 4 {
				it.ErrorCounters = &ErrorCounters{TX: ne.Uint16(a.data[0:]), RX: ne.Uint16(a.data[2:])}
			}
		}
	}
}

// configureBody builds the attributes of an RTM_NEWLINK that applies cfg
// to a kind "can" interface.
func configureBody(index int32, cfg Config) []byte {
	var data [][]byte
	if cfg.Bitrate != 0 {
		bt := make([]byte, 32)
		ne.PutUint32(bt[0:], cfg.Bitrate)
		ne.PutUint32(bt[4:], cfg.SamplePoint)
		data = append(data, attr(iflaCANBittiming, bt))
	}
	if cfg.DataBitrate != 0 {
		bt := make([]byte, 32)
		ne.PutUint32(bt[0:], cfg.DataBitrate)
		ne.PutUint32(bt[4:], cfg.DataSamplePoint)
		data = append(data, attr(iflaCANDataBittiming, bt))
	}
	if cfg.Set != 0 || cfg.Clear != 0 {
		cm := make([]byte, 8)
		ne.PutUint32(cm[0:], uint32(cfg.Set|cfg.Clear))
		ne.PutUint32(cm[4:], uint32(cfg.Set))
		data = append(data, attr(iflaCANCtrlmode, cm))
	}
	if cfg.RestartMs != nil {
		data = append(data, attr(iflaCANRestartMs, u32(*cfg.RestartMs)))
	}
	return append(ifinfomsg(index, 0, 0),
		nested(iflaLinkinfo, attr(iflaInfoKind, cstr("can")), nested(iflaInfoData, data...))...)
}
