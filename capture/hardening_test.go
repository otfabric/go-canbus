// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// item is one frame of a generated trace: a CAN/CAN FD frame or, with xl,
// a CAN XL frame.
type item struct {
	xl bool
	f  canbus.Frame
	x  canbus.XLFrame
}

// byteSource hands out the bytes of a fuzz input, then zeros.
type byteSource []byte

func (s *byteSource) u8() byte {
	if len(*s) == 0 {
		return 0
	}
	c := (*s)[0]
	*s = (*s)[1:]
	return c
}

func (s *byteSource) u16() uint16 { return uint16(s.u8())<<8 | uint16(s.u8()) }
func (s *byteSource) u32() uint32 { return uint32(s.u16())<<16 | uint32(s.u16()) }

// genTrace derives a trace of valid frames with non-decreasing timestamps
// between 1970 and 2106 from data. XL frames are generated only with xl.
func genTrace(data []byte, xl bool) []item {
	src := byteSource(data)
	ts := time.Unix(int64(src.u32()%(math.MaxUint32-1<<20)), int64(src.u32()%1e9))
	var out []item
	for len(src) > 0 && len(out) < 64 {
		ts = ts.Add(time.Duration(src.u16()) * time.Duration(src.u8()%16+1) * time.Microsecond)
		iface := []string{"vcan0", "can1"}[src.u8()%2]
		echo := src.u8()%4 == 0
		payload := func(n int) []byte {
			p := make([]byte, n)
			for i := range p {
				p[i] = src.u8()
			}
			return p
		}
		var it item
		var err error
		switch k := src.u8() % 8; k {
		case 0, 7:
			it.f, err = canbus.NewStandard(canbus.ID(src.u16())&canbus.MaxStandardID, payload(int(src.u8()%9)))
		case 1:
			it.f, err = canbus.NewExtended(canbus.ID(src.u32())&canbus.MaxExtendedID, payload(int(src.u8()%9)))
		case 2:
			ext := src.u8()%2 == 0
			id := canbus.ID(src.u32()) & canbus.MaxStandardID
			if ext {
				id = canbus.ID(src.u32()) & canbus.MaxExtendedID
			}
			it.f, err = canbus.NewRemote(id, ext, src.u8()%9)
		case 3:
			ext := src.u8()%2 == 0
			id := canbus.ID(src.u32()) & canbus.MaxStandardID
			if ext {
				id = canbus.ID(src.u32()) & canbus.MaxExtendedID
			}
			n := canbus.DLCToLen(src.u8(), true)
			it.f, err = canbus.NewFD(id, ext, src.u8()%2 == 0, payload(n))
			if src.u8()%2 == 0 {
				it.f.Flags |= canbus.FlagESI
			}
		case 4:
			it.f, err = canbus.NewStandard(canbus.ID(src.u16())&canbus.MaxStandardID, payload(8))
			it.f.Len8DLC = 9 + src.u8()%7
		case 5:
			it.f = canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrorClass(src.u32()) & canbus.ErrClassAll})
		case 6:
			if !xl {
				continue
			}
			it.xl = true
			it.x = canbus.XLFrame{Priority: src.u16() & canbus.MaxXLPriority, VCID: src.u8(),
				Flags: canbus.XLFlags(src.u8() & 0x03), SDT: src.u8(), AF: src.u32(),
				Data: payload(int(src.u16()%canbus.MaxXLLen) + 1)}
			err = it.x.Validate()
		}
		if err != nil {
			continue
		}
		if it.xl {
			it.x.Timestamp, it.x.Interface, it.x.Echo = ts, iface, echo
		} else {
			if !it.f.IsError() {
				if it.f.Validate() != nil {
					continue
				}
			}
			it.f.Timestamp, it.f.Interface, it.f.Echo = ts, iface, echo
		}
		out = append(out, it)
	}
	return out
}

var allFormats = []Format{FormatCandump, FormatJSONL, FormatPCAP, FormatPCAPNG, FormatASC, FormatBLF, FormatTRC}

