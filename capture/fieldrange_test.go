// SPDX-License-Identifier: MIT

package capture

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// TestPCAPNGResolutionTable: every if_tsresol value either gives the exact
// time of a tick count or, when 64 bits do not hold the ticks of one
// second, a *ParseError for the packets of that interface. No value reads
// as another resolution.
func TestPCAPNGResolutionTable(t *testing.T) {
	read := func(res byte, ts uint64) (time.Time, error) {
		got, _, err := readTrace(pcapngEPB(res, ts, 0, 9))
		if err != nil {
			return time.Time{}, err
		}
		return got[0].f.Timestamp, nil
	}
	for _, base := range []uint64{10, 2} {
		for _, e := range []int{0, 1, 6, 9, 18, 19, 20, 31, 32, 62, 63, 64, 126, 127} {
			res := byte(e)
			limit := 19
			if base == 2 {
				res, limit = res|0x80, 63
			}
			ticks := new(big.Int).Exp(big.NewInt(int64(base)), big.NewInt(int64(e)), nil) // ticks of one second
			if e > limit {
				if ticks.IsUint64() {
					t.Fatalf("%d^%d fits 64 bits: the table is wrong", base, e)
				}
				for _, ts := range []uint64{0, 1, math.MaxUint64} {
					var pe *ParseError
					if at, err := read(res, ts); !errors.As(err, &pe) {
						t.Errorf("%d^-%d s, %d ticks: read as %v, %v", base, e, ts, at, err)
					}
				}
				continue
			}
			// No tick, one second, and one second and a half (rounded down
			// to a nanosecond, as the reader does).
			second := ticks.Uint64()
			half := new(big.Int).Mul(new(big.Int).SetUint64(second/2), big.NewInt(int64(time.Second)))
			for _, c := range []struct {
				ts   uint64
				want time.Time
			}{
				{0, time.Unix(0, 0)},
				{second, time.Unix(1, 0)},
				{second + second/2, time.Unix(1, half.Div(half, ticks).Int64())},
			} {
				if at, err := read(res, c.ts); err != nil || !at.Equal(c.want) {
					t.Errorf("%d^-%d s, %d ticks: %v, %v; want %v", base, e, c.ts, at, err, c.want)
				}
			}
		}
	}
	// An interface whose packets carry no timestamp (simple packet blocks)
	// reads whatever its resolution.
	le := binary.LittleEndian
	file := pcapngEPB(0xFF, 0, 0, 9)
	// Rewrite the enhanced packet block (the last one) as a simple one.
	epb := len(file) - 44
	if le.Uint32(file[epb:]) != blockEPB {
		t.Fatal("the helper's layout changed")
	}
	spb := le.AppendUint32(nil, blockSPB)
	spb = le.AppendUint32(spb, 28)
	spb = le.AppendUint32(spb, 9)
	spb = append(spb, file[epb+28:epb+40]...)
	spb = le.AppendUint32(spb, 28)
	if got, _, err := readTrace(append(file[:epb:epb], spb...)); err != nil || len(got) != 1 || !got[0].f.Timestamp.IsZero() {
		t.Errorf("simple packet block: %v, %v", got, err)
	}
}

// pcapRecord builds a pcap file of the given magic with one classical
// frame at sec and frac.
func pcapRecord(order binary.AppendByteOrder, magic, sec, frac uint32) []byte {
	b := order.AppendUint32(nil, magic)
	b = order.AppendUint16(b, 2)
	b = order.AppendUint16(b, 4)
	b = append(b, make([]byte, 8)...)
	b = order.AppendUint32(b, pcapSnapLen)
	b = order.AppendUint32(b, LinkTypeCANSocketCAN)
	b = order.AppendUint32(b, sec)
	b = order.AppendUint32(b, frac)
	b = order.AppendUint32(b, 9)
	b = order.AppendUint32(b, 9)
	return append(b, 0, 0, 1, 0x23, 1, 0, 0, 0, 0xAA)
}

