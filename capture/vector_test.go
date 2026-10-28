// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// vectorRoundTrip writes frames() in format and reads them back: the
// frames must survive except the Linux error class, which ASC and BLF do
// not carry, and the interface name, which becomes a channel number.
func vectorRoundTrip(t *testing.T, format Format, precision time.Duration) {
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
	r, got, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil || got != format {
		t.Fatalf("detected %q: %v", got, err)
	}
	for i := range in {
		var f canbus.Frame
		if err := r.Read(&f); err != nil {
			t.Fatalf("%s frame %d: %v\n%s", format, i, err, buf.String())
		}
		want := in[i]
		if want.IsError() {
			if !f.IsError() {
				t.Fatalf("%s frame %d: error frame lost", format, i)
			}
		} else if !same(&f, &want) {
			t.Errorf("%s frame %d: %v (%v), want %v", format, i, f, f.Flags, want)
		}
		if d := f.Timestamp.Sub(want.Timestamp); d > precision || d < -precision {
			t.Errorf("%s frame %d: timestamp off by %v", format, i, d)
		}
		if f.Interface != "1" {
			t.Errorf("%s: interface %q", format, f.Interface)
		}
		if f.Echo != want.Echo {
			t.Errorf("%s frame %d: echo %v", format, i, f.Echo)
		}
	}
	var f canbus.Frame
	if err := r.Read(&f); err != io.EOF {
		t.Fatalf("%s: after the last frame: %v", format, err)
	}
}

func TestASCRoundTrip(t *testing.T) { vectorRoundTrip(t, FormatASC, time.Microsecond) }
func TestBLFRoundTrip(t *testing.T) { vectorRoundTrip(t, FormatBLF, time.Microsecond) }

func TestBLFSeekableHeaderAndEmpty(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "*.blf")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, _ := NewBLFWriter(f)
	in := frames(t)
	for i := range in {
		_ = w.Write(&in[i])
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.Name())
	if n := binary.LittleEndian.Uint32(b[32:]); n != uint32(len(in)) {
		t.Fatalf("object count %d", n)
	}
	if size := binary.LittleEndian.Uint64(b[16:]); size != uint64(len(b)) {
		t.Fatalf("file size %d, actual %d", size, len(b))
	}
	var empty bytes.Buffer
	w, _ = NewBLFWriter(&empty)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewBLFReader(bytes.NewReader(empty.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Read(&canbus.Frame{}); err != io.EOF {
		t.Fatal(err)
	}
	var ea bytes.Buffer
	aw := NewASCWriter(&ea)
	_ = aw.Close()
	if !strings.Contains(ea.String(), "End TriggerBlock") {
		t.Fatal(ea.String())
	}
}

// TestASCVariants reads lines as Vector tools write them.
func TestASCVariants(t *testing.T) {
	src := `date Mon Oct 27 08:15:00.123 pm 2026
base hex  timestamps absolute
internal events logged
// version 13.0.0
Begin Triggerblock Mon Oct 27 08:15:00.123 pm 2026
   0.000000 Start of measurement
   0.001000 1  Statistic: D 0 R 0 XD 0 XR 0 E 0 O 0 B 0.00%
   0.010000 1  123             Rx   d 3 11 22 33  Length = 0 BitCount = 0 ID = 291
   0.020000 2  1ABCDEF0x       Tx   d 8 01 02 03 04 05 06 07 08
   0.030000 1  7FF             Rx   r 4
   0.040000 CANFD   1 Rx      10                          SomeMsg 1 0 9 12 00 01 02 03 04 05 06 07 08 09 0a 0b   0 0 0 0 0 0 0 0
   0.050000 1  ErrorFrame
   0.060000 1  0A1             Rx   d f 01 02 03 04 05 06 07 08
End TriggerBlock
`
	r, f, err := NewReader(strings.NewReader(src))
	if err != nil || f != FormatASC {
		t.Fatal(f, err)
	}
	var got []canbus.Frame
	for {
		var fr canbus.Frame
		err := r.Read(&fr)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fr)
	}
	if len(got) != 6 {
		t.Fatalf("%d frames: %v", len(got), got)
	}
	want := []string{"123#112233", "1ABCDEF0#0102030405060708", "7FF#R4", "010##1000102030405060708090A0B", "20000008#0000000000000000", "0A1#0102030405060708_F"}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("frame %d: %s, want %s", i, got[i].String(), w)
		}
	}
	base := time.Date(2026, 10, 27, 20, 15, 0, 123000000, time.Local)
	if !got[0].Timestamp.Equal(base.Add(10*time.Millisecond)) || !got[1].Echo || got[1].Interface != "2" {
		t.Fatal(got[0].Timestamp, got[1])
	}
	dec := "base dec  timestamps absolute\n 0.5 1  291 Rx d 2 10 255\n"
	r = NewASCReader(strings.NewReader(dec))
	var fr canbus.Frame
	if err := r.Read(&fr); err != nil || fr.String() != "123#0AFF" || fr.Timestamp != time.Unix(0, 500000000) {
		t.Fatal(fr, err)
	}
	for _, bad := range []string{"0.1 1 800 Rx d 1 00", "0.1 1 123 Rx q 1 00", "0.1 1 123 Rx d 8 00",
		"0.1 1 123 Rx d", "0.1 1 123 Rx d z 00", "0.1 CANFD 1 Rx 10 0 0 9 13 00", "0.1 CANFD 1 Rx", "0.1 1 123 Rx d 1 zz",
		"0.1 1 20000000x Rx d 0", "0.1 CANFD 1 Rx 10 0 0 z 12", "0.1 CANFD 1 Rx 10 0 0 1"} {
		r := NewASCReader(strings.NewReader(bad + "\n"))
		if err := r.Read(&fr); err == nil || err == io.EOF {
			t.Errorf("accepted %q: %v", bad, err)
		}
	}
}

