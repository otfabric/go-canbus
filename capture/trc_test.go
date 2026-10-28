// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

func TestTRCRoundTrip(t *testing.T) { vectorRoundTrip(t, FormatTRC, time.Microsecond) }

// TestTRCErrorFrames: a protocol violation and the counters survive.
func TestTRCErrorFrames(t *testing.T) {
	in := []canbus.Frame{
		canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassProtocol, Protocol: canbus.ProtStuff | canbus.ProtTx, CountersValid: true, TxErrors: 9, RxErrors: 3}),
		canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassProtocol, Protocol: canbus.ProtForm}),
		canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassProtocol, Protocol: canbus.ProtBit}),
		canbus.NewErrorFrame(canbus.ErrorInfo{Class: canbus.ErrClassBusOff}),
	}
	var buf bytes.Buffer
	w := NewTRCWriter(&buf)
	for i := range in {
		if err := w.Write(&in[i]); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	if !strings.Contains(buf.String(), " ER  1        - ") {
		t.Fatal(buf.String())
	}
	r := NewTRCReader(&buf)
	want := []string{
		"protocol|counters protocol=stuff|tx location=0x0 tx=9 rx=3",
		"protocol|counters protocol=form location=0x0 tx=0 rx=0",
		"protocol|counters protocol=bit location=0x0 tx=0 rx=0",
		"protocol|counters protocol=unspecified location=0x0 tx=0 rx=0",
	}
	for i, s := range want {
		var f canbus.Frame
		if err := r.Read(&f); err != nil {
			t.Fatal(err)
		}
		info, ok := canbus.ParseError(&f)
		if !ok || info.String() != s {
			t.Errorf("frame %d: %q, want %q", i, info.String(), s)
		}
	}
}

