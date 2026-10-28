// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
)

// readAll reads a trace with go-canbus.
func readAll(t *testing.T, src []byte) ([]canbus.Frame, capture.Format) {
	t.Helper()
	r, format, err := capture.NewReader(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	var out []canbus.Frame
	for {
		var f canbus.Frame
		err := r.Read(&f)
		if err == io.EOF {
			return out, format
		}
		if err != nil {
			t.Fatalf("after %d frames: %v", len(out), err)
		}
		out = append(out, f)
	}
}

// sameLogged compares frames as a log format keeps them: error frames
// only as error frames when the format has no error class.
func sameLogged(t *testing.T, got, want []canbus.Frame, keepsErrorClass bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d frames, want %d", len(got), len(want))
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.IsError() && !keepsErrorClass {
			if !g.IsError() {
				t.Fatalf("frame %d: error frame lost: %s", i, g.String())
			}
			continue
		}
		if !sameFrame(&g, &w) {
			t.Fatalf("frame %d: %s (%s), want %s (%s)", i, g.String(), g.Flags, w.String(), w.Flags)
		}
		if !w.Timestamp.IsZero() {
			if d := g.Timestamp.Sub(w.Timestamp); d > time.Millisecond || d < -time.Millisecond {
				t.Fatalf("frame %d: timestamp off by %v", i, d)
			}
		}
	}
}

// fixtureFrames reads fixtures/logs/frames.jsonl (the frames the log
// fixtures were written from) with their timestamps.
func fixtureFrames(t *testing.T) []canbus.Frame {
	fs, _ := readAll(t, toCaptureJSONL(t, fixture(t, "logs/frames.jsonl")))
	return fs
}

// toCaptureJSONL converts contract frame documents to go-canbus's JSON
// Lines capture format.
func toCaptureJSONL(t *testing.T, docs []byte) []byte {
	var b bytes.Buffer
	w := capture.NewJSONLWriter(&b)
	for _, line := range bytes.Split(docs, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		f := frames(t, line)[0]
		var d struct {
			Timestamp string `json:"timestamp"`
		}
		_ = jsonUnmarshal(line, &d)
		if d.Timestamp != "" {
			var rec canbus.Frame
			if err := capture.ParseCandumpLine("("+d.Timestamp+") x "+f.String(), &rec); err == nil {
				f.Timestamp = rec.Timestamp
			}
		}
		_ = w.Write(&f)
	}
	_ = w.Close()
	return b.Bytes()
}

// TestLogFixturesRead reads the ASC and BLF files python-can wrote
// (fixtures/logs) with go-canbus: every frame must come back.
func TestLogFixturesRead(t *testing.T) {
	if !caps(t, "pythoncan").has("log-convert") {
		t.Skip("the pinned canbus-interop images predate contract 1.1 (no log fixtures)")
	}
	want := fixtureFrames(t)
	for _, c := range []struct {
		file   string
		format capture.Format
		errCls bool
	}{{"logs/frames.asc", capture.FormatASC, false}, {"logs/frames.blf", capture.FormatBLF, false}} {
		scenario(t, "pythoncan", "read "+c.file, func(t *testing.T) {
			got, format := readAll(t, fixture(t, c.file))
			if format != c.format {
				t.Fatalf("detected %s", format)
			}
			sameLogged(t, got, want, c.errCls)
		})
	}
}

