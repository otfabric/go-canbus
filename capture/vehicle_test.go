// SPDX-License-Identifier: MIT

package capture

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/internal/corpus"
	"github.com/otfabric/go-canbus/virtual"
)

// The tests in this file run go-canbus against traffic recorded on real
// vehicles by others (testdata/captures, see its README): ten passenger
// cars of the CANdid dataset and the ten CAN domains of the CarDS
// electric vehicle. They are evidence that the readers and writers carry
// what real recordings hold, not a conformance claim.

func vehicleManifest(t *testing.T) *corpus.Manifest {
	t.Helper()
	m, err := corpus.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.Verify(m); err != nil {
		t.Fatal(err)
	}
	return m
}

// readCandumpFile reads every frame of a candump log.
func readCandumpFile(t *testing.T, path string) ([]canbus.Frame, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := NewCandumpReader(bytes.NewReader(raw))
	var out []canbus.Frame
	for {
		var f canbus.Frame
		err := r.Read(&f)
		if errors.Is(err, io.EOF) {
			return out, raw
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out = append(out, f)
	}
}

// statsOf counts what the frames and lines of a trace hold.
func statsOf(frames []canbus.Frame, raw []byte) corpus.Stats {
	var s corpus.Stats
	ifs := map[string]bool{}
	var prev time.Time
	for i := range frames {
		f := &frames[i]
		s.Frames++
		ifs[f.Interface] = true
		if f.IsExtended() {
			s.Extended++
		} else {
			s.Standard++
		}
		for _, c := range []struct {
			flag canbus.Flags
			n    *int
		}{{canbus.FlagFD, &s.FD}, {canbus.FlagBRS, &s.BRS}, {canbus.FlagESI, &s.ESI}, {canbus.FlagRemote, &s.Remote}, {canbus.FlagError, &s.Error}} {
			if f.Flags&c.flag != 0 {
				*c.n++
			}
		}
		if f.Echo {
			s.TxMarked++
		}
		if f.Timestamp.Before(prev) {
			s.OutOfOrder++
		}
		prev = f.Timestamp
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.HasSuffix(line, " R") || strings.HasSuffix(line, " T") {
			s.Tokens++
		}
	}
	for k := range ifs {
		s.Interfaces = append(s.Interfaces, k)
	}
	slices.Sort(s.Interfaces)
	return s
}

// TestVehicleCapturesDecode: every recorded line decodes, to the counts the
// manifest holds (cross-checked with Wireshark in
// TestWiresharkReadsVehicleCaptures).
func TestVehicleCapturesDecode(t *testing.T) {
	m := vehicleManifest(t)
	kinds := map[string]bool{}
	for _, tf := range m.Traces() {
		frames, raw := readCandumpFile(t, corpus.Path(tf.Path))
		got := statsOf(frames, raw)
		want := *tf.Stats
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		if !bytes.Equal(gj, wj) {
			t.Errorf("%s:\n got  %s\n want %s", tf.Path, gj, wj)
		}
		if got.ESI > 0 {
			kinds["esi"] = true
		}
		if got.FD > 0 && got.Extended > 0 {
			kinds["fd+ext"] = true
		}
		if got.OutOfOrder > 0 {
			kinds["out-of-order"] = true
		}
		if got.TxMarked > 0 {
			kinds["T"] = true
		}
	}
	// The corpus is chosen to hold these: guard against a cut losing them.
	for _, k := range []string{"esi", "fd+ext", "out-of-order", "T"} {
		if !kinds[k] {
			t.Errorf("no recorded frame of kind %q", k)
		}
	}
}

// TestVehicleCapturesCandumpRoundTrip: a recorded log read and written
// again is byte for byte the original. Logs with direction tokens need
// SetDirection; without it their "T" marks were lost (go-canbus 0.8.0 and
// earlier wrote no token).
func TestVehicleCapturesCandumpRoundTrip(t *testing.T) {
	m := vehicleManifest(t)
	for _, tf := range m.Traces() {
		frames, raw := readCandumpFile(t, corpus.Path(tf.Path))
		write := func(dir bool) []byte {
			var buf bytes.Buffer
			w := NewCandumpWriter(&buf, "")
			w.SetDirection(dir)
			for i := range frames {
				if err := w.Write(&frames[i]); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			return buf.Bytes()
		}
		tokens := tf.Stats.Tokens > 0
		if got := write(tokens); !bytes.Equal(got, raw) {
			t.Errorf("%s: written back differs from the recording (direction tokens %v)", tf.Path, tokens)
		}
		if tokens {
			plain := write(false)
			if bytes.Contains(plain, []byte(" T\n")) || bytes.Contains(plain, []byte(" R\n")) {
				t.Errorf("%s: tokens written without SetDirection", tf.Path)
			}
		}
	}
}

// TestVehicleCapturesFormats converts every recorded frame to each trace
// format and back. What a format cannot carry is named: pcap has no
// interface names, pcap and pcapng no direction, ASC, BLF and TRC number
// channels (vectorChannel) and BLF clamps frames recorded before its
// start time (see TestVehicleCapturesBLFStart).
func TestVehicleCapturesFormats(t *testing.T) {
	pinLocalUTC(t)
	m := vehicleManifest(t)
	for _, tf := range m.Traces() {
		frames, _ := readCandumpFile(t, corpus.Path(tf.Path))
		for _, format := range []Format{FormatJSONL, FormatPCAP, FormatPCAPNG, FormatASC, FormatBLF, FormatTRC} {
			var buf bytes.Buffer
			w, err := NewWriter(&buf, format)
			if err != nil {
				t.Fatal(err)
			}
			for i := range frames {
				if err := w.Write(&frames[i]); err != nil {
					t.Fatalf("%s %s frame %d: %v", tf.Path, format, i+1, err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			got, detected, err := readTrace(buf.Bytes())
			if err != nil || detected != format || len(got) != len(frames) {
				t.Fatalf("%s %s: read back as %s, %d of %d frames: %v", tf.Path, format, detected, len(got), len(frames), err)
			}
			vector := format == FormatASC || format == FormatBLF || format == FormatTRC
			channels := map[string]string{}
			start := frames[0].Timestamp.Truncate(time.Millisecond)
			for i := range frames {
				w, g := &frames[i], &got[i].f
				if !same(g, w) {
					t.Fatalf("%s %s frame %d: %v, want %v", tf.Path, format, i+1, g, w)
				}
				wantIf, wantEcho, wantTS := w.Interface, w.Echo, w.Timestamp
				switch format {
				case FormatPCAP:
					wantIf, wantEcho = "", false
				case FormatPCAPNG:
					wantEcho = false
				case FormatBLF:
					if wantTS.Before(start) {
						wantTS = start
					}
				case FormatCandump, FormatJSONL, FormatASC, FormatTRC:
				}
				if vector {
					wantIf = strconv.Itoa(vectorChannel(w.Interface))
					if c, ok := channels[g.Interface]; ok && c != w.Interface {
						t.Fatalf("%s %s: interfaces %s and %s both written as channel %s", tf.Path, format, c, w.Interface, g.Interface)
					}
					channels[g.Interface] = w.Interface
				}
				if g.Interface != wantIf || g.Echo != wantEcho || !g.Timestamp.Equal(wantTS) {
					t.Fatalf("%s %s frame %d: %q echo %v at %v, want %q echo %v at %v", tf.Path, format, i+1,
						g.Interface, g.Echo, g.Timestamp, wantIf, wantEcho, wantTS)
				}
			}
			if vector && len(channels) != len(tf.Stats.Interfaces) {
				t.Errorf("%s %s: %d channels for %d recorded interfaces", tf.Path, format, len(channels), len(tf.Stats.Interfaces))
			}
		}
	}
}

// TestVehicleCapturesBLFStart pins a property of every CANdid recording:
// its first line is newer than the lines after it (12–23 ms, the logger
// wrote one frame early). BLF object times are unsigned offsets from the
// file's start time, so the BLF writer starts at the first frame and
// writes the older frames at the start. ASC and TRC keep their times
// (TestVehicleCapturesFormats).
func TestVehicleCapturesBLFStart(t *testing.T) {
	m := vehicleManifest(t)
	clamped := 0
	for _, tf := range m.Traces() {
		if tf.Source != "candid" {
			continue
		}
		frames, _ := readCandumpFile(t, corpus.Path(tf.Path))
		if tf.Stats.OutOfOrder != 1 || !frames[1].Timestamp.Before(frames[0].Timestamp) {
			t.Fatalf("%s: the first line is not the out-of-order one", tf.Path)
		}
		start := frames[0].Timestamp.Truncate(time.Millisecond)
		for i := range frames {
			if frames[i].Timestamp.Before(start) {
				clamped++
			}
		}
	}
	if clamped == 0 {
		t.Fatal("no recorded frame is older than its trace's first frame")
	}
	t.Logf("%d CANdid frames are older than their trace's BLF start", clamped)
}

// TestVehicleCapturesReplay replays a recording with its out-of-order first
// frame at 20 times its speed: every frame is sent once, in file order.
func TestVehicleCapturesReplay(t *testing.T) {
	path := corpus.Path("candid/9_brakes_CAN.head500.log")
	frames, raw := readCandumpFile(t, path)
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	tx, _ := vb.Open(virtual.WithQueueSize(4096))
	rx, _ := vb.Open(virtual.WithQueueSize(4096))
	st, err := Replay(t.Context(), NewCandumpReader(bytes.NewReader(raw)), tx, ReplayOptions{Speed: 20})
	if err != nil || st.Sent != len(frames) {
		t.Fatalf("sent %d of %d: %v", st.Sent, len(frames), err)
	}
	for i := range frames {
		var f canbus.Frame
		if err := rx.Receive(t.Context(), &f); err != nil {
			t.Fatal(err)
		}
		if !same(&f, &frames[i]) {
			t.Fatalf("frame %d: %v, want %v", i+1, f, frames[i])
		}
	}
	span := frames[len(frames)-1].Timestamp.Sub(frames[1].Timestamp)
	if min := span / 20 / 2; st.Elapsed < min {
		t.Fatalf("replayed in %v, recording spans %v", st.Elapsed, span)
	}
}

// TestWiresharkReadsVehicleCaptures: Wireshark's own candump reader and
// CAN dissector decode every recorded line to the frame this package
// reads (interface included), and dissect the pcapng this package writes
// of them the same way.
func TestWiresharkReadsVehicleCaptures(t *testing.T) {
	bin := tshark(t)
	m := vehicleManifest(t)
	for _, tf := range m.Traces() {
		frames, _ := readCandumpFile(t, corpus.Path(tf.Path))
		want := make([]string, len(frames))
		for i := range frames {
			want[i] = frames[i].Interface + " " + frames[i].String()
		}
		out, err := exec.Command(bin, "-r", corpus.Path(tf.Path), "-T", "json").Output()
		if err != nil {
			t.Fatalf("tshark %s: %v", tf.Path, err)
		}
		if got := tsharkFrames(t, out); !sameFrames(got, want) {
			t.Errorf("%s: Wireshark's candump reader differs:\n%s", tf.Path, firstDiff(got, want))
		}
		got := wiresharkFrames(t, FormatPCAPNG, func(w XLWriter) {
			for i := range frames {
				if err := w.Write(&frames[i]); err != nil {
					t.Fatal(err)
				}
			}
		})
		if !sameFrames(got, want) {
			t.Errorf("%s: Wireshark dissects the pcapng differently:\n%s", tf.Path, firstDiff(got, want))
		}
	}
}

func tsharkFrames(t *testing.T, out []byte) []string {
	t.Helper()
	var packets []struct {
		Source struct {
			Layers map[string]json.RawMessage `json:"layers"`
		} `json:"_source"`
	}
	if err := json.Unmarshal(out, &packets); err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(packets))
	for i, p := range packets {
		got[i] = dissected(t, p.Source.Layers)
	}
	return got
}

func firstDiff(got, want []string) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return "frame " + strconv.Itoa(i+1) + ": " + short([]string{got[i]}) + "\nwant " + short([]string{want[i]})
		}
	}
	return "lengths " + strconv.Itoa(len(got)) + " and " + strconv.Itoa(len(want))
}

// TestExtendedVehicleCorpus streams the whole recordings of the extended
// corpus (make captures-extended): each has the SHA-256 the manifest records, decodes
// to the recorded number of frames, and is written back byte for byte.
// Skipped without the corpus, unless CANBUS_REQUIRE_CAPTURES=1.
func TestExtendedVehicleCorpus(t *testing.T) {
	dir, ok := corpus.ExtendedDir()
	if !ok {
		if os.Getenv(corpus.RequireEnv) == "1" {
			t.Fatalf("extended corpus %s missing: run make captures-extended", dir)
		}
		t.Skipf("extended corpus %s missing: make captures-extended fetches it", dir)
	}
	m := vehicleManifest(t)
	for _, e := range m.Extended {
		path := filepath.Join(dir, filepath.FromSlash(e.Name))
		if _, err := os.Stat(path); err != nil {
			if os.Getenv(corpus.RequireEnv) == "1" {
				t.Errorf("%s: %v", e.Name, err)
			}
			continue
		}
		t.Run(e.Name, func(t *testing.T) { checkExtended(t, path, e) })
	}
}

func checkExtended(t *testing.T, path string, e corpus.Extended) {
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	in := sha256.New()
	first, err := bufio.NewReader(io.TeeReader(io.LimitReader(fh, 4096), in)).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	in.Reset()
	// Some recordings (TU/e) do not end their last line with a newline;
	// the writer always does. Hold the last byte back to compare.
	st, err := fh.Stat()
	if err != nil {
		t.Fatal(err)
	}
	last := make([]byte, 1)
	if _, err := fh.ReadAt(last, st.Size()-1); err != nil {
		t.Fatal(err)
	}
	out := &holdLast{h: sha256.New()}
	w := NewCandumpWriter(out, "")
	line := strings.TrimSpace(first)
	w.SetDirection(strings.HasSuffix(line, " R") || strings.HasSuffix(line, " T"))
	r := NewCandumpReader(io.TeeReader(fh, in))
	n := 0
	var f canbus.Frame
	for {
		err := r.Read(&f)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n++
		if err := w.Write(&f); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(in.Sum(nil)); got != e.SHA256 {
		t.Fatalf("SHA-256 %s, manifest %s", got, e.SHA256)
	}
	if n != e.Frames {
		t.Errorf("%d frames, manifest %d", n, e.Frames)
	}
	if last[0] == '\n' {
		out.h.Write(out.last[:out.n])
	}
	if got := hex.EncodeToString(out.h.Sum(nil)); got != e.SHA256 {
		t.Errorf("written back: SHA-256 %s, the recording %s", got, e.SHA256)
	}
}

// holdLast hashes everything written to it except the last byte.
type holdLast struct {
	h    hash.Hash
	last [1]byte
	n    int
}

func (w *holdLast) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.h.Write(w.last[:w.n])
	w.h.Write(p[:len(p)-1])
	w.last[0], w.n = p[len(p)-1], 1
	return len(p), nil
}

// vehicleSeeds adds the first lines of every recorded excerpt to a fuzz
// target's corpus: real candump lines of ten cars and the CarDS vehicle
// (CAN FD with BRS and ESI, 29-bit identifiers, direction tokens, a first
// line newer than the second).
func vehicleSeeds(f *testing.F) {
	m, err := corpus.Load()
	if err != nil {
		f.Fatal(err)
	}
	for _, tf := range m.Traces() {
		raw, err := os.ReadFile(corpus.Path(tf.Path))
		if err != nil {
			f.Fatal(err)
		}
		lines := bytes.SplitAfter(raw, []byte("\n"))
		f.Add(bytes.Join(lines[:min(len(lines), 40)], nil))
	}
}

// TestVehicleLinesMalformed cuts, corrupts and stretches recorded lines:
// the parser never panics, and every line it accepts is one it would
// write back the same way (no silent reinterpretation).
func TestVehicleLinesMalformed(t *testing.T) {
	m := vehicleManifest(t)
	var lines []string
	for _, tf := range m.Traces() {
		raw, _ := os.ReadFile(corpus.Path(tf.Path))
		all := strings.Split(strings.TrimSpace(string(raw)), "\n")
		lines = append(lines, all[:min(len(all), 25)]...)
	}
	accepted, refused := 0, 0
	check := func(line string) {
		var f canbus.Frame
		if err := ParseCandumpLine(line, &f); err != nil {
			refused++
			return
		}
		accepted++
		if err := f.Validate(); err != nil && !f.IsError() {
			t.Fatalf("%q accepted as invalid frame %v: %v", line, f, err)
		}
	}
	for _, line := range lines {
		for i := range len(line) {
			check(line[:i])
		}
		check(line + "00")                                                 // one byte more
		check(line + " X")                                                 // unknown token
		check(strings.Replace(line, "#", "##F", 1))                        // FD flags nibble with every bit
		check(strings.Replace(line, "#", "#"+strings.Repeat("00", 64), 1)) // beyond 64 bytes
		check(strings.Replace(line, ")", ".1)", 1))                        // two fractions
		check(strings.Replace(line, "(", "(-", 1))                         // negative time
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("%d accepted, %d refused", accepted, refused)
	}
}

func TestVectorChannel(t *testing.T) {
	for iface, want := range map[string]int{
		"": 1, "1": 1, "2": 2, "17": 17, "0": 1, "can0": 1, "can4": 5, "can9": 10, "vcan3": 4,
		"slcan0": 1, "can": 1, "any": 1, "can1234567890": 1, "300": 300, "can-1": 2,
	} {
		if got := vectorChannel(iface); got != want {
			t.Errorf("vectorChannel(%q) = %d, want %d", iface, got, want)
		}
	}
	// A BLF classical channel has 16 bits.
	f, _ := canbus.NewStandard(1, nil)
	f.Interface = "70000"
	bw, _ := NewBLFWriter(io.Discard)
	if err := bw.Write(&f); !errors.Is(err, ErrUnrepresentable) {
		t.Fatalf("BLF channel 70000: %v", err)
	}
}

func TestCandumpWriterDirection(t *testing.T) {
	var buf bytes.Buffer
	w := NewCandumpWriter(&buf, "can9")
	f, _ := canbus.NewStandard(0x7E0, []byte{0x02, 0x10, 0x03})
	w.Write(&f)
	w.SetDirection(true)
	w.Write(&f)
	f.Echo = true
	w.Write(&f)
	x, _ := canbus.ParseXLFrame("05242#81:01:12345678#AABB")
	w.WriteXL(&x)
	x.Echo = true
	w.WriteXL(&x)
	w.Close()
	want := "(0000000000.000000) can9 7E0#021003\n" +
		"(0000000000.000000) can9 7E0#021003 R\n" +
		"(0000000000.000000) can9 7E0#021003 T\n" +
		"(0000000000.000000) can9 05242#81:01:12345678#AABB R\n" +
		"(0000000000.000000) can9 05242#81:01:12345678#AABB T\n"
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
	// And they read back with their marks.
	r := NewCandumpReader(strings.NewReader(want))
	var g canbus.Frame
	var gx canbus.XLFrame
	var echoes []bool
	for {
		xl, err := r.ReadAny(&g, &gx)
		if err != nil {
			break
		}
		echoes = append(echoes, map[bool]bool{true: gx.Echo, false: g.Echo}[xl])
	}
	if !slices.Equal(echoes, []bool{false, false, true, false, true}) {
		t.Fatal(echoes)
	}
}
