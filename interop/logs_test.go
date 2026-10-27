// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
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