func xlFormat(f Format) bool {
	switch f {
	case FormatCandump, FormatJSONL, FormatPCAP, FormatPCAPNG:
		return true
	default:
		return false
	}
}

// writeTrace writes items in format.
func writeTrace(t testing.TB, format Format, items []item) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, format)
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		if items[i].xl {
			err = w.(XLWriter).WriteXL(&items[i].x)
		} else {
			err = w.Write(&items[i].f)
		}
		if err != nil {
			t.Fatalf("%s: write %d: %v", format, i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readTrace reads every frame of b, stopping at the first error.
func readTrace(b []byte) ([]item, Format, error) {
	r, format, err := NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, format, err
	}
	xr, _ := r.(XLReader)
	var out []item
	for {
		var it item
		if xr != nil {
			it.xl, err = xr.ReadAny(&it.f, &it.x)
		} else {
			err = r.Read(&it.f)
		}
		if err == io.EOF {
			return out, format, nil
		}
		if err != nil {
			return out, format, err
		}
		out = append(out, it)
	}
}

// checkRoundTrip compares what was read back with what was written, as far
// as the format carries it.
func checkRoundTrip(t *testing.T, format Format, in, got []item) {
	t.Helper()
	if len(got) != len(in) {
		t.Fatalf("%s: %d frames read back, %d written", format, len(got), len(in))
	}
	vector := format == FormatASC || format == FormatBLF || format == FormatTRC
	for i := range in {
		w, g := &in[i], &got[i]
		if g.xl != w.xl {
			t.Fatalf("%s frame %d: XL %v, want %v", format, i, g.xl, w.xl)
		}
		var wantTS, gotTS time.Time
		var wantIf, gotIf string
		var wantEcho, gotEcho bool
		if w.xl {
			if g.x.Priority != w.x.Priority || g.x.VCID != w.x.VCID || g.x.Flags != w.x.Flags ||
				g.x.SDT != w.x.SDT || g.x.AF != w.x.AF || !bytes.Equal(g.x.Data, w.x.Data) {
				t.Fatalf("%s frame %d: %+v, want %+v", format, i, g.x, w.x)
			}
			wantTS, gotTS, wantIf, gotIf, wantEcho, gotEcho = w.x.Timestamp, g.x.Timestamp, w.x.Interface, g.x.Interface, w.x.Echo, g.x.Echo
		} else {
			switch {
			case vector && w.f.IsError():
				if !g.f.IsError() {
					t.Fatalf("%s frame %d: error frame lost: %v", format, i, g.f)
				}
			case !same(&g.f, &w.f):
				t.Fatalf("%s frame %d: %v (%v), want %v (%v)", format, i, g.f, g.f.Flags, w.f, w.f.Flags)
			}
			wantTS, gotTS, wantIf, gotIf, wantEcho, gotEcho = w.f.Timestamp, g.f.Timestamp, w.f.Interface, g.f.Interface, w.f.Echo, g.f.Echo
		}
		switch format {
		case FormatCandump:
			wantTS = wantTS.Truncate(time.Microsecond)
		case FormatPCAP:
			wantIf = ""
		case FormatJSONL, FormatPCAPNG, FormatASC, FormatBLF, FormatTRC:
		}
		if vector {
			wantIf = "1"
			if d := gotTS.Sub(wantTS); d > time.Microsecond || d < -time.Microsecond {
				t.Fatalf("%s frame %d: timestamp %v off by %v", format, i, gotTS, d)
			}
		} else if !gotTS.Equal(wantTS) {
			t.Fatalf("%s frame %d: timestamp %v, want %v", format, i, gotTS, wantTS)
		}
		if gotIf != wantIf {
			t.Fatalf("%s frame %d: interface %q, want %q", format, i, gotIf, wantIf)
		}
		// ASC and BLF error frames have no direction.
		lostEcho := !w.xl && w.f.IsError() && (format == FormatASC || format == FormatBLF)
		if (vector || format == FormatJSONL) && !lostEcho && gotEcho != wantEcho {
			t.Fatalf("%s frame %d: echo %v, want %v", format, i, gotEcho, wantEcho)
		}
	}
}

// pinLocalUTC makes the zone-less ASC, BLF and TRC times deterministic.
func pinLocalUTC(t testing.TB) {
	old := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = old })
}

