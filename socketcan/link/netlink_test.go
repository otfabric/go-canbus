// SPDX-License-Identifier: MIT

package link

import (
	"testing"
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
	if errnoOf([]byte{1}) != -1 || errnoOf(u32(0xFFFFFFFF)) != -1 {
		t.Fatal("errnoOf")
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
	if (ModeFD | ModeBerrReporting).String() != "BERR-REPORTING,FD" || CtrlMode(0x1000).String() != "0x1000" {
		t.Fatal((ModeFD | ModeBerrReporting).String())
	}
	if operState(2) != "DOWN" || operState(9) != "STATE-9" || cstring([]byte("ab")) != "ab" {
		t.Fatal("operState")
	}
}

func FuzzParseLink(f *testing.F) {
	f.Add(fakeLink())
	f.Fuzz(func(t *testing.T, b []byte) {
		parseLink(b)
		_, _ = parseMessages(b)
	})
}