func TestBLFErrors(t *testing.T) {
	if _, err := NewBLFReader(strings.NewReader("LOGX")); err == nil {
		t.Fatal("bad signature")
	}
	h := make([]byte, 8)
	copy(h, "LOGG")
	binary.LittleEndian.PutUint32(h[4:], 10)
	if _, err := NewBLFReader(bytes.NewReader(h)); err == nil {
		t.Fatal("bad header size")
	}
	binary.LittleEndian.PutUint32(h[4:], 144)
	if _, err := NewBLFReader(bytes.NewReader(h)); err == nil {
		t.Fatal("short header")
	}
	// A valid file, then corruptions of it.
	var buf bytes.Buffer
	w, _ := NewBLFWriter(&buf)
	in := frames(t)
	for i := range in {
		_ = w.Write(&in[i])
	}
	_ = w.Close()
	good := buf.Bytes()
	for _, cut := range []int{150, 170, len(good) - 9} {
		r, err := NewBLFReader(bytes.NewReader(good[:cut]))
		if err != nil {
			continue
		}
		for err == nil {
			err = r.Read(&canbus.Frame{})
		}
		if err == io.EOF {
			t.Errorf("truncation at %d not reported", cut)
		}
	}
	bad := append([]byte(nil), good...)
	copy(bad[144:], "XXXX")
	r, _ := NewBLFReader(bytes.NewReader(bad))
	if err := r.Read(&canbus.Frame{}); err == nil {
		t.Fatal("bad object signature accepted")
	}
	bad = append([]byte(nil), good...)
	binary.LittleEndian.PutUint16(bad[144+16:], 7) // compression method
	r, _ = NewBLFReader(bytes.NewReader(bad))
	if err := r.Read(&canbus.Frame{}); err == nil {
		t.Fatal("unknown compression accepted")
	}
}

// blfFile builds a BLF file with the given objects in one uncompressed
// container and one object at file level.
func blfFile(t *testing.T, objs ...[]byte) []byte {
	t.Helper()
	le := binary.LittleEndian
	w := &BLFWriter{start: time.Unix(1700000000, 0)}
	h := w.fileHeader(time.Time{})
	var body []byte
	for _, o := range objs {
		body = append(body, o...)
	}
	cont := make([]byte, 32)
	copy(cont, "LOBJ")
	le.PutUint16(cont[4:], 16)
	le.PutUint16(cont[6:], 1)
	le.PutUint32(cont[8:], uint32(32+len(body)))
	le.PutUint32(cont[12:], blfContType)
	le.PutUint16(cont[16:], blfCompressNone)
	le.PutUint32(cont[24:], uint32(len(body)))
	out := append(h, cont...)
	out = append(out, body...)
	out = append(out, make([]byte, (32+len(body))%4)...)
	return out
}