func roundTripAll(t *testing.T, data []byte) {
	for _, format := range allFormats {
		in := genTrace(data, xlFormat(format))
		if len(in) == 0 {
			continue // an empty candump or JSONL trace is not detectable
		}
		b := writeTrace(t, format, in)
		got, detected, err := readTrace(b)
		if err != nil || detected != format {
			t.Fatalf("%s: read back as %s: %v (after %d frames)", format, detected, err, len(got))
		}
		checkRoundTrip(t, format, in, got)
	}
}

// FuzzRoundTrip writes random CAN, CAN FD and CAN XL traces in every
// format and reads them back.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("\x65\x4f\x00\x00\x00\x00\x00\x00\x01\x02\x00\x00\x03\x12\x34\x08\x01\x02\x03\x04\x05\x06\x07\x08"))
	rng := rand.New(rand.NewPCG(1, 2))
	for range 8 {
		b := make([]byte, 64+rng.IntN(512))
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		pinLocalUTC(t)
		roundTripAll(t, data)
	})
}

// TestRoundTripRandom is FuzzRoundTrip's property over a fixed set of
// random traces, so that plain go test covers more than the seeds.
func TestRoundTripRandom(t *testing.T) {
	pinLocalUTC(t)
	rng := rand.New(rand.NewPCG(7, 11))
	for range 100 {
		b := make([]byte, rng.IntN(2048))
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		roundTripAll(t, b)
	}
}

// TestTruncatedEveryOffset reads every prefix of a trace of every format:
// no reader may panic, and the binary formats return a prefix of the
// frames.
func TestTruncatedEveryOffset(t *testing.T) {
	pinLocalUTC(t)
	rng := rand.New(rand.NewPCG(3, 5))
	seed := make([]byte, 600)
	for i := range seed {
		seed[i] = byte(rng.Uint32())
	}
	for _, format := range allFormats {
		in := genTrace(seed, xlFormat(format))
		full := writeTrace(t, format, in)
		want, _, err := readTrace(full)
		if err != nil {
			t.Fatal(format, err)
		}
		binary := format == FormatPCAP || format == FormatPCAPNG || format == FormatBLF
		for cut := range len(full) {
			got, _, _ := readTrace(full[:cut])
			if len(got) > len(want) {
				t.Fatalf("%s cut at %d: %d frames, more than %d", format, cut, len(got), len(want))
			}
			if binary {
				checkRoundTrip(t, format, want[:len(got)], got)
			}
		}
	}
}