func readTRC(t *testing.T, src string) []canbus.Frame {
	t.Helper()
	r, format, err := NewReader(strings.NewReader(src))
	if err != nil || format != FormatTRC {
		t.Fatalf("detected %q: %v", format, err)
	}
	var out []canbus.Frame
	for {
		var f canbus.Frame
		err := r.Read(&f)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
}

func notation(fs []canbus.Frame) string {
	var b strings.Builder
	for i := range fs {
		f := &fs[i]
		b.WriteString(f.Interface + " " + f.String())
		if f.Echo {
			b.WriteString(" tx")
		}
		b.WriteString(" " + f.Timestamp.UTC().Format("15:04:05.000000") + "\n")
	}
	return b.String()
}

// TestTRCVersions reads records of every file version as PEAK's tools
// lay them out.
func TestTRCVersions(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()
	cases := []struct{ name, src, want string }{
		{"1.0", `;##########################################################################
;   C:\trace.trc
;
;    Start time: 27.10.2026 08:15:00.0
;    PCAN-Net: PCI1
;
;   Columns description:
;----+- ---+--- ----+--- + -+ -- -- ...
     1)      1841  0001  8  00 11 22 33 44 55 66 77
     2)      1842  FFFFFFFF  4  00 00 00 0C
     3)      1843  1FFFFFFF 0  RTR
     4)      1844  0123  2  AB CD
`, "1 001#0011223344556677 00:00:01.841000\n1 1FFFFFFF#R 00:00:01.843000\n1 123#ABCD 00:00:01.844000\n"},
		{"1.1", `;$FILEVERSION=1.1
;$STARTTIME=46322.34375
;---+--   ----+----  --+--  ----+---  +  -+ -- -- -- -- -- -- --
     1)      1841.0  Rx         0001  8  00 11 22 33 44 55 66 77
     2)      1841.3  Tx     18FEF100  2  01 02
     3)      1842.0  Warng  FFFFFFFF  4  00 00 00 08  BUSHEAVY
     4)      1843.0  Rx         0100  3  RTR
     5)      1844.7  Rx         0100  0
`, "1 001#0011223344556677 08:15:01.841000\n1 18FEF100#0102 tx 08:15:01.841300\n1 100#R3 08:15:01.843000\n1 100# 08:15:01.844700\n"},
		{"1.2", `;$FILEVERSION=1.2
;$STARTTIME=46322.34375
     1)      1841.0 1  Rx         0001  2  00 11
     2)      1841.3 2  Tx     18FEF100  1  01
`, "1 001#0011 08:15:01.841000\n2 18FEF100#01 tx 08:15:01.841300\n"},
		{"1.3", `;$FILEVERSION=1.3
;$STARTTIME=46322.34375
     1)      1841.0 1  Rx         0001 -  2    00 11
     2)      1841.3 2  Tx     18FEF100 -  12   01 02 03 04 05 06 07 08
`, "1 001#0011 08:15:01.841000\n2 18FEF100#0102030405060708_C tx 08:15:01.841300\n"},
		{"2.0", `;$FILEVERSION=2.0
;$STARTTIME=46322.34375
;$COLUMNS=N,O,T,I,d,l,D
      1      1059.900 DT     0300 Rx 8  00 00 00 00 04 00 00 00
      2      1060.900 FB 18FEF100 Rx 12 01 02 03 04 05 06 07 08 09 0A 0B 0C
      3      1061.000 ST          Rx    00 00 00 08
      4      1062.000 RR     0300 Tx 2
`, "1 300#0000000004000000 08:15:01.059900\n1 18FEF100##10102030405060708090A0B0C 08:15:01.060900\n1 300#R2 tx 08:15:01.062000\n"},
		{"2.1", `;$FILEVERSION=2.1
;$STARTTIME=46322.34375
;$COLUMNS=N,O,T,B,I,d,R,L,D
      1      1059.900 DT  1     0300 Rx -  8    00 00 00 00 04 00 00 00
      2      1060.900 FD  2     0301 Rx -  9    01 02 03 04 05 06 07 08 09 0A 0B 0C
      3      1061.000 FE  1     0302 Tx -  1    AA
      4      1062.000 BI  1     0303 Rx -  0
      5      1063.000 EC  1     Rx    00 00
      6      1064.000 EV  1     Rx    some event
      7      1065.000 ER  1        - Rx -  5    02 01 0A 7F 80
      8      1066.000 ER  1        - Rx -  0
`, "1 300#0000000004000000 08:15:01.059900\n2 301##00102030405060708090A0B0C 08:15:01.060900\n1 302##2AA tx 08:15:01.061000\n1 303##3 08:15:01.062000\n" +
			"1 20000208#000004000000807F 08:15:01.065000\n1 20000008#0000000000000000 08:15:01.066000\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := notation(readTRC(t, c.src)); got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
	r := NewTRCReader(strings.NewReader(";$FILEVERSION=2.1\n"))
	_ = r.Read(&canbus.Frame{})
	if r.Version() != "2.1" {
		t.Fatal(r.Version())
	}
}

func TestTRCErrors(t *testing.T) {
	for _, src := range []string{
		";$FILEVERSION=3.0\n",
		";$FILEVERSION=2.1\n;$STARTTIME=x\n",
		";$FILEVERSION=1.1\n 1) 1.0 Rx 0001\n",
		";$FILEVERSION=1.1\n 1) x Rx 0001 1 00\n",
		";$FILEVERSION=1.1\n 1) 1.0 Rx 0801 1 00\n",
		";$FILEVERSION=1.1\n 1) 1.0 Rx 20000000 1 00\n",
		";$FILEVERSION=1.1\n 1) 1.0 Rx 0001 16 00\n",
		";$FILEVERSION=1.1\n 1) 1.0 Rx 0001 2 00\n",
		";$FILEVERSION=1.1\n 1) 1.0 Rx 0001 1 GG\n",
		";$FILEVERSION=1.1\n 1) 1.0 Rx\n",
		";$FILEVERSION=1.2\n 1) 1.0 x Rx 0001 1 00\n",
		";$FILEVERSION=1.3\n 1) 1.0 1 Rx 0001 -\n",
		";$FILEVERSION=1.1\n 1 1.0 Rx 0001 1 00\n",
		";$FILEVERSION=2.1\n;$COLUMNS=N,O,T,B,I,d,R,L,D\n 1 1.0 DT x 0001 Rx - 1 00\n",
		";$FILEVERSION=2.1\n;$COLUMNS=N,O,T,B,I,d,R,L,D\n 1 1.0 DT 1 0001 Rx - 16 00\n",
		";$FILEVERSION=2.1\n;$COLUMNS=N,O,T,B,I,d,R,D\n 1 1.0 DT 1 0001 Rx - 00\n",
		";$FILEVERSION=2.1\n;$COLUMNS=N,O,T,B,I,d,R,l,D\n 1 1.0 FD 1 0001 Rx - 13 00\n",
		";$FILEVERSION=2.1\n;$COLUMNS=N,O,T,B,I,d,R,l,D\n 1 1.0 DT 1 0001 Rx - 9 00\n",
		";$FILEVERSION=2.1\n;$COLUMNS=N,O,I\n 1 1.0 0001\n",
		";$FILEVERSION=2.1\n;$COLUMNS=N,O,T,B,I,d,R,L,D\n 1 x DT 1 0001 Rx - 1 00\n",
		";$FILEVERSION=2.1\n;$COLUMNS=N,O,T,B,I,d,R,L,D\n 1 1.0 DT 1 XYZ Rx - 1 00\n",
	} {
		r := NewTRCReader(strings.NewReader(src))
		var f canbus.Frame
		var pe *ParseError
		if err := r.Read(&f); !errors.As(err, &pe) {
			t.Errorf("%q: %v", src, err)
		}
	}
	// 2.x records before any header have no columns.
	r := NewTRCReader(strings.NewReader(";$FILEVERSION=1.0\n"))
	r.version = 21
	r.s = NewTRCReader(strings.NewReader(" 1 1.0 DT 1 0001 Rx - 1 00\n")).s
	if err := r.Read(&canbus.Frame{}); err == nil {
		t.Fatal("no error without columns")
	}
}

func TestTRCOLEDate(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("CET", 3600)
	defer func() { time.Local = old }()
	in := time.Date(2026, 10, 27, 20, 15, 0, 123000000, time.Local)
	if got := oleToTime(timeToOLE(in)); !got.Equal(in) {
		t.Fatalf("%v, want %v", got, in)
	}
	// The OLE date is the local wall clock.
	if d := timeToOLE(in) - (46322.84375 + 0.123/86400); d > 1e-10 || d < -1e-10 {
		t.Fatalf("%v", d)
	}
}
