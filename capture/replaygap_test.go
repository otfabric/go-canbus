// SPDX-License-Identifier: MIT

package capture

import (
	"context"
	"io"
	"math"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// TestSaturate: a number of nanoseconds becomes the nearest Duration, on
// every platform: beyond the range the longest or the shortest one.
func TestSaturate(t *testing.T) {
	two63 := math.Ldexp(1, 63)
	for _, c := range []struct {
		ns   float64
		want time.Duration
	}{
		{0, 0}, {1, 1}, {-1, -1}, {1.9, 1}, {1e9, time.Second},
		{math.Nextafter(two63, 0), math.MaxInt64 - 1023}, // the largest float64 below 2^63
		{two63, math.MaxInt64}, {math.Nextafter(two63, math.Inf(1)), math.MaxInt64},
		{math.Ldexp(1, 64), math.MaxInt64}, {math.MaxFloat64, math.MaxInt64}, {math.Inf(1), math.MaxInt64},
		{-two63, math.MinInt64}, {math.Nextafter(-two63, 0), math.MinInt64 + 1024},
		{math.Nextafter(-two63, math.Inf(-1)), math.MinInt64}, {-math.MaxFloat64, math.MinInt64}, {math.Inf(-1), math.MinInt64},
		{math.NaN(), 0},
	} {
		if got := saturate(c.ns); got != c.want {
			t.Errorf("saturate(%v) = %d, want %d", c.ns, got, c.want)
		}
	}
}

// traceReader reads the frames of a slice.
type traceReader struct{ frames []canbus.Frame }

func (r *traceReader) Read(f *canbus.Frame) error {
	if len(r.frames) == 0 {
		return io.EOF
	}
	*f, r.frames = r.frames[0], r.frames[1:]
	return nil
}

type countingSender struct{ sent int }

func (s *countingSender) Send(context.Context, *canbus.Frame) error { s.sent++; return nil }

// TestReplayGapBeyondDuration: a gap between two records that no Duration
// holds, as recorded or after the division by Speed, is the longest wait
// there is: with MaxDuration set the replay ends as limited at once, and a
// record that far before the first one is sent at once. The same for a
// Rate so small that its interval is beyond a Duration.
func TestReplayGapBeyondDuration(t *testing.T) {
	const yearSeconds = 365 * 24 * 3600.0
	at := func(years int) time.Time { return time.Date(2000+years, 1, 1, 0, 0, 0, 0, time.UTC) }
	two := func(a, b time.Time) *traceReader {
		fa, _ := canbus.NewStandard(1, []byte{1})
		fb, _ := canbus.NewStandard(2, []byte{2})
		fa.Timestamp, fb.Timestamp = a, b
		return &traceReader{frames: []canbus.Frame{fa, fb}}
	}
	for _, c := range []struct {
		name    string
		trace   *traceReader
		opts    ReplayOptions
		sent    int
		limited bool
	}{
		{"1 ms apart", two(at(0), at(0).Add(time.Millisecond)), ReplayOptions{Speed: 1, MaxDuration: time.Hour}, 2, false},
		{"a day apart", two(at(0), at(0).Add(24*time.Hour)), ReplayOptions{Speed: 1, MaxDuration: time.Hour}, 1, true},
		{"292 years apart", two(at(0), at(292)), ReplayOptions{Speed: 1, MaxDuration: time.Hour}, 1, true},
		{"300 years apart", two(at(0), at(300)), ReplayOptions{Speed: 1, MaxDuration: time.Hour}, 1, true},
		{"7000 years apart", two(at(0), at(7000)), ReplayOptions{Speed: 1, MaxDuration: time.Hour}, 1, true},
		{"200 years apart at half speed", two(at(0), at(200)), ReplayOptions{Speed: 0.5, MaxDuration: time.Hour}, 1, true},
		{"a year apart at a billionth of the speed", two(at(0), at(1)), ReplayOptions{Speed: 1e-9, MaxDuration: time.Hour}, 1, true},
		{"a second apart, the smallest speed", two(at(0), at(0).Add(time.Second)), ReplayOptions{Speed: math.SmallestNonzeroFloat64, MaxDuration: time.Hour}, 1, true},
		{"300 years before the first", two(at(300), at(0)), ReplayOptions{Speed: 1, MaxDuration: time.Hour}, 2, false},
		{"200 years before, at half speed", two(at(200), at(0)), ReplayOptions{Speed: 0.5, MaxDuration: time.Hour}, 2, false},
		{"the smallest rate", two(at(0), at(0)), ReplayOptions{Rate: math.SmallestNonzeroFloat64, MaxDuration: time.Hour}, 1, true},
		{"a rate of one frame in 300 years", two(at(0), at(0)), ReplayOptions{Rate: 1 / (300 * yearSeconds), MaxDuration: time.Hour}, 1, true},
	} {
		var s countingSender
		st, err := Replay(context.Background(), c.trace, &s, c.opts)
		if err != nil || s.sent != c.sent || st.Sent != c.sent || st.Limited != c.limited || st.Elapsed > 10*time.Second {
			t.Errorf("%s: %d frames sent, %+v, %v; want %d sent, limited %v", c.name, s.sent, st, err, c.sent, c.limited)
		}
	}
}