func TestWriterTimestampRange(t *testing.T) {
	pinLocalUTC(t)
	date := func(y int) time.Time { return time.Date(y, 6, 1, 0, 0, 0, 0, time.UTC) }
	mk := func(ts time.Time) *canbus.Frame {
		f, _ := canbus.NewStandard(0x123, []byte{1})
		f.Timestamp = ts
		return &f
	}
	for _, c := range []struct {
		format Format
		ts     time.Time
		ok     bool
	}{
		{FormatPCAP, date(1969), false},
		{FormatPCAP, date(2107), false},
		{FormatPCAP, time.Unix(math.MaxUint32, 999999999), true},
		{FormatPCAP, time.Unix(math.MaxUint32+1, 0), false},
		{FormatPCAPNG, date(1969), false},
		{FormatPCAPNG, date(2500), true},
		{FormatPCAPNG, time.Unix(pcapngMaxSec, 999999999), true},
		{FormatPCAPNG, date(2555), false},
		{FormatCandump, date(1969), false},
		{FormatCandump, date(9999), true},
		{FormatCandump, date(10000), false},
		{FormatJSONL, date(1969), false},
		{FormatJSONL, date(10000), false},
		{FormatBLF, date(1600), false},
		{FormatBLF, date(1969), true},
		{FormatASC, date(10000), false},
		{FormatASC, date(1969), true},
		{FormatTRC, date(2200), false},
		{FormatTRC, date(1600), false},
		{FormatTRC, date(1969), true},
	} {
		var buf bytes.Buffer
		w, _ := NewWriter(&buf, c.format)
		err := w.Write(mk(c.ts))
		if c.ok != (err == nil) || err != nil && !errors.Is(err, ErrUnrepresentable) {
			t.Errorf("%s at %v: %v", c.format, c.ts, err)
		}
		if c.ok {
			_ = w.Close()
			got, _, err := readTrace(buf.Bytes())
			if err != nil || len(got) != 1 || !got[0].f.Timestamp.Equal(c.ts) {
				t.Errorf("%s at %v: read back %v (%v)", c.format, c.ts, got, err)
			}
		}
	}
	// CAN XL frames are checked the same way.
	x := canbus.XLFrame{Priority: 1, Data: []byte{1}, Timestamp: date(1969)}
	for _, format := range []Format{FormatPCAP, FormatPCAPNG, FormatCandump, FormatJSONL} {
		w, _ := NewWriter(io.Discard, format)
		if err := w.(XLWriter).WriteXL(&x); !errors.Is(err, ErrUnrepresentable) {
			t.Errorf("%s XL before 1970: %v", format, err)
		}
	}
	// Relative times that do not fit a time.Duration.
	for _, format := range []Format{FormatBLF, FormatASC, FormatTRC} {
		w, _ := NewWriter(io.Discard, format)
		if err := w.Write(mk(date(1970))); err != nil {
			t.Fatal(format, err)
		}
		if err := w.Write(mk(date(2290))); !errors.Is(err, ErrUnrepresentable) {
			t.Errorf("%s 320 years after the start: %v", format, err)
		}
	}
	w, _ := NewWriter(io.Discard, FormatASC)
	if err := w.Write(mk(date(1970))); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(mk(date(1650))); !errors.Is(err, ErrUnrepresentable) {
		t.Errorf("ASC 320 years before the start: %v", err)
	}
}

func TestWriterFieldRange(t *testing.T) {
	// pcapng option values have a 16-bit length.
	f, _ := canbus.NewStandard(1, nil)
	f.Interface = strings.Repeat("n", math.MaxUint16+1)
	w, _ := NewPCAPNGWriter(io.Discard)
	if err := w.Write(&f); !errors.Is(err, ErrUnrepresentable) {
		t.Fatalf("interface name of 65536 bytes: %v", err)
	}
	var buf bytes.Buffer
	w, _ = NewPCAPNGWriter(&buf)
	f.Interface = strings.Repeat("n", math.MaxUint16)
	if err := w.Write(&f); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	got, _, err := readTrace(buf.Bytes())
	if err != nil || len(got) != 1 || got[0].f.Interface != f.Interface {
		t.Fatalf("interface name of 65535 bytes: %d frames, %v", len(got), err)
	}
	// CAN_FD_MESSAGE_64 has an 8-bit channel.
	fd, _ := canbus.NewFD(1, false, false, []byte{1})
	fd.Interface = "300"
	bw, _ := NewBLFWriter(io.Discard)
	if err := bw.Write(&fd); !errors.Is(err, ErrUnrepresentable) {
		t.Fatalf("BLF CAN FD channel 300: %v", err)
	}
	f.Interface = "300"
	if err := bw.Write(&f); err != nil {
		t.Fatalf("BLF classical channel 300: %v", err)
	}
}

