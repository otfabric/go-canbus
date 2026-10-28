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

// XLSender sends CAN XL frames: a [canbus.Bus], a SocketCAN socket opened
// with CAN XL, a virtual bus port.
type XLSender interface {
	SendXL(ctx context.Context, x *canbus.XLFrame) error
}

// ReplayOptions control [Replay]. The zero value replays every frame as
// fast as possible.
type ReplayOptions struct {
	// Speed scales the original timing: 1 replays with the recorded gaps,
	// 2 twice as fast, 0.5 at half speed. 0 sends as fast as possible.
	Speed float64
	// Filter selects the frames to send; nil sends all. Error frames are
	// never sent. CAN XL frames are selected as by [canbus.FilterSet.MatchXL].
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
	// OnXLFrame, if set, is called after each CAN XL frame was sent.
	OnXLFrame func(x *canbus.XLFrame)
}

// ReplayStats reports what [Replay] did.
type ReplayStats struct {
	Read     int // frames read from the trace
	Sent     int // frames sent
	Filtered int // frames not selected by the filter
	Skipped  int // error frames (which cannot be sent)
	// SkippedXL counts the CAN XL frames that were not sent because their
	// sender does not carry CAN XL (no [XLSender], or SendXL reports
	// [canbus.ErrUnsupported], as a socket opened without CAN XL does).
	SkippedXL int
	Elapsed   time.Duration // wall time of the replay
	Limited   bool          // MaxFrames or MaxDuration ended the replay
}

// Replay reads frames from r and sends them to s, streaming: memory use
// does not depend on the trace size. A reader of CAN XL frames
// ([XLReader]) has them replayed through senders that carry CAN XL
// ([XLSender]); see [ReplayStats.SkippedXL]. It returns when the trace ends, a
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
	// pace waits until a frame recorded at ts is due.
	pace := func(ts time.Time) error {
		if opts.Speed > 0 && !ts.IsZero() {
			if first.IsZero() {
				first = ts
			}
			offset := time.Duration(float64(ts.Sub(first)) / opts.Speed)
			if err := wait(start.Add(offset)); err != nil {
				return err
			}
		}
		if interval > 0 && !lastSend.IsZero() {
			return wait(lastSend.Add(interval))
		}
		return nil
	}
	xr, _ := r.(XLReader)
	var f canbus.Frame
	var x canbus.XLFrame
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
		var isXL bool
		var err error
		if xr != nil {
			isXL, err = xr.ReadAny(&f, &x)
		} else {
			err = r.Read(&f)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return st, nil
			}
			return st, err
		}
		st.Read++
		if isXL {
			if opts.Filter != nil && !opts.Filter.MatchXL(&x) {
				st.Filtered++
				continue
			}
			var dst any = s
			if d, ok := opts.Interfaces[x.Interface]; ok {
				dst = d
			}
			xs, ok := dst.(XLSender)
			if !ok {
				st.SkippedXL++
				continue
			}
			if err := pace(x.Timestamp); err != nil {
				return limited(st, err)
			}
			out := x
			out.Timestamp, out.Interface, out.Echo = time.Time{}, "", false
			if err := xs.SendXL(ctx, &out); err != nil {
				if errors.Is(err, canbus.ErrUnsupported) {
					st.SkippedXL++
					continue
				}
				return st, err
			}
			lastSend = time.Now()
			st.Sent++
			if opts.OnXLFrame != nil {
				opts.OnXLFrame(&x)
			}
			continue
		}
		if f.Flags&canbus.FlagError != 0 {
			st.Skipped++
			continue
		}
		if opts.Filter != nil && !opts.Filter.Match(&f) {
			st.Filtered++
			continue
		}
		if err := pace(f.Timestamp); err != nil {
			return limited(st, err)
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
// returns the number of frames written. CAN XL frames are recorded with
// [RecordAll].
func RecordFrames(ctx context.Context, sub *canbus.Subscription, w Writer, count int) (int, error) {
	n := 0
	for count <= 0 || n < count {
		f, err := sub.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return n, nil //nolint:nilerr // the end of ctx ends a recording normally
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

// RecordAll is [RecordFrames] for a subscription opened with
// [canbus.WithXL] and a writer of CAN XL frames: it records the frames of
// both queues in the order the bus received them. The bus fills the
// queues in arrival order, so a frame taken from one queue is preceded
// only by frames already queued in the other: of two queued frames the
// earlier (by timestamp) is written first. When count ends the recording,
// a frame read ahead from the other queue is left unwritten.
func RecordAll(ctx context.Context, sub *canbus.Subscription, w XLWriter, count int) (int, error) {
	var (
		f        canbus.Frame
		x        canbus.XLFrame
		hasF     bool
		hasX     bool
		n        int
		finished bool // the subscription ended: drain, then stop
	)
	poll := func() {
		if !hasF {
			select {
			case f = <-sub.Frames():
				hasF = true
			default:
			}
		}
		if !hasX {
			select {
			case x = <-sub.XLFrames():
				hasX = true
			default:
			}
		}
	}
	for count <= 0 || n < count {
		poll()
		if !hasF && !hasX {
			if finished {
				return n, sub.Err()
			}
			select {
			case f = <-sub.Frames():
				hasF = true
			case x = <-sub.XLFrames():
				hasX = true
			case <-sub.Done():
				finished = true
				continue
			case <-ctx.Done():
				return n, nil
			}
			poll() // a frame of the other queue may precede it
		}
		var err error
		if hasF && (!hasX || !x.Timestamp.Before(f.Timestamp)) {
			err, hasF = w.Write(&f), false
		} else {
			err, hasX = w.WriteXL(&x), false
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
