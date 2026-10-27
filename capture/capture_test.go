// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
)

func frames(t testing.TB) []canbus.Frame {
	t.Helper()
	ts := time.Unix(1700000000, 123456789)
	mk := func(f canbus.Frame, err error) canbus.Frame {
		if err != nil {
			t.Fatal(err)
		}
		f.Timestamp = ts
		ts = ts.Add(1500 * time.Microsecond)
		f.Interface = "vcan0"
		return f
	}
	out := []canbus.Frame{
		mk(canbus.NewStandard(0x123, []byte{0xA1, 0xB2})),
		mk(canbus.NewStandard(0, nil)),
		mk(canbus.NewExtended(0x1FFFFFFF, []byte{1, 2, 3, 4, 5, 6, 7, 8})),
		mk(canbus.NewRemote(0x7FF, false, 4)),
		mk(canbus.NewRemote(0x1234, true, 0)),
		mk(canbus.NewFD(0x10, false, true, bytes.Repeat([]byte{0x5A}, 64))),
		mk(canbus.NewFD(0x10, true, false, make([]byte, 12))),
		mk(canbus.Frame{ID: 0x22, Flags: canbus.FlagFD | canbus.FlagESI}, nil),
		mk(canbus.Frame{ID: 1, Len: 8, Len8DLC: 13, Data: [64]byte{1, 2, 3, 4, 5, 6, 7, 8}}, nil),
		mk(canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassBusOff | canbus.ErrClassController, Controller: canbus.CtrlTxPassive}), nil),
	}
	out[1].Echo = true
	return out
}

func same(a, b *canbus.Frame) bool {
	return a.ID == b.ID && a.Flags == b.Flags && a.Len == b.Len && a.Len8DLC == b.Len8DLC && a.Data == b.Data
}

func TestParseFrame(t *testing.T) {
	ok := map[string]canbus.Frame{
		"123#DEADBEEF":              {ID: 0x123, Len: 4, Data: [64]byte{0xDE, 0xAD, 0xBE, 0xEF}},
		"5AA#":                      {ID: 0x5AA},
		"1F334455#1122334455667788": {ID: 0x1F334455, Flags: canbus.FlagExtended, Len: 8, Data: [64]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}},
		"123#R":                     {ID: 0x123, Flags: canbus.FlagRemote},
		"123#R3":                    {ID: 0x123, Flags: canbus.FlagRemote, Len: 3},
		"00000123#R":                {ID: 0x123, Flags: canbus.FlagRemote | canbus.FlagExtended},
		"7A1#r":                     {ID: 0x7A1, Flags: canbus.FlagRemote},
		"123#11.22.33":              {ID: 0x123, Len: 3, Data: [64]byte{0x11, 0x22, 0x33}},
		"213##311":                  {ID: 0x213, Flags: canbus.FlagFD | canbus.FlagBRS | canbus.FlagESI, Len: 1, Data: [64]byte{0x11}},
		"213##0":                    {ID: 0x213, Flags: canbus.FlagFD},
		"213##1112233445566778899":  {ID: 0x213, Flags: canbus.FlagFD | canbus.FlagBRS, Len: 12, Data: [64]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}},
		"123#1122334455667788_E":    {ID: 0x123, Len: 8, Len8DLC: 14, Data: [64]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}},
		"20000080#0000000000000000": {ID: 0x80, Flags: canbus.FlagError, Len: 8},
	}
	for in, want := range ok {
		var f canbus.Frame
		if err := ParseFrame(in, &f); err != nil || !same(&f, &want) {
			t.Errorf("ParseFrame(%q) = %+v, %v", in, f, err)
		}
	}
	bad := []string{"123", "12#00", "1234#00", "800#00", "40000000#", "XYZ#00", "123#0", "123#GG", "123#112233445566778899",
		"123#R9", "123#R12", "123##", "123##Z0", "123###00", "123#11_9", "123#1122334455667788_8", "123#1122334455667788_EE",
		"20000080##100", "20000080#R", "123##1" + strings.Repeat("00", 65)}
	for _, in := range bad {
		var f canbus.Frame
		if err := ParseFrame(in, &f); err == nil {
			t.Errorf("ParseFrame(%q) accepted: %+v", in, f)
		}
	}
}