// pcapngEPB builds a pcapng file with one interface of if_tsresol res and
// one enhanced packet block with timestamp ts.
func pcapngEPB(res byte, ts uint64, ifIdx, capLen uint32) []byte {
	le := binary.LittleEndian
	var b []byte
	block := func(typ uint32, body []byte) {
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
		b = le.AppendUint32(b, typ)
		b = le.AppendUint32(b, uint32(12+len(body)))
		b = append(b, body...)
		b = le.AppendUint32(b, uint32(12+len(body)))
	}
	shb := make([]byte, 16)
	le.PutUint32(shb, bomMagic)
	le.PutUint16(shb[4:], 1)
	block(blockSHB, shb)
	idb := make([]byte, 8)
	le.PutUint16(idb, LinkTypeCANSocketCAN)
	idb = append(idb, 9, 0, 1, 0, res, 0, 0, 0, 0, 0, 0, 0)
	block(blockIDB, idb)
	epb := make([]byte, 20)
	le.PutUint32(epb[0:], ifIdx)
	le.PutUint32(epb[4:], uint32(ts>>32))
	le.PutUint32(epb[8:], uint32(ts))
	le.PutUint32(epb[12:], capLen)
	le.PutUint32(epb[16:], 9)
	block(blockEPB, append(epb, 0, 0, 1, 0x23, 1, 0, 0, 0, 0xAA))
	return b
}

func TestReaderTimestampRange(t *testing.T) {
	pinLocalUTC(t)
	read := func(b []byte) ([]item, error) {
		got, _, err := readTrace(b)
		return got, err
	}
	// pcapng: 2^64-1 ns is 2554; 2^64-1 s is past the year 9999.
	if got, err := read(pcapngEPB(9, math.MaxUint64, 0, 9)); err != nil || got[0].f.Timestamp.Year() != 2554 {
		t.Fatalf("pcapng 2^64-1 ns: %v %v", got, err)
	}
	if _, err := read(pcapngEPB(0, math.MaxUint64, 0, 9)); err == nil {
		t.Fatal("pcapng 2^64-1 s accepted")
	}
	// A resolution of 10^-18 s: the fraction times 1e9 overflows 64 bits.
	got, err := read(pcapngEPB(18, 5e18+999999999999999999, 0, 9))
	if err != nil || !got[0].f.Timestamp.Equal(time.Unix(5, 999999999)) {
		t.Fatalf("pcapng attosecond ticks: %v %v", got, err)
	}
	// Indices and lengths that are negative as a 32-bit int.
	if _, err := read(pcapngEPB(9, 0, 0x80000000, 9)); err == nil {
		t.Fatal("interface 2^31 accepted")
	}
	if _, err := read(pcapngEPB(9, 0, 0, 0x80000000)); err == nil {
		t.Fatal("captured length 2^31 accepted")
	}
	// BLF: timestamps past a time.Duration in 10 µs and 1 ns units.
	can := make([]byte, 16)
	can[0] = 1
	if _, err := read(blfFile(t, blfObj(blfCANMessage, 1, math.MaxInt64/10000+1, can))); err == nil {
		t.Fatal("BLF 10 µs timestamp overflow accepted")
	}
	if got, err := read(blfFile(t, blfObj(blfCANMessage, 1, math.MaxInt64/10000, can))); err != nil || len(got) != 1 {
		t.Fatalf("BLF largest 10 µs timestamp: %v", err)
	}
	ns := blfObj(blfCANMessage, 1, math.MaxInt64+1, can)
	binary.LittleEndian.PutUint32(ns[16:], blfFlagOneNanos)
	if _, err := read(blfFile(t, ns)); err == nil {
		t.Fatal("BLF ns timestamp overflow accepted")
	}
	for _, s := range []string{
		"(99999999999999.000000) can0 123#00\n",
		`{"type":"frame","timestamp":"253402300800.0","id":1,"len":0,"data":""}` + "\n",
		"date Mon Oct 27 08:15:00.123 pm 2026\nbase hex timestamps absolute\n 1e300 1 123 Rx d 0\n",
		"date Mon Oct 27 08:15:00.123 pm 2026\nbase hex timestamps absolute\n NaN 1 123 Rx d 0\n",
		";$FILEVERSION=2.1\n;$STARTTIME=1e300\n;$COLUMNS=N,O,T,I,d,l,D\n 1 1.0 DT 0001 Rx 0\n",
		";$FILEVERSION=2.1\n;$STARTTIME=NaN\n;$COLUMNS=N,O,T,I,d,l,D\n 1 1.0 DT 0001 Rx 0\n",
		";$FILEVERSION=2.1\n;$STARTTIME=46000\n;$COLUMNS=N,O,T,I,d,l,D\n 1 1e300 DT 0001 Rx 0\n",
		";$FILEVERSION=1.1\n 1) Inf Rx 0001 0\n",
	} {
		var pe *ParseError
		if _, err := read([]byte(s)); !errors.As(err, &pe) {
			t.Errorf("%q: %v", s, err)
		}
	}
}