// blfObj builds an object with a v1 (or v2) header and timestamp ts in
// 10 µs units.
func blfObj(typ uint32, version uint16, ts uint64, body []byte) []byte {
	le := binary.LittleEndian
	hl := 16
	if version == 2 {
		hl = 24
	}
	size := 16 + hl + len(body)
	o := make([]byte, 16+hl)
	copy(o, "LOBJ")
	le.PutUint16(o[4:], uint16(16+hl))
	le.PutUint16(o[6:], version)
	le.PutUint32(o[8:], uint32(size))
	le.PutUint32(o[12:], typ)
	le.PutUint32(o[16:], blfFlagTenMicros)
	le.PutUint64(o[24:], ts)
	o = append(o, body...)
	return append(o, make([]byte, size%4)...)
}

func TestBLFObjectTypes(t *testing.T) {
	le := binary.LittleEndian
	msg2 := make([]byte, 16)
	le.PutUint16(msg2[0:], 3)
	msg2[2], msg2[3] = 0x01, 2
	le.PutUint32(msg2[4:], 0x123)
	msg2[8], msg2[9] = 0xAA, 0xBB
	fd := make([]byte, 20+64)
	le.PutUint16(fd[0:], 1)
	fd[3] = 9 // DLC 9: 12 bytes
	le.PutUint32(fd[4:], 0x80000010)
	fd[13], fd[14] = 0x01|0x02|0x04, 12
	fd[20] = 0x55
	fdClassic := make([]byte, 20+64)
	fdClassic[2], fdClassic[3] = 0x80, 3 // remote, not EDL
	le.PutUint32(fdClassic[4:], 0x7FF)
	fdData := make([]byte, 20+64)
	fdData[3] = 2
	le.PutUint32(fdData[4:], 0x5)
	fdData[20], fdData[21] = 1, 2
	rem64 := make([]byte, 40)
	rem64[1] = 4
	le.PutUint32(rem64[4:], 0x9)
	le.PutUint32(rem64[12:], 0x0010)
	cls64 := make([]byte, 40+8)
	cls64[1], cls64[2] = 8, 8
	le.PutUint32(cls64[4:], 0x8)
	cls64[40] = 0xEE
	marker := make([]byte, 8) // GLOBAL_MARKER: skipped
	file := blfFile(t,
		blfObj(blfCANMessage2, 1, 100, msg2),
		blfObj(96, 1, 150, marker),
		blfObj(blfCANFDMessage, 2, 200, fd),
		blfObj(blfCANFDMessage, 1, 300, fdClassic),
		blfObj(blfCANFDMessage, 1, 350, fdData),
		blfObj(blfCANFD64, 1, 400, rem64),
		blfObj(blfCANFD64, 1, 500, cls64),
	)
	r, err := NewBLFReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"123#AABB", "00000010##3550000000000000000000000", "7FF#R3", "005#0102", "009#R4", "008#EE00000000000000"}
	for i, w := range want {
		var f canbus.Frame
		if err := r.Read(&f); err != nil {
			t.Fatal(i, err)
		}
		if f.String() != w {
			t.Errorf("object %d: %s, want %s", i, f.String(), w)
		}
		if i == 0 && (f.Interface != "3" || !f.Echo || f.Timestamp != time.Unix(1700000000, 1000000)) {
			t.Errorf("metadata %+v", f)
		}
	}
	if err := r.Read(&canbus.Frame{}); err != io.EOF {
		t.Fatal(err)
	}
	// Short objects and an unknown header version are errors.
	for _, o := range [][]byte{
		blfObj(blfCANMessage, 1, 0, make([]byte, 4)),
		blfObj(blfCANFDMessage, 1, 0, make([]byte, 4)),
		blfObj(blfCANFD64, 1, 0, make([]byte, 4)),
		blfObj(blfCANErrorExt, 1, 0, make([]byte, 4)),
		blfObj(blfCANMessage, 3, 0, make([]byte, 16)),
		blfObj(blfCANFDMessage, 1, 0, []byte{0, 0, 0, 15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01, 4, 0, 0, 0, 0, 0, 1, 2, 3, 4}),
	} {
		r, _ := NewBLFReader(bytes.NewReader(blfFile(t, o)))
		if err := r.Read(&canbus.Frame{}); err == nil || err == io.EOF {
			t.Errorf("accepted a bad object: %v", err)
		}
	}
}

func TestFormatFromPath(t *testing.T) {
	for p, want := range map[string]Format{"a.log": FormatCandump, "b.JSONL": FormatJSONL, "c.pcap": FormatPCAP,
		"d.pcapng": FormatPCAPNG, "e.asc": FormatASC, "f.blf": FormatBLF, "g.txt": ""} {
		if got := FormatFromPath(p); got != want {
			t.Errorf("%s: %q", p, got)
		}
	}
}
