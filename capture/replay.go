// SPDX-License-Identifier: MIT

package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/otfabric/go-canbus"
)

// Sender sends frames: a [canbus.Bus] or any [canbus.Transport].
type Sender interface {
	Send(ctx context.Context, f *canbus.Frame) error
}

// ReplayOptions control [Replay]. The zero value replays every frame as
// fast as possible.
type ReplayOptions struct {
	// Speed scales the original timing: 1 replays with the recorded gaps,
	// 2 twice as fast, 0.5 at half speed. 0 sends as fast as possible.
	Speed float64
	// Filter selects the frames to send; nil sends all. Error frames are
	// never sent.
	Filter *canbus.FilterSet
	// MaxFrames stops after this many frames were sent (0: no limit).
	MaxFrames int
	// MaxDuration stops when this much time has passed (0: no limit).
	MaxDuration time.Duration
	// Rate limits the transmission to this many frames per second (0: no
	// limit). It applies on top of Speed.
	Rate float64
	// Interfaces maps recorded interface names to the senders frames of
	// that interface go to. Frames of interfaces not in the map go to the
	// sender passed to Replay; nil sends everything there.
	Interfaces map[string]Sender
	// OnFrame, if set, is called after each frame was sent.
	OnFrame func(f *canbus.Frame)
}

// ReplayStats reports what [Replay] did.
type ReplayStats struct {
	Read     int           // frames read from the trace
	Sent     int           // frames sent
	Filtered int           // frames not selected by the filter
	Skipped  int           // error frames (which cannot be sent)
	Elapsed  time.Duration // wall time of the replay
	Limited  bool          // MaxFrames or MaxDuration ended the replay
}

// Replay reads frames from r and sends them to s, streaming: memory use
// does not depend on the trace size. It returns when the trace ends, a
// limit is reached, ctx ends or a send fails.
func Replay(ctx context.Context, r Reader, s Sender, opts ReplayOptions) (st ReplayStats, err error) {
	if opts.Speed < 0 || opts.Rate < 0 || opts.MaxFrames < 0 || opts.MaxDuration < 0 {
		return st, fmt.Errorf("%w: negative replay option", canbus.ErrInvalidOption)
	}
	start := time.Now()
	defer func() { st.Elapsed = time.Since(start) }() // named result: reported on every return
	var first time.Time
	var interval time.Duration
	if opts.Rate > 0 {
		interval = time.Duration(float64(time.Second) / opts.Rate)
	}
	var lastSend time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	wait := func(until time.Time) error {
		d := time.Until(until)
		if opts.MaxDuration > 0 {
			if rest := opts.MaxDuration - time.Since(start); d > rest {
				return errLimit
			}
		}
		if d <= 0 {
			return nil
		}
		timer.Reset(d)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	var f canbus.Frame
	for {
		if opts.MaxFrames > 0 && st.Sent >= opts.MaxFrames {
			st.Limited = true
			return st, nil
		}
		if opts.MaxDuration > 0 && time.Since(start) >= opts.MaxDuration {
			st.Limited = true
			return st, nil
		}
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if err := r.Read(&f); err != nil {
			if errors.Is(err, io.EOF) {
				return st, nil
			}
			return st, err
		}
		st.Read++
		if f.Flags&canbus.FlagError != 0 {
			st.Skipped++
			continue
		}
		if opts.Filter != nil && !opts.Filter.Match(&f) {
			st.Filtered++
			continue
		}
		if opts.Speed > 0 && !f.Timestamp.IsZero() {
			if first.IsZero() {
				first = f.Timestamp
			}
			offset := time.Duration(float64(f.Timestamp.Sub(first)) / opts.Speed)
			if err := wait(start.Add(offset)); err != nil {
				return limited(st, err)
			}
		}
		if interval > 0 && !lastSend.IsZero() {
			if err := wait(lastSend.Add(interval)); err != nil {
				return limited(st, err)
			}
		}
		dst := s
		if d, ok := opts.Interfaces[f.Interface]; ok {
			dst = d
		}
		out := f
		out.Timestamp, out.Interface, out.Echo = time.Time{}, "", false
		if err := dst.Send(ctx, &out); err != nil {
			return st, err
		}
		lastSend = time.Now()
		st.Sent++
		if opts.OnFrame != nil {
			opts.OnFrame(&f)
		}
	}
}

var errLimit = errors.New("replay duration limit")

func limited(st ReplayStats, err error) (ReplayStats, error) {
	if errors.Is(err, errLimit) {
		st.Limited = true
		return st, nil
	}
	return st, err
}

// RecordFrames receives frames from sub and writes them to w until ctx ends,
// count frames were written (0: no limit) or the subscription ends. It
// returns the number of frames written.
func RecordFrames(ctx context.Context, sub *canbus.Subscription, w Writer, count int) (int, error) {
	n := 0
	for count <= 0 || n < count {
		f, err := sub.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return n, nil
			}
			return n, err
		}
		if err := w.Write(&f); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