// blfContainer builds a LOG_CONTAINER object holding data, zlib-compressed
// when z, announcing usize uncompressed bytes.
func blfContainer(data []byte, z bool, usize uint32) []byte {
	le := binary.LittleEndian
	method := uint16(blfCompressNone)
	if z {
		var b bytes.Buffer
		zw := zlib.NewWriter(&b)
		_, _ = zw.Write(data)
		_ = zw.Close()
		data, method = b.Bytes(), blfCompressZlib
	}
	c := make([]byte, 32)
	copy(c, blfObjSig)
	le.PutUint16(c[4:], 16)
	le.PutUint16(c[6:], 1)
	le.PutUint32(c[8:], uint32(32+len(data)))
	le.PutUint32(c[12:], blfContType)
	le.PutUint16(c[16:], method)
	le.PutUint32(c[24:], usize)
	c = append(c, data...)
	return append(c, make([]byte, (32+len(data))%4)...)
}

func blfHeader(t *testing.T) []byte {
	t.Helper()
	w := &BLFWriter{start: time.Unix(1700000000, 0)}
	h, err := w.fileHeader(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestBLFSplitObjects splits the objects of a trace across two containers
// at every byte, as python-can does at 128 KiB.
func TestBLFSplitObjects(t *testing.T) {
	pinLocalUTC(t)
	var objs []byte
	for i := range 5 {
		body := make([]byte, 16)
		body[0], body[3], body[4], body[8] = 1, 1, byte(i), byte(i)
		objs = append(objs, blfObj(blfCANMessage, 1, uint64(i), body)...)
		fd := make([]byte, 40+12)
		fd[0], fd[1], fd[2], fd[4] = 1, 9, 12, byte(i) // 51-byte object: 3 padding bytes
		binary.LittleEndian.PutUint32(fd[12:], 0x1000)
		objs = append(objs, blfObj(blfCANFD64, 1, uint64(i), fd[:40+12])...)
	}
	whole, _, err := readTrace(append(blfHeader(t), blfContainer(objs, true, uint32(len(objs)))...))
	if err != nil || len(whole) != 10 {
		t.Fatalf("unsplit: %d frames, %v", len(whole), err)
	}
	for cut := 1; cut < len(objs); cut++ {
		for _, z := range []bool{false, true} {
			b := blfHeader(t)
			b = append(b, blfContainer(objs[:cut], z, uint32(cut))...)
			b = append(b, blfContainer(objs[cut:], z, uint32(len(objs)-cut))...)
			got, _, err := readTrace(b)
			if err != nil {
				t.Fatalf("split at %d: %v", cut, err)
			}
			checkRoundTrip(t, FormatBLF, whole, got)
		}
	}
	// The incomplete object at the end of the file is an error.
	b := append(blfHeader(t), blfContainer(objs[:20], false, 20)...)
	if _, _, err := readTrace(b); err == nil {
		t.Fatal("object cut at the end of the file accepted")
	}
}

// allocated returns the bytes allocated while f runs.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestReadersMemoryBounded feeds hostile files whose announced sizes are
// far larger than the file and checks the memory the readers allocate.
func TestReadersMemoryBounded(t *testing.T) {
	le := binary.LittleEndian
	readAllOf := func(b []byte) error { _, _, err := readTrace(b); return err }

	// BLF: a 16 MiB object announced in a 40-byte file.
	obj := make([]byte, 16)
	copy(obj, blfObjSig)
	le.PutUint32(obj[8:], blfObjectMax)
	le.PutUint32(obj[12:], blfContType)
	hostile := append(blfHeader(t), obj...)
	hostile = append(hostile, make([]byte, 24)...)
	if n := allocated(func() {
		if readAllOf(hostile) == nil {
			t.Error("truncated 16 MiB object accepted")
		}
	}); n > 1<<20 {
		t.Errorf("truncated 16 MiB BLF object: %d bytes allocated", n)
	}
	le.PutUint32(obj[8:], blfObjectMax+1)
	if readAllOf(append(blfHeader(t), obj...)) == nil {
		t.Error("object larger than 16 MiB accepted")
	}

	// BLF zip bombs: 32 MiB of zeros in a ~32 KiB container, announcing
	// 1 KiB, 16 MiB and more than 16 MiB.
	bomb := make([]byte, 32<<20)
	for _, c := range []struct {
		usize uint32
		limit uint64
	}{{1024, 1 << 20}, {blfObjectMax, 20 << 20}, {blfObjectMax + 1, 1 << 20}} {
		file := append(blfHeader(t), blfContainer(bomb, true, c.usize)...)
		if n := allocated(func() {
			if readAllOf(file) == nil {
				t.Errorf("zip bomb announcing %d bytes accepted", c.usize)
			}
		}); n > c.limit+4*uint64(len(file)) { // reading the file itself costs a few times its size
			t.Errorf("zip bomb announcing %d bytes: %d bytes allocated", c.usize, n)
		}
	}

	// BLF: a container inside a container is skipped, not expanded.
	can := make([]byte, 16)
	can[0] = 1
	inner := blfContainer(blfObj(blfCANMessage, 1, 0, can), true, 32)
	nested := append(inner, blfObj(blfCANMessage, 1, 1, can)...)
	got, _, err := readTrace(append(blfHeader(t), blfContainer(nested, true, uint32(len(nested)))...))
	if err != nil || len(got) != 1 {
		t.Errorf("nested container: %d frames, %v", len(got), err)
	}

	// pcapng: a 1 MiB block announced in a short file.
	ng := pcapngEPB(9, 0, 0, 9)
	ng = append(ng, 6, 0, 0, 0, 0, 0, 0x10, 0, 1, 2, 3, 4)
	if n := allocated(func() {
		if readAllOf(ng) == nil {
			t.Error("truncated pcapng block accepted")
		}
	}); n > 4<<20 {
		t.Errorf("truncated pcapng block: %d bytes allocated", n)
	}

	// pcap: records of 64 KiB, the most the reader accepts, reuse one buffer.
	var p bytes.Buffer
	pw, _ := NewPCAPWriter(&p)
	_ = pw.Close()
	rec := make([]byte, 16+65536)
	le.PutUint32(rec[8:], 65536)
	le.PutUint32(rec[12:], 65536)
	rec[16+4] = 8
	for range 64 {
		p.Write(rec)
	}
	if n := allocated(func() {
		if readAllOf(p.Bytes()) != nil {
			t.Error("64 KiB pcap records rejected")
		}
	}); n > 1<<20 {
		t.Errorf("64 pcap records of 64 KiB: %d bytes allocated", n)
	}
	le.PutUint32(rec[8:], 65537)
	if readAllOf(append(p.Bytes()[:24:24], rec...)) == nil {
		t.Error("pcap record of 65537 bytes accepted")
	}

	// Text: lines longer than 1 MiB are an error, not a bigger buffer.
	long := append([]byte("(1.0) can0 123#"), bytes.Repeat([]byte("0"), 2<<20)...)
	if n := allocated(func() {
		if readAllOf(long) == nil {
			t.Error("2 MiB candump line accepted")
		}
	}); n > 8<<20 {
		t.Errorf("2 MiB line: %d bytes allocated", n)
	}
}