// TestPCAPFractionRange: the fraction of a pcap record time counts micro-
// or nanoseconds below one second; a record with a fraction of a second or
// more is a *ParseError, for both magics and both byte orders.
func TestPCAPFractionRange(t *testing.T) {
	for _, order := range []binary.AppendByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, c := range []struct {
			magic, frac uint32
			nanos       int64 // -1: not a time
		}{
			{pcapMagicMicro, 0, 0},
			{pcapMagicMicro, 1, 1000},
			{pcapMagicMicro, 999_999, 999_999_000},
			{pcapMagicMicro, 1_000_000, -1},
			{pcapMagicMicro, 1_000_001, -1},
			{pcapMagicMicro, math.MaxInt32, -1},
			{pcapMagicMicro, math.MaxUint32, -1},
			{pcapMagicNano, 0, 0},
			{pcapMagicNano, 999_999_999, 999_999_999},
			{pcapMagicNano, 1_000_000_000, -1},
			{pcapMagicNano, 1_000_000_001, -1},
			{pcapMagicNano, math.MaxInt32, -1},
			{pcapMagicNano, math.MaxUint32, -1},
		} {
			got, _, err := readTrace(pcapRecord(order, c.magic, 5, c.frac))
			if c.nanos < 0 {
				var pe *ParseError
				if !errors.As(err, &pe) || pe.Line != 1 {
					t.Errorf("%v magic %#x fraction %d: %v, %v", order, c.magic, c.frac, got, err)
				}
				continue
			}
			if err != nil || len(got) != 1 || !got[0].f.Timestamp.Equal(time.Unix(5, c.nanos)) {
				t.Errorf("%v magic %#x fraction %d: %v, %v", order, c.magic, c.frac, got, err)
			}
		}
	}
}

// TestBLFStartTimeRange: the start time of a BLF header is a SYSTEMTIME.
// Each field is read within its range only: a field beyond it, a day the
// month does not have and a year outside 1601..9999 are a *ParseError
// instead of another date.
func TestBLFStartTimeRange(t *testing.T) {
	pinLocalUTC(t)
	le := binary.LittleEndian
	can := make([]byte, 16)
	can[0] = 1
	file := func(fields [8]uint16) []byte {
		b := blfFile(t, blfObj(blfCANMessage, 1, 0, can))
		for i, v := range fields {
			le.PutUint16(b[40+2*i:], v)
		}
		return b
	}
	// year, month, day of the week, day, hour, minute, second, millisecond
	valid := [8]uint16{2024, 2, 4, 29, 23, 59, 59, 999}
	got, _, err := readTrace(file(valid))
	if want := time.Date(2024, 2, 29, 23, 59, 59, 999e6, time.UTC); err != nil || len(got) != 1 || !got[0].f.Timestamp.Equal(want) {
		t.Fatalf("%v, %v; want %v", got, err, want)
	}
	for _, c := range []struct {
		name  string
		field int
		value uint16
		ok    bool
	}{
		{"year 1600", 0, 1600, false}, {"year 1601", 0, 1601, false /* 1601-02-29 does not exist */}, {"year 1604", 0, 1604, true},
		{"year 9996", 0, 9996, true}, {"year 10000", 0, 10000, false}, {"year 65535", 0, 65535, false}, {"year 1", 0, 1, false},
		{"month 0", 1, 0, false}, {"month 1", 1, 1, true}, {"month 12", 1, 12, true}, {"month 13", 1, 13, false}, {"month 65535", 1, 65535, false},
		{"weekday 0", 2, 0, true}, {"weekday 6", 2, 6, true}, {"weekday 7", 2, 7, false},
		{"day 0", 3, 0, false}, {"day 1", 3, 1, true}, {"day 30 of February", 3, 30, false}, {"day 32", 3, 32, false},
		{"hour 0", 4, 0, true}, {"hour 24", 4, 24, false},
		{"minute 0", 5, 0, true}, {"minute 60", 5, 60, false},
		{"second 0", 6, 0, true}, {"second 60", 6, 60, false},
		{"millisecond 0", 7, 0, true}, {"millisecond 1000", 7, 1000, false}, {"millisecond 65535", 7, 65535, false},
	} {
		fields := valid
		fields[c.field] = c.value
		got, _, err := readTrace(file(fields))
		var pe *ParseError
		switch {
		case c.ok && (err != nil || len(got) != 1):
			t.Errorf("%s: %v, %v", c.name, got, err)
		case !c.ok && !errors.As(err, &pe):
			t.Errorf("%s: read as %v, %v", c.name, got, err)
		}
	}
	// A year of 0 is no start time: offsets count from 1970.
	if got, _, err := readTrace(file([8]uint16{0, 99, 99, 99, 99, 99, 99, 9999})); err != nil || !got[0].f.Timestamp.Equal(time.Unix(0, 0)) {
		t.Errorf("no start time: %v, %v", got, err)
	}
	// Start plus offset: the last second of the year 9999 reads, the next
	// one does not.
	last := [8]uint16{9999, 12, 5, 31, 23, 59, 58, 0}
	second := uint64(time.Second / (10 * time.Microsecond))
	b := blfFile(t, blfObj(blfCANMessage, 1, second, can))
	for i, v := range last {
		le.PutUint16(b[40+2*i:], v)
	}
	if got, _, err := readTrace(b); err != nil || got[0].f.Timestamp.Unix() != maxUnixSec {
		t.Errorf("last second of 9999: %v, %v", got, err)
	}
	b = blfFile(t, blfObj(blfCANMessage, 1, 2*second, can))
	for i, v := range last {
		le.PutUint16(b[40+2*i:], v)
	}
	var pe *ParseError
	if got, _, err := readTrace(b); !errors.As(err, &pe) {
		t.Errorf("first second of 10000: %v, %v", got, err)
	}
}

