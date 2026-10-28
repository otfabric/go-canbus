// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// realFrames are the frames of testdata/linux-7.0-*: sent with canbusctl
// on vcanxl0 (and one on vcan0) under Linux 7.0 and captured by tcpdump
// 4.99.6 / libpcap 1.10.6 (pcap) and dumpcap 4.6.4 (pcapng, both
// interfaces).
var realFrames = []string{
	"123#DEADBEEF",
	"1234ABCD#1122",
	"123#R4",
	"456##3112233445566778899AABBCC",
	"05242#81:01:12345678#AABBCCDDEE",
	"007FF#82:FF:FFFFFFFF#00",
	"001A0#80:04:00C0FFEE#" + strings.Repeat("5A", 2048),
}

func readAll(t *testing.T, path string) (kinds []string, ifaces []string, times []time.Time) {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	r, _, err := NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	var f canbus.Frame
	var x canbus.XLFrame
	for {
		isXL, err := r.(XLReader).ReadAny(&f, &x)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if isXL {
			kinds, ifaces, times = append(kinds, x.String()), append(ifaces, x.Interface), append(times, x.Timestamp)
		} else {
			kinds, ifaces, times = append(kinds, f.String()), append(ifaces, f.Interface), append(times, f.Timestamp)
		}
	}
}

func TestRealCaptureTcpdump(t *testing.T) {
	got, _, times := readAll(t, "testdata/linux-7.0-tcpdump.pcap")
	if strings.Join(got, "\n") != strings.Join(realFrames, "\n") {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(realFrames, "\n"))
	}
	for i := 1; i < len(times); i++ {
		if times[i].Before(times[i-1]) || times[i].Year() != 2026 {
			t.Fatalf("timestamp %d: %v", i, times[i])
		}
	}
}

func TestRealCaptureDumpcap(t *testing.T) {
	got, ifaces, _ := readAll(t, "testdata/linux-7.0-dumpcap.pcapng")
	want := append([]string{"321#0102"}, realFrames...)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ifaces[0] != "vcan0" || ifaces[len(ifaces)-1] != "vcanxl0" {
		t.Fatalf("interfaces %v", ifaces)
	}
}

// pcapPackets returns the packet bytes of a little-endian pcap file.
func pcapPackets(t *testing.T, b []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for b = b[pcapHeaderLen:]; len(b) >= pcapRecordLen; {
		n := int(binary.LittleEndian.Uint32(b[8:]))
		out = append(out, b[pcapRecordLen:pcapRecordLen+n])
		b = b[pcapRecordLen+n:]
	}
	return out
}

// TestXLWriterMatchesLibpcap: the CAN XL packets PCAPWriter writes are
// byte-identical to libpcap's capture of the same frames.
func TestXLWriterMatchesLibpcap(t *testing.T) {
	real, err := os.ReadFile("testdata/linux-7.0-tcpdump.pcap")
	if err != nil {
		t.Fatal(err)
	}
	want := pcapPackets(t, real)
	var buf bytes.Buffer
	w, _ := NewPCAPWriter(&buf)
	n := 0
	for _, s := range realFrames {
		if x, err := canbus.ParseXLFrame(s); err == nil {
			if err := w.WriteXL(&x); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	_ = w.Close()
	got := pcapPackets(t, buf.Bytes())
	if len(got) != 3 || n != 3 {
		t.Fatalf("%d packets", len(got))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[len(want)-3+i]) {
			t.Errorf("packet %d:\n got % x\nwant % x", i, got[i][:min(24, len(got[i]))], want[len(want)-3+i][:min(24, len(want[len(want)-3+i]))])
		}
	}
}

func TestPCAPNGWriterInterfaces(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewPCAPNGWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(1800000000, 123456789)
	a, _ := canbus.NewStandard(1, []byte{1})
	a.Interface, a.Timestamp = "can0", ts
	b := a
	b.Interface = "can1"
	x := canbus.XLFrame{Priority: 2, Data: []byte{2}, Interface: "can0", Timestamp: ts.Add(time.Nanosecond)}
	c := a
	c.Interface = ""
	for _, err := range []error{w.Write(&a), w.Write(&b), w.WriteXL(&x), w.Write(&c), w.Close()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteXL(&canbus.XLFrame{}); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatalf("invalid frame: %v", err)
	}
	if n := bytes.Count(buf.Bytes(), []byte{1, 0, 0, 0}); n < 3 {
		t.Fatalf("interface blocks: %d", n)
	}
	r, _ := NewPCAPNGReader(bytes.NewReader(buf.Bytes()))
	var f canbus.Frame
	var g canbus.XLFrame
	var got []string
	for {
		isXL, err := r.ReadAny(&f, &g)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if isXL {
			got = append(got, g.Interface+" "+g.String()+" "+g.Timestamp.UTC().Format(time.RFC3339Nano))
		} else {
			got = append(got, f.Interface+" "+f.String()+" "+f.Timestamp.UTC().Format(time.RFC3339Nano))
		}
	}
	want := []string{
		"can0 001#01 " + ts.UTC().Format(time.RFC3339Nano),
		"can1 001#01 " + ts.UTC().Format(time.RFC3339Nano),
		"can0 00002#80:00:00000000#02 " + ts.Add(time.Nanosecond).UTC().Format(time.RFC3339Nano),
		" 001#01 " + ts.UTC().Format(time.RFC3339Nano),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPCAPXLMalformed(t *testing.T) {
	var x canbus.XLFrame
	for _, b := range [][]byte{
		{0, 0, 2, 0x42, 0x80, 0, 1, 0},                   // short header
		{0, 0, 2, 0x42, 0x80, 0, 0, 0, 0, 0, 0, 0},       // length 0
		{0, 0, 2, 0x42, 0x80, 0, 0x01, 0x08, 0, 0, 0, 0}, // length 2049
		{1, 0, 2, 0x42, 0x80, 0, 1, 0, 0, 0, 0, 0, 1},    // reserved priority bits
		{0, 0, 2, 0x42, 0x84, 0, 1, 0, 0, 0, 0, 0, 1},    // reserved flag
	} {
		if _, err := decodeSocketCANAny(b, &canbus.Frame{}, &x); err == nil {
			t.Errorf("% x accepted", b)
		}
	}
	// A truncated capture (snap length) leaves the missing payload zero.
	b := []byte{0, 0, 2, 0x42, 0x80, 0, 4, 0, 0, 0, 0, 0, 0xAA}
	x.Data = []byte{9, 9, 9, 9, 9, 9}
	if _, err := decodeSocketCANAny(b, &canbus.Frame{}, &x); err != nil || !bytes.Equal(x.Data, []byte{0xAA, 0, 0, 0}) {
		t.Fatalf("truncated: % x %v", x.Data, err)
	}
	// Writing an invalid frame fails.
	w, _ := NewPCAPWriter(io.Discard)
	if err := w.WriteXL(&canbus.XLFrame{Priority: 0x800, Data: []byte{1}}); !errors.Is(err, canbus.ErrInvalidFrame) {
		t.Fatal(err)
	}
}