// TestLogConvert: go-canbus writes ASC, BLF and candump logs that
// python-can reads back unchanged, and reads what python-can writes.
func TestLogConvert(t *testing.T) {
	if !caps(t, "pythoncan").has("log-convert") {
		t.Skip("the pinned canbus-interop images predate contract 1.1")
	}
	want := fixtureFrames(t)
	for _, c := range []struct {
		ext     string
		format  capture.Format
		feature string
	}{{".asc", capture.FormatASC, "logAsc"}, {".blf", capture.FormatBLF, "logBlf"}, {".log", capture.FormatCandump, ""}} {
		scenario(t, "pythoncan", "go writes "+c.ext+", python-can reads", func(t *testing.T) {
			if !caps(t, "pythoncan").has("log-convert") || c.feature != "" && !caps(t, "pythoncan").Features[c.feature] {
				skipf(t, "pythoncan lacks %s", c.ext)
			}
			work := workDir(t)
			var b bytes.Buffer
			w, _ := capture.NewWriter(&b, c.format)
			var sent []canbus.Frame
			for i := range want {
				f := want[i]
				if c.format == capture.FormatCandump && f.IsRemote() && f.Len > 0 {
					continue // python-can's candump reader drops the requested DLC
				}
				if c.format == capture.FormatASC && f.IsFD() && f.Len == 0 {
					continue // python-can's ASC reader turns 0-byte FD frames into remote ones
				}
				f.Interface = "1"
				if c.format == capture.FormatCandump {
					f.Interface = "can0"
				}
				_ = w.Write(&f)
				sent = append(sent, f)
			}
			_ = w.Close()
			writeFile(t, work, "go"+c.ext, b.Bytes())
			out, code := run(t, "pythoncan", nil, work, "log-convert", "--input", "/work/go"+c.ext, "--output", "/work/back.jsonl")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			back, _ := os.ReadFile(work + "/back.jsonl")
			got, _ := readAll(t, toCaptureJSONL(t, back))
			sameLogged(t, got, sent, c.format == capture.FormatBLF)
		})
		scenario(t, "pythoncan", "python-can writes "+c.ext+", go reads", func(t *testing.T) {
			if !caps(t, "pythoncan").has("log-convert") || c.feature != "" && !caps(t, "pythoncan").Features[c.feature] {
				skipf(t, "pythoncan lacks %s", c.ext)
			}
			work := workDir(t)
			writeFile(t, work, "in.jsonl", fixture(t, "logs/frames.jsonl"))
			out, code := run(t, "pythoncan", nil, work, "log-convert", "--input", "/work/in.jsonl", "--output", "/work/py"+c.ext)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			b, _ := os.ReadFile(work + "/py" + c.ext)
			got, _ := readAll(t, b)
			exp := want
			if c.format == capture.FormatCandump {
				// python-can's candump writer drops the requested DLC of
				// remote frames and the class of error frames.
				exp = nil
				for _, f := range want {
					if f.IsRemote() {
						f.Len = 0
					}
					exp = append(exp, f)
				}
			}
			sameLogged(t, got, exp, c.format != capture.FormatASC && c.format != capture.FormatCandump)
		})
	}
}

// trcVersions are records of each TRC file version python-can reads, laid
// out as PEAK's tools write them; both readers must agree on them.
var trcVersions = map[string]string{
	"1.0": `;##########################################################################
;    Start time: 27.10.2026 08:15:00.0
;----+- ---+--- ----+--- + -+ -- -- ...
     1)      1841  0001  8  00 11 22 33 44 55 66 77
     2)      1843  1FFFFFFF 0  RTR
     3)      1844  0123  2  AB CD
`,
	"1.1": `;$FILEVERSION=1.1
;$STARTTIME=46322.34375
     1)      1841.0  Rx         0001  8  00 11 22 33 44 55 66 77
     2)      1841.3  Tx     18FEF100  2  01 02
     3)      1843.0  Rx         0100  3  RTR
     4)      1844.7  Rx         0100  0
`,
	"1.3": `;$FILEVERSION=1.3
;$STARTTIME=46322.34375
     1)      1841.0 1  Rx         0001 -  2    00 11
     2)      1841.3 1  Tx     18FEF100 -  8    01 02 03 04 05 06 07 08
`,
	"2.0": `;$FILEVERSION=2.0
;$STARTTIME=46322.34375
;$COLUMNS=N,O,T,I,d,l,D
      1      1059.900 DT     0300 Rx 8  00 00 00 00 04 00 00 00
      2      1060.900 FB 18FEF100 Rx 12 01 02 03 04 05 06 07 08 09 0A 0B 0C
      3      1061.500 FD     0301 Rx 0
      4      1062.000 RR     0300 Tx 2
`,
}

