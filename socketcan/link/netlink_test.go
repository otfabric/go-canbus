// SPDX-License-Identifier: MIT

package link

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/otfabric/go-canbus"
)

// fakeLink builds an RTM_NEWLINK body as the kernel sends it for a CAN
// controller.
func fakeLink() []byte {
	info := make([]byte, ifinfoLen)
	ne.PutUint16(info[2:], arphrdCAN)
	ne.PutUint32(info[4:], 7)
	ne.PutUint32(info[8:], iffUp)
	bt := make([]byte, 32)
	ne.PutUint32(bt[0:], 500000)
	ne.PutUint32(bt[4:], 875)
	dbt := make([]byte, 32)
	ne.PutUint32(dbt[0:], 2000000)
	cm := make([]byte, 8)
	ne.PutUint32(cm[0:], uint32(ModeFD|ModeLoopback))
	ne.PutUint32(cm[4:], uint32(ModeFD))
	berr := make([]byte, 4)
	ne.PutUint16(berr[0:], 3)
	ne.PutUint16(berr[2:], 130)
	stats := make([]byte, 64)
	ne.PutUint64(stats[0:], 11)
	ne.PutUint64(stats[8:], 22)
	xs := make([]byte, 24)
	ne.PutUint32(xs[12:], 2)
	body := append(info, attr(iflaIfname, cstr("can0"))...)
	body = append(body, attr(iflaMTU, u32(72))...)
	body = append(body, attr(iflaTxQLen, u32(10))...)
	body = append(body, attr(iflaOperst, []byte{6})...)
	body = append(body, attr(iflaStats64, stats)...)
	body = append(body, nested(iflaLinkinfo,
		attr(iflaInfoKind, cstr("can")),
		nested(iflaInfoData,
			attr(iflaCANBittiming, bt), attr(iflaCANDataBittiming, dbt), attr(iflaCANClock, u32(80000000)),
			attr(iflaCANState, u32(uint32(StateErrorPassive))), attr(iflaCANCtrlmode, cm),
			attr(iflaCANRestartMs, u32(100)), attr(iflaCANBerrCounter, berr)),
		attr(iflaInfoXstats, xs))...)
	return body
}

func TestParseLink(t *testing.T) {
	it, ok := parseLink(fakeLink())
	if !ok {
		t.Fatal("not parsed")
	}
	if it.Name != "can0" || it.Index != 7 || !it.Up || !it.FD() || it.Virtual() || it.Kind != "can" || it.OperState != "UP" || it.TxQueueLen != 10 {
		t.Fatalf("%+v", it)
	}
	if it.BitTiming.Bitrate != 500000 || it.BitTiming.SamplePoint != 875 || it.DataBitTiming.Bitrate != 2000000 || it.ClockHz != 80000000 {
		t.Fatalf("timing %+v %+v", it.BitTiming, it.DataBitTiming)
	}
	if *it.State != StateErrorPassive || it.CtrlMode != ModeFD || it.CtrlModeSupported != ModeFD|ModeLoopback || *it.RestartMs != 100 {
		t.Fatalf("state %v mode %v", *it.State, it.CtrlMode)
	}
	if it.ErrorCounters.RX != 130 || it.Stats.RxPackets != 11 || it.Stats.TxPackets != 22 || it.DeviceStats.BusOff != 2 {
		t.Fatalf("counters %+v %+v %+v", it.ErrorCounters, it.Stats, it.DeviceStats)
	}
	eth := make([]byte, ifinfoLen)
	ne.PutUint16(eth[2:], 1) // ARPHRD_ETHER
	if _, ok := parseLink(eth); ok {
		t.Fatal("ethernet parsed as CAN")
	}
	if _, ok := parseLink([]byte{1}); ok {
		t.Fatal("short body")
	}
}

func TestMessages(t *testing.T) {
	m := message(rtmGetLink, nlmFDump, 42, ifinfomsg(3, 0, 0))
	if len(m) != nlmsgHdrLen+ifinfoLen || ne.Uint32(m) != uint32(len(m)) || ne.Uint16(m[6:]) != nlmFDump|nlmFRequest {
		t.Fatalf("% x", m)
	}
	two := append(append([]byte{}, m...), message(nlmsgDone, 0, 42)...)
	msgs, err := parseMessages(two)
	if err != nil || len(msgs) != 2 || msgs[0].seq != 42 || msgs[1].typ != nlmsgDone {
		t.Fatal(msgs, err)
	}
	if _, err := parseMessages(m[:20]); err == nil {
		t.Fatal("truncated accepted")
	}
	if e, err := errnoOf(u32(0)); e != 0 || err != nil {
		t.Fatal("ACK:", e, err)
	}
	if e, err := errnoOf(u32(0xFFFFFFFF)); e != syscall.Errno(1) || err != nil {
		t.Fatal("-1:", e, err)
	}
	if e, err := errnoOf(u32(0x80000000)); e != syscall.Errno(1<<31) || err != nil {
		t.Fatal("INT_MIN:", e, err)
	}
	// Malformed replies used to read as -1 (EPERM, "CAP_NET_ADMIN needed").
	if _, err := errnoOf([]byte{1}); err == nil {
		t.Fatal("short error body accepted")
	}
	if _, err := errnoOf(u32(5)); err == nil {
		t.Fatal("positive error code accepted")
	}
}