// TestPCAPNGInterfaceBound: a section holds at most pcapngMaxIfaces
// interface descriptions; the next one is a *ParseError, and what the
// reader keeps for a section stays within a budget set by that bound. A
// new section starts the count again.
func TestPCAPNGInterfaceBound(t *testing.T) {
	le := binary.LittleEndian
	block := func(b []byte, typ uint32, body []byte) []byte {
		b = le.AppendUint32(b, typ)
		b = le.AppendUint32(b, uint32(12+len(body)))
		b = append(b, body...)
		return le.AppendUint32(b, uint32(12+len(body)))
	}
	shb := make([]byte, 16)
	le.PutUint32(shb, bomMagic)
	le.PutUint16(shb[4:], 1)
	idb := make([]byte, 8)
	le.PutUint16(idb, LinkTypeCANSocketCAN)
	epb := make([]byte, 20)
	le.PutUint32(epb[12:], 9)
	le.PutUint32(epb[16:], 9)
	epb = append(epb, 0, 0, 1, 0x23, 1, 0, 0, 0, 0xAA, 0, 0, 0)
	section := func(b []byte, ifaces int) []byte {
		b = block(b, blockSHB, shb)
		for range ifaces {
			b = block(b, blockIDB, idb)
		}
		return block(b, blockEPB, epb)
	}
	for _, c := range []struct {
		name   string
		file   []byte
		frames int
		ok     bool
	}{
		{"no interface", section(nil, 0), 0, false},
		{"one", section(nil, 1), 1, true},
		{"one below the bound", section(nil, pcapngMaxIfaces-1), 1, true},
		{"the bound", section(nil, pcapngMaxIfaces), 1, true},
		{"one above the bound", section(nil, pcapngMaxIfaces+1), 0, false},
		{"the bound in each of two sections", section(section(nil, pcapngMaxIfaces), pcapngMaxIfaces), 2, true},
	} {
		got, _, err := readTrace(c.file)
		var pe *ParseError
		if len(got) != c.frames || c.ok != (err == nil) || !c.ok && !errors.As(err, &pe) {
			t.Errorf("%s: %d frames, %v", c.name, len(got), err)
		}
	}
	// Twenty times the bound in one section: the reader ends at the bound,
	// having kept at most the bound's interfaces (a few MiB with the
	// growth of the slice), not all of the file's.
	big := section(nil, 20*pcapngMaxIfaces)
	if n := allocated(func() {
		if _, _, err := readTrace(big); err == nil {
			t.Error("a section of 20 times the bound read without error")
		}
	}); n > 16<<20 {
		t.Errorf("%d bytes allocated for a section of %d interface descriptions", n, 20*pcapngMaxIfaces)
	}
}

// endlessLine is a reader of one line that never ends.
type endlessLine struct{}

func (endlessLine) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

// TestTextLineBound: the readers of the text formats hold one line of at
// most 1 MiB. A longer line ends the trace with bufio.ErrTooLong after the
// records before it, and costs the bound, not the length of the line.
func TestTextLineBound(t *testing.T) {
	for name, c := range map[string]struct {
		open  func(io.Reader) Reader
		first string // a trace with one frame
	}{
		"candump": {func(r io.Reader) Reader { return NewCandumpReader(r) }, "(1.000000) can0 123#AA\n"},
		"jsonl": {func(r io.Reader) Reader { return NewJSONLReader(r) },
			`{"type":"frame","timestamp":"1.0","id":291,"len":1,"data":"AA"}` + "\n"},
		"asc": {func(r io.Reader) Reader { return NewASCReader(r) }, "base hex timestamps absolute\n 1.0 1 123 Rx d 1 AA\n"},
		"trc": {func(r io.Reader) Reader { return NewTRCReader(r) }, ";$FILEVERSION=1.1\n 1) 1.0 Rx 0123 1 AA\n"},
	} {
		var frames int
		var err error
		got := allocated(func() {
			r := c.open(io.MultiReader(strings.NewReader(c.first), io.LimitReader(endlessLine{}, 64<<20)))
			for {
				var f canbus.Frame
				if err = r.Read(&f); err != nil {
					return
				}
				frames++
			}
		})
		if frames != 1 || !errors.Is(err, bufio.ErrTooLong) {
			t.Errorf("%s: %d frames, then %v; want 1 frame, then bufio.ErrTooLong", name, frames, err)
		}
		if got > 8<<20 {
			t.Errorf("%s: %d bytes allocated for a line of 64 MiB", name, got)
		}
	}
}