// pyRead has python-can read a trace file and returns its frames.
func pyRead(t *testing.T, name string, src []byte) []canbus.Frame {
	t.Helper()
	work := workDir(t)
	writeFile(t, work, name, src)
	out, code := run(t, "pythoncan", nil, work, "log-convert", "--input", "/work/"+name, "--output", "/work/back.jsonl")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	back, _ := os.ReadFile(work + "/back.jsonl")
	got, _ := readAll(t, toCaptureJSONL(t, back))
	return got
}

// classicalData keeps the frames python-can's TRC writer writes.
func classicalData(fs []canbus.Frame) []canbus.Frame {
	var out []canbus.Frame
	for _, f := range fs {
		if !f.IsError() && !f.IsRemote() && !f.IsFD() {
			out = append(out, f)
		}
	}
	return out
}

// TestTRC: PEAK TRC between go-canbus and python-can.
func TestTRC(t *testing.T) {
	if !caps(t, "pythoncan").Features["logTrc"] {
		t.Skip("the pinned pythoncan image cannot convert TRC")
	}
	want := fixtureFrames(t)
	const writerLoss = "python-can 4.6.1's TRC writer skips remote, CAN FD and error frames"
	scenario(t, "pythoncan", "read logs/frames.trc", func(t *testing.T) {
		got, format := readAll(t, fixture(t, "logs/frames.trc"))
		if format != capture.FormatTRC {
			t.Fatalf("detected %s", format)
		}
		sameLogged(t, got, classicalData(want), false)
		record(t, "pythoncan", "logs/frames.trc: frames python-can did not write", "known-difference", writerLoss)
	})
	scenario(t, "pythoncan", "python-can writes .trc, go reads", func(t *testing.T) {
		work := workDir(t)
		writeFile(t, work, "in.jsonl", fixture(t, "logs/frames.jsonl"))
		out, code := run(t, "pythoncan", nil, work, "log-convert", "--input", "/work/in.jsonl", "--output", "/work/py.trc")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		b, _ := os.ReadFile(work + "/py.trc")
		got, _ := readAll(t, b)
		sameLogged(t, got, classicalData(want), false)
	})
	scenario(t, "pythoncan", "go writes .trc, python-can reads", func(t *testing.T) {
		var b bytes.Buffer
		w := capture.NewTRCWriter(&b)
		var sent []canbus.Frame
		skippedER := 0
		for i := range want {
			f := want[i]
			f.Interface = "1"
			_ = w.Write(&f)
			if f.IsError() {
				skippedER++
				continue
			}
			sent = append(sent, f)
		}
		_ = w.Close()
		got := pyRead(t, "go.trc", b.Bytes())
		// python-can reads FE (CAN FD with ESI) as bit-rate switched.
		feAsBRS := 0
		for i := range got {
			if i < len(sent) && sent[i].Flags&(canbus.FlagFD|canbus.FlagESI|canbus.FlagBRS) == canbus.FlagFD|canbus.FlagESI && got[i].Flags&canbus.FlagBRS != 0 {
				got[i].Flags &^= canbus.FlagBRS
				feAsBRS++
			}
		}
		sameLogged(t, got, sent, false)
		if skippedER > 0 {
			record(t, "pythoncan", "go.trc: ER records", "known-difference", fmt.Sprintf("python-can 4.6.1's TRC reader skips error frames (%d)", skippedER))
		}
		if feAsBRS > 0 {
			record(t, "pythoncan", "go.trc: FE records", "known-difference", fmt.Sprintf("python-can 4.6.1 reads FE (ESI) as bit-rate switched; PEAK: FB = BRS, FE = ESI, BI = both (%d)", feAsBRS))
		}
	})
	for _, v := range []string{"1.0", "1.1", "1.3", "2.0"} {
		scenario(t, "pythoncan", "TRC "+v+": both readers agree", func(t *testing.T) {
			src := []byte(trcVersions[v])
			got, format := readAll(t, src)
			if format != capture.FormatTRC || len(got) == 0 {
				t.Fatalf("detected %s, %d frames", format, len(got))
			}
			sameLogged(t, pyRead(t, "v.trc", src), got, false)
		})
	}
}