func TestCandumpLines(t *testing.T) {
	in := "# comment\n\n(1436509052.249713) vcan0 044#2A366C2BBA\n(1436509052.449847) can1 12345678##1AA\n(0.5) x 123#R T\n(1.000000001) x 123# R\n"
	r := NewCandumpReader(strings.NewReader(in))
	var f canbus.Frame
	if err := r.Read(&f); err != nil || f.ID != 0x44 || f.Len != 5 || f.Interface != "vcan0" || f.Timestamp != time.Unix(1436509052, 249713000) {
		t.Fatal(f, err)
	}
	if err := r.Read(&f); err != nil || !f.IsFD() || !f.IsExtended() || f.Len != 1 || f.Interface != "can1" {
		t.Fatal(f, err)
	}
	if err := r.Read(&f); err != nil || !f.Echo || f.Timestamp != time.Unix(0, 500000000) {
		t.Fatal(f, err)
	}
	if err := r.Read(&f); err != nil || f.Echo || f.Timestamp != time.Unix(1, 1) {
		t.Fatal(f, err)
	}
	if err := r.Read(&f); err != io.EOF {
		t.Fatal(err)
	}
	for _, line := range []string{"123#00", "(1) x", "1) x 123#00", "(1.2.3) x 123#00", "(-1) x 123#", "(1.0000000001) x 123#", "(1) x 123#00 Q", "(1) x 123#00 R R", "(1.x) a 123#"} {
		var f canbus.Frame
		if err := ParseCandumpLine(line, &f); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
	r = NewCandumpReader(strings.NewReader("(1) x 123#00\n(2) x bad\n"))
	_ = r.Read(&f)
	var pe *ParseError
	if err := r.Read(&f); !errors.As(err, &pe) || pe.Line != 2 || !strings.Contains(pe.Error(), "candump record 2") {
		t.Fatal(err)
	}
}

func roundTrip(t *testing.T, format Format) {
	t.Helper()
	in := frames(t)
	var buf bytes.Buffer
	w, err := NewWriter(&buf, format)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if err := w.Write(&in[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, got, err := NewReader(&buf)
	if err != nil || got != format {
		t.Fatalf("detected %q: %v", got, err)
	}
	for i := range in {
		var f canbus.Frame
		if err := r.Read(&f); err != nil {
			t.Fatalf("%s frame %d: %v", format, i, err)
		}
		if !same(&f, &in[i]) {
			t.Errorf("%s frame %d: %v, want %v", format, i, f, in[i])
		}
		wantTS := in[i].Timestamp
		if format == FormatCandump {
			wantTS = wantTS.Truncate(time.Microsecond)
		}
		if !f.Timestamp.Equal(wantTS) {
			t.Errorf("%s frame %d: timestamp %v, want %v", format, i, f.Timestamp, wantTS)
		}
		if format != FormatPCAP && f.Interface != "vcan0" {
			t.Errorf("%s: interface %q", format, f.Interface)
		}
		if format == FormatJSONL && f.Echo != in[i].Echo {
			t.Errorf("echo lost")
		}
	}
	var f canbus.Frame
	if err := r.Read(&f); err != io.EOF {
		t.Fatalf("%s: after the last frame: %v", format, err)
	}
}

func TestRoundTrips(t *testing.T) {
	for _, f := range []Format{FormatCandump, FormatJSONL, FormatPCAP} {
		roundTrip(t, f)
	}
	if _, err := NewWriter(io.Discard, FormatPCAPNG); !errors.Is(err, ErrUnknownFormat) {
		t.Fatal(err)
	}
	if _, _, err := NewReader(strings.NewReader("hello")); !errors.Is(err, ErrUnknownFormat) {
		t.Fatal(err)
	}
	if _, _, err := NewReader(strings.NewReader("  \n")); !errors.Is(err, ErrUnknownFormat) {
		t.Fatal(err)
	}
	if _, f, err := NewReader(strings.NewReader("# c\n(1) a 123#\n")); err != nil || f != FormatCandump {
		t.Fatal(f, err)
	}
}

func TestCandumpWriterDefaults(t *testing.T) {
	var b bytes.Buffer
	w := NewCandumpWriter(&b, "")
	f, _ := canbus.NewStandard(1, []byte{2})
	_ = w.Write(&f)
	_ = w.Close()
	if b.String() != "(0000000000.000000) can0 001#02\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestJSONLErrors(t *testing.T) {
	bad := []string{
		`{"type":"header","version":2}`,
		`{"type":"what"}`,
		`{"type":"frame","id":1,"len":1,"data":"zz"}`,
		`{"type":"frame","id":1,"len":2,"data":"00"}`,
		`{"type":"frame","id":1,"len":0,"data":"00","remote":true}`,
		`{"type":"frame","id":2048,"len":0,"data":""}`,
		`{"type":"frame","id":1,"len":0,"data":"","timestamp":"x"}`,
		`{"type":"frame","id":1,"len":0,"data":"","extra":1}`,
		`{"type":"frame","id":1,"len":65,"data":"` + strings.Repeat("00", 65) + `","fd":true}`,
		`not json`,
	}
	for _, line := range bad {
		r := NewJSONLReader(strings.NewReader(line + "\n"))
		var f canbus.Frame
		if err := r.Read(&f); err == nil || err == io.EOF {
			t.Errorf("accepted %s: %v", line, err)
		}
	}
	r := NewJSONLReader(strings.NewReader("\n{\"type\":\"frame\",\"id\":5,\"len\":0,\"data\":\"\",\"remote\":true,\"len8dlc\":0}\n"))
	var f canbus.Frame
	if err := r.Read(&f); err != nil || !f.IsRemote() || !f.Timestamp.IsZero() {
		t.Fatal(f, err)
	}
}

// pcapng builds a pcapng file: an interface of link type 1 (Ethernet)
// whose packet must be skipped, a CAN interface with nanosecond
// resolution and a name, one EPB, one SPB.
func pcapng(order binary.ByteOrder) []byte {
	var b bytes.Buffer
	block := func(typ uint32, body []byte) {
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
		total := uint32(12 + len(body))
		_ = binary.Write(&b, order, typ)
		_ = binary.Write(&b, order, total)
		b.Write(body)
		_ = binary.Write(&b, order, total)
	}
	shb := make([]byte, 16)
	order.PutUint32(shb[0:], bomMagic)
	order.PutUint16(shb[4:], 1)
	for i := 8; i < 16; i++ {
		shb[i] = 0xFF
	}
	block(blockSHB, shb)
	idb := func(lt uint16, opts []byte) []byte {
		h := make([]byte, 8)
		order.PutUint16(h[0:], lt)
		order.PutUint32(h[4:], 256)
		return append(h, opts...)
	}
	opt := func(code uint16, v []byte) []byte {
		o := make([]byte, 4)
		order.PutUint16(o[0:], code)
		order.PutUint16(o[2:], uint16(len(v)))
		o = append(o, v...)
		for len(o)%4 != 0 {
			o = append(o, 0)
		}
		return o
	}
	canOpts := append(opt(2, []byte("can7")), opt(9, []byte{9})...)
	canOpts = append(canOpts, 0, 0, 0, 0)
	block(blockIDB, idb(LinkTypeCANSocketCAN, canOpts)) // interface 0 (also of SPBs)
	block(blockIDB, idb(1, nil))                        // interface 1: Ethernet
	pkt := func(ifc uint32, ts uint64, data []byte) []byte {
		h := make([]byte, 20)
		order.PutUint32(h[0:], ifc)
		order.PutUint32(h[4:], uint32(ts>>32))
		order.PutUint32(h[8:], uint32(ts))
		order.PutUint32(h[12:], uint32(len(data)))
		order.PutUint32(h[16:], uint32(len(data)))
		return append(h, data...)
	}
	block(blockEPB, pkt(1, 0, []byte{1, 2, 3}))
	can := []byte{0x80, 0, 0x12, 0x34, 2, 0, 0, 0, 0xAB, 0xCD}
	block(blockEPB, pkt(0, 1700000000123456789, can))
	block(0x0BAD, []byte{1, 2, 3, 4}) // unknown block: skipped
	spb := make([]byte, 4)
	order.PutUint32(spb, uint32(len(can)))
	block(blockSPB, append(spb, can...))
	return b.Bytes()
}

func TestPCAPNG(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		r, f, err := NewReader(bytes.NewReader(pcapng(order)))
		if err != nil || f != FormatPCAPNG {
			t.Fatal(f, err)
		}
		var fr canbus.Frame
		if err := r.Read(&fr); err != nil || fr.ID != 0x1234 || !fr.IsExtended() || fr.Len != 2 || fr.Data[1] != 0xCD || fr.Interface != "can7" ||
			fr.Timestamp != time.Unix(1700000000, 123456789) {
			t.Fatalf("%v: %+v %v", order, fr, err)
		}
		if err := r.Read(&fr); err != nil || fr.ID != 0x1234 || !fr.Timestamp.IsZero() {
			t.Fatal(fr, err)
		}
		if err := r.Read(&fr); err != io.EOF {
			t.Fatal(err)
		}
	}
	good := pcapng(binary.LittleEndian)
	for _, cut := range []int{4, 10, 30, len(good) - 3} {
		r, _ := NewPCAPNGReader(bytes.NewReader(good[:cut]))
		var err error
		for err == nil {
			err = r.Read(&canbus.Frame{})
		}
		if err == io.EOF && cut != 4 {
			t.Errorf("truncation at %d not reported", cut)
		}
	}
	r, _ := NewPCAPNGReader(bytes.NewReader([]byte{6, 0, 0, 0, 12, 0, 0, 0, 12, 0, 0, 0}))
	if err := r.Read(&canbus.Frame{}); err == nil || !strings.Contains(err.Error(), "before the section header") {
		t.Fatal(err)
	}
}

func TestPCAPVariants(t *testing.T) {
	// Big-endian, microsecond file written by another tool, with an old
	// style FD packet (no FDF flag, longer than 16 bytes).
	var b bytes.Buffer
	h := make([]byte, 24)
	binary.BigEndian.PutUint32(h[0:], pcapMagicMicro)
	binary.BigEndian.PutUint32(h[20:], LinkTypeCANSocketCAN)
	b.Write(h)
	rec := func(sec, usec uint32, data []byte) {
		r := make([]byte, 16)
		binary.BigEndian.PutUint32(r[0:], sec)
		binary.BigEndian.PutUint32(r[4:], usec)
		binary.BigEndian.PutUint32(r[8:], uint32(len(data)))
		binary.BigEndian.PutUint32(r[12:], uint32(len(data)))
		b.Write(r)
		b.Write(data)
	}
	fd := make([]byte, 8+12)
	fd[3], fd[4], fd[5] = 0x55, 12, fdFlagBRS
	rec(5, 7, fd)
	rtr := []byte{0x40, 0, 0x01, 0x23, 3, 0, 0, 0}
	rec(5, 8, rtr)
	r, _, err := NewReader(&b)
	if err != nil {
		t.Fatal(err)
	}
	var f canbus.Frame
	if err := r.Read(&f); err != nil || !f.IsFD() || f.Flags&canbus.FlagBRS == 0 || f.Len != 12 || f.Timestamp != time.Unix(5, 7000) {
		t.Fatal(f, err)
	}
	if err := r.Read(&f); err != nil || !f.IsRemote() || f.Len != 3 {
		t.Fatal(f, err)
	}
	for _, bad := range [][]byte{nil, make([]byte, 24), append(append([]byte{}, h[:20]...), 0, 0, 0, 1)} {
		if _, err := NewPCAPReader(bytes.NewReader(bad)); err == nil {
			t.Error("bad header accepted")
		}
	}
	for _, pkt := range [][]byte{{1, 2}, {0, 0, 0, 1, 9, 0, 0, 0}, append([]byte{0, 0, 0, 1, 65, fdFlagFDF, 0, 0}, make([]byte, 64)...)} {
		if err := decodeSocketCAN(pkt, &f); err == nil {
			t.Errorf("packet % x accepted", pkt)
		}
	}
}

func TestReplay(t *testing.T) {
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	tx, _ := vb.Open(virtual.WithFD())
	rx, _ := vb.Open(virtual.WithFD(), virtual.WithQueueSize(10000))
	other, _ := vb.Open(virtual.WithFD())
	otherRx, _ := vb.Open(virtual.WithFD(), virtual.WithoutLoopback())
	_ = otherRx
	ctx := context.Background()
	in := frames(t)
	var buf bytes.Buffer
	w := NewJSONLWriter(&buf)
	for i := range in {
		_ = w.Write(&in[i])
	}
	_ = w.Close()
	trace := buf.Bytes()
	reader := func() Reader { return NewJSONLReader(bytes.NewReader(trace)) }

	// As fast as possible: everything but the error frame, unchanged.
	var seen int
	st, err := Replay(ctx, reader(), tx, ReplayOptions{OnFrame: func(*canbus.Frame) { seen++ }})
	if err != nil || st.Read != len(in) || st.Sent != len(in)-1 || st.Skipped != 1 || seen != st.Sent {
		t.Fatal(st, err)
	}
	for i := 0; i < st.Sent; i++ {
		var f canbus.Frame
		if err := rx.Receive(ctx, &f); err != nil || !same(&f, &in[i]) {
			t.Fatal(i, f, err)
		}
	}
	// Original timing: 9 gaps of 1.5 ms.
	st, err = Replay(ctx, reader(), tx, ReplayOptions{Speed: 1})
	if err != nil || st.Elapsed < 12*time.Millisecond {
		t.Fatal("original timing too fast:", st, err)
	}
	// Scaled: 100x faster than original is near zero.
	st, err = Replay(ctx, reader(), tx, ReplayOptions{Speed: 100})
	if err != nil || st.Elapsed > 50*time.Millisecond {
		t.Fatal(st, err)
	}
	// Filter, MaxFrames.
	flt := canbus.FilterSet{Filters: []canbus.Filter{{ID: 0x10, Mask: 0x7FF}}}
	st, _ = Replay(ctx, reader(), tx, ReplayOptions{Filter: &flt})
	if st.Sent != 2 || st.Filtered != len(in)-3 {
		t.Fatal("filter", st)
	}
	st, _ = Replay(ctx, reader(), tx, ReplayOptions{MaxFrames: 2})
	if st.Sent != 2 || !st.Limited {
		t.Fatal("max frames", st)
	}
	// Rate limit: 9 frames at 200/s takes at least 40 ms.
	st, _ = Replay(ctx, reader(), tx, ReplayOptions{Rate: 200})
	if st.Elapsed < 40*time.Millisecond {
		t.Fatal("rate", st)
	}
	// MaxDuration ends a slow replay.
	st, err = Replay(ctx, reader(), tx, ReplayOptions{Speed: 0.001, MaxDuration: 30 * time.Millisecond})
	if err != nil || !st.Limited || st.Sent != 1 || st.Elapsed > time.Second {
		t.Fatal("max duration", st, err)
	}
	// Cancellation during a wait.
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := Replay(cctx, reader(), tx, ReplayOptions{Speed: 0.001}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := Replay(cctx, reader(), tx, ReplayOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// Interface map, send failure, bad options, reader errors.
	st, err = Replay(ctx, reader(), tx, ReplayOptions{Interfaces: map[string]Sender{"vcan0": other}})
	if err != nil || st.Sent != len(in)-1 || other.Stats().TxFrames != uint64(st.Sent) {
		t.Fatal("interface map", st, err)
	}
	classic, _ := vb.Open()
	if _, err := Replay(ctx, reader(), classic, ReplayOptions{}); !errors.Is(err, canbus.ErrUnsupported) {
		t.Fatal("FD on a classical port:", err)
	}
	if _, err := Replay(ctx, reader(), tx, ReplayOptions{Speed: -1}); !errors.Is(err, canbus.ErrInvalidOption) {
		t.Fatal(err)
	}
	if _, err := Replay(ctx, NewCandumpReader(strings.NewReader("bad\n")), tx, ReplayOptions{}); err == nil {
		t.Fatal("reader error ignored")
	}
}

func TestRecordFrames(t *testing.T) {
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	tx, _ := vb.Open()
	rxp, _ := vb.Open()
	b, _ := canbus.NewBus(rxp)
	defer b.Close()
	sub, _ := b.Subscribe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		f, _ := canbus.NewStandard(canbus.ID(i), nil)
		_ = tx.Send(ctx, &f)
	}
	var buf bytes.Buffer
	w := NewCandumpWriter(&buf, "")
	n, err := RecordFrames(ctx, sub, w, 3)
	_ = w.Close()
	if err != nil || n != 3 || strings.Count(buf.String(), "\n") != 3 || !strings.Contains(buf.String(), " virtual0 002#") {
		t.Fatal(n, err, buf.String())
	}
	short, c2 := context.WithTimeout(ctx, 20*time.Millisecond)
	defer c2()
	if n, err := RecordFrames(short, sub, w, 0); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	sub.Close()
	if _, err := RecordFrames(ctx, sub, w, 0); !errors.Is(err, canbus.ErrClosed) {
		t.Fatal(err)
	}
}

func FuzzParseFrame(f *testing.F) {
	for _, s := range []string{"123#DEADBEEF", "12345678##3AABB", "123#R5", "123#1122334455667788_C", "20000004#0000000000000000"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var fr canbus.Frame
		if err := ParseFrame(s, &fr); err != nil {
			return
		}
		if err := fr.Validate(); err != nil {
			t.Fatalf("%q parsed to an invalid frame: %v", s, err)
		}
		var again canbus.Frame
		if err := ParseFrame(fr.String(), &again); err != nil || !same(&fr, &again) {
			t.Fatalf("%q -> %q -> %+v (%v)", s, fr.String(), again, err)
		}
	})
}

func FuzzReaders(f *testing.F) {
	var b bytes.Buffer
	w, _ := NewPCAPWriter(&b)
	for _, fr := range frames(f) {
		_ = w.Write(&fr)
	}
	_ = w.Close()
	f.Add(b.Bytes())
	f.Add(pcapng(binary.LittleEndian))
	f.Add([]byte("(1.5) vcan0 123#00\n"))
	f.Add([]byte(`{"type":"frame","id":1,"len":1,"data":"00"}` + "\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, _, err := NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		for i := 0; i < 1000; i++ {
			var fr canbus.Frame
			if err := r.Read(&fr); err != nil {
				return
			}
			if fr.Flags&canbus.FlagError == 0 {
				if err := fr.Validate(); err != nil {
					t.Fatalf("reader produced an invalid frame %+v: %v", fr, err)
				}
			}
		}
	})
}