func TestConfigureBody(t *testing.T) {
	r := uint32(50)
	b := configureBody(4, Config{Bitrate: 500000, SamplePoint: 875, DataBitrate: 2000000, Set: ModeFD, Clear: ModeListenOnly, RestartMs: &r})
	// Decode it as the kernel would and check every field.
	attrs := parseAttrs(b[ifinfoLen:])
	if len(attrs) != 1 || attrs[0].typ != iflaLinkinfo {
		t.Fatal(attrs)
	}
	var it Interface
	parseLinkinfo(attrs[0].data, &it)
	if it.Kind != "can" || it.BitTiming.Bitrate != 500000 || it.BitTiming.SamplePoint != 875 || it.DataBitTiming.Bitrate != 2000000 ||
		it.CtrlModeSupported != ModeFD|ModeListenOnly || it.CtrlMode != ModeFD || *it.RestartMs != 50 {
		t.Fatalf("%+v", it)
	}
	if !(&Config{}).Empty() || (&Config{Set: ModeFD}).Empty() {
		t.Fatal("Empty")
	}
}

func TestNames(t *testing.T) {
	if StateBusOff.String() != "BUS-OFF" || ControllerState(9).String() != "STATE-9" {
		t.Fatal("state names")
	}
	if (ModeFD|ModeBerrReporting).String() != "BERR-REPORTING,FD" || CtrlMode(0x1000).String() != "0x1000" {
		t.Fatal((ModeFD | ModeBerrReporting).String())
	}
	if operState(2) != "DOWN" || operState(9) != "STATE-9" || cstring([]byte("ab")) != "ab" {
		t.Fatal("operState")
	}
}

func TestBounds(t *testing.T) {
	for _, n := range []string{"", strings.Repeat("x", ifNameMax+1), strings.Repeat("x", 70000)} {
		if err := checkName(n); !errors.Is(err, canbus.ErrInvalidOption) {
			t.Errorf("name of %d bytes: %v", len(n), err)
		}
	}
	if err := checkName("vcan0"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []int{0, -1, math.MinInt32} {
		if _, err := checkMTU(m); !errors.Is(err, canbus.ErrInvalidOption) {
			t.Errorf("MTU %d: %v", m, err)
		}
	}
	if strconv.IntSize == 64 {
		big := int64(1) << 32
		if _, err := checkMTU(int(big)); !errors.Is(err, canbus.ErrInvalidOption) {
			t.Error("MTU 2^32 accepted (wrapped to 0)")
		}
		if _, err := ifindex32(int(big)); !errors.Is(err, canbus.ErrInvalidOption) {
			t.Error("ifindex 2^32 accepted")
		}
	}
	if v, err := checkMTU(72); v != 72 || err != nil {
		t.Fatal(v, err)
	}
	if _, err := ifindex32(-1); !errors.Is(err, canbus.ErrInvalidOption) {
		t.Error("ifindex -1 accepted")
	}
	if v, err := ifindex32(math.MaxInt32); v != math.MaxInt32 || err != nil {
		t.Fatal(v, err)
	}
	// A negative ifindex from the kernel is not an interface.
	b := fakeLink()
	ne.PutUint32(b[4:], 0xFFFFFFFF)
	if _, ok := parseLink(b); ok {
		t.Fatal("negative ifindex accepted")
	}
	// ifinfomsg keeps the bits of a C int.
	if got := ne.Uint32(ifinfomsg(-1, 0, 0)[4:]); got != 0xFFFFFFFF {
		t.Fatalf("ifinfomsg(-1) index %#x", got)
	}
	// A route attribute cannot hold more than 65531 bytes.
	if a := attr(1, make([]byte, maxAttrData)); ne.Uint16(a) != math.MaxUint16 {
		t.Fatalf("largest attribute length %d", ne.Uint16(a))
	}
	defer func() {
		if recover() == nil {
			t.Fatal("oversized attribute did not panic")
		}
	}()
	attr(1, make([]byte, maxAttrData+1))
}

// FuzzNetlinkStream parses an arbitrary receive buffer as the request loop
// does: messages, then links and error codes.
func FuzzNetlinkStream(f *testing.F) {
	link := fakeLink()
	f.Add(append(message(rtmNewLink, nlmFMulti, 1, link), message(nlmsgDone, 0, 1)...))
	f.Add(message(nlmsgError, 0, 2, u32(0xFFFFFFEA)))
	f.Add(message(nlmsgError, 0, 2, []byte{1}))
	f.Fuzz(func(t *testing.T, b []byte) {
		msgs, _ := parseMessages(b)
		for _, m := range msgs {
			if len(m.body) > len(b) {
				t.Fatal("message body beyond the buffer")
			}
			switch m.typ {
			case nlmsgError:
				e, err := errnoOf(m.body)
				if err == nil && e != 0 && e > 1<<31 {
					t.Fatalf("errno %d out of range", e)
				}
			case rtmNewLink:
				if it, ok := parseLink(m.body); ok && it.Index < 0 {
					t.Fatalf("negative index %d", it.Index)
				}
			}
		}
	})
}

func FuzzParseLink(f *testing.F) {
	f.Add(fakeLink())
	f.Fuzz(func(t *testing.T, b []byte) {
		parseLink(b)
		_, _ = parseMessages(b)
	})
}

// TestMessageTooLarge: a request beyond the 32-bit netlink length panics
// before allocating (no API can build one; names are checked first).
func TestMessageTooLarge(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("a 4 GiB message needs 64-bit int")
	}
	chunk := make([]byte, 1<<20)
	body := make([][]byte, 4097) // 4097 MiB through one shared buffer
	for i := range body {
		body[i] = chunk
	}
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	message(rtmNewLink, 0, 1, body...)
}
