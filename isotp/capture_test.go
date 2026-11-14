// SPDX-License-Identifier: MIT

package isotp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
	"github.com/otfabric/go-canbus/internal/corpus"
	"github.com/otfabric/go-canbus/virtual"
)

// The tests in this file run the ISO-TP connection against diagnostic
// traffic recorded on the CarDS vehicle (testdata/captures/cards): two
// workshop testers talking UDS to its ECUs, and CaringCaribou's UDS
// enumeration. The expected messages are Wireshark's ISO 15765 dissection
// of the same lines (the *.isotp.json files, made with tshark 4.6.9), not
// this package's.

// golden is a *.isotp.json file.
type golden struct {
	Messages []struct {
		Frame    int    `json:"frame"`
		ID       uint32 `json:"id"`
		Extended bool   `json:"extended"`
		Data     string `json:"data"`
	} `json:"messages"`
	FlowControls []struct {
		Frame  int    `json:"frame"`
		ID     uint32 `json:"id"`
		Status int    `json:"status"`
		BS     int    `json:"bs"`
		STmin  int    `json:"stmin"`
	} `json:"flow_controls"`
	FirstFrames []struct {
		Frame int    `json:"frame"`
		ID    uint32 `json:"id"`
	} `json:"first_frames"`
	Interface string `json:"interface"`
}

// recorded is the diagnostic bus of a recording with Wireshark's
// dissection of it.
type recorded struct {
	frames []canbus.Frame // the frames on the golden file's interface
	lines  []int          // their lines in the whole recording (from 1)
	gold   golden
	msgAt  map[int][]byte // message completed by line n
}

func loadRecorded(t *testing.T, name string) recorded {
	t.Helper()
	if _, err := corpus.Checked(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(corpus.Path(name + ".log"))
	if err != nil {
		t.Fatal(err)
	}
	var rec recorded
	b, err := os.ReadFile(corpus.Path(name + ".isotp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rec.gold); err != nil {
		t.Fatal(err)
	}
	r := capture.NewCandumpReader(bytes.NewReader(raw))
	index := map[int]int{} // line -> index in rec.frames
	for line := 1; ; line++ {
		var f canbus.Frame
		if err := r.Read(&f); err != nil {
			break
		}
		if rec.gold.Interface == "" || f.Interface == rec.gold.Interface {
			index[line] = len(rec.frames)
			rec.frames = append(rec.frames, f)
			rec.lines = append(rec.lines, line)
		}
	}
	rec.msgAt = map[int][]byte{}
	for _, msg := range rec.gold.Messages {
		d, err := hex.DecodeString(msg.Data)
		i, ok := index[msg.Frame]
		if err != nil || !ok || uint32(rec.frames[i].ID) != msg.ID {
			t.Fatalf("%s: bad golden message %+v", name, msg)
		}
		rec.msgAt[msg.Frame] = d
	}
	for k, ff := range rec.gold.FirstFrames {
		i, ok := index[ff.Frame]
		if !ok {
			t.Fatalf("%s: bad golden first frame %+v", name, ff)
		}
		rec.gold.FirstFrames[k].Frame = i + 1 // as an index of rec.frames from 1
	}
	return rec
}

func pciOf(f *canbus.Frame) byte { return f.Data[0] >> 4 }

// TestCaptureReassemblyMatchesWireshark replays each recorded trace to a
// listen-only connection per CAN identifier: every single frame and every
// complete multi-frame message is what Wireshark reassembles, in order,
// and every first frame the recording does not complete (CaringCaribou
// never answers them with a flow control the ECU accepts) ends in a
// reception error instead of a message.
func TestCaptureReassemblyMatchesWireshark(t *testing.T) {
	for _, name := range []string{
		"cards/tester-t2_2020-ev_read-vin",
		"cards/tester-t1_2020-ev_recognize-car",
		"cards/caringcaribou_2020-ev_uds-scan",
	} {
		t.Run(name, func(t *testing.T) { reassemble(t, loadRecorded(t, name)) })
	}
}

func reassemble(t *testing.T, rec recorded) {
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	tx, _ := vb.Open(virtual.WithBlocking())
	type key struct {
		id  canbus.ID
		ext bool
	}
	want := map[key][]string{}
	wantErrs := map[key]int{}
	var order []key
	for i := range rec.frames {
		f := &rec.frames[i]
		k := key{f.ID, f.IsExtended()}
		if _, ok := want[k]; !ok {
			want[k] = nil
			order = append(order, k)
		}
		if d, ok := rec.msgAt[rec.lines[i]]; ok {
			want[k] = append(want[k], hex.EncodeToString(d))
		}
	}
	// From Wireshark's dissection: a first frame that a later single or
	// first frame of the same identifier overtakes before its message is
	// complete ends in ErrInterrupted; one still open at the end of the
	// trace ends in nothing (N_Cr is an hour here: no wall-clock timeout
	// may decide what this test sees).
	isFF := map[int]bool{}
	for _, ff := range rec.gold.FirstFrames {
		isFF[ff.Frame-1] = true
	}
	open := map[key]bool{}
	pending := 0
	for i := range rec.frames {
		f := &rec.frames[i]
		k := key{f.ID, f.IsExtended()}
		_, msg := rec.msgAt[rec.lines[i]]
		switch {
		case isFF[i]:
			if open[k] {
				wantErrs[k]++
			}
			open[k] = true
		case msg && pciOf(f) == pciCF:
			open[k] = false
		case msg:
			if open[k] {
				wantErrs[k]++
			}
			open[k] = false
		}
	}
	for _, o := range open {
		if o {
			pending++
		}
	}
	type result struct {
		msgs []string
		errs int
	}
	results := map[key]*result{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(t.Context())
	var conns []*Conn
	for _, k := range order {
		port, _ := vb.Open(virtual.WithQueueSize(len(rec.frames) + 1))
		bus, _ := canbus.NewBus(port)
		defer bus.Close()
		c, err := Dial(bus, Config{TxID: k.id, RxID: k.id, ExtendedIDs: k.ext, ListenOnly: true,
			TimeoutCr: time.Hour, QueueSize: len(rec.frames) + 1})
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		res := &result{}
		results[k] = res
		wg.Go(func() {
			for {
				msg, err := c.Receive(ctx)
				mu.Lock()
				switch {
				case ctx.Err() != nil:
					mu.Unlock()
					return
				case errors.Is(err, ErrInterrupted):
					res.errs++
				case err != nil:
					mu.Unlock()
					t.Errorf("%v: %v", k, err)
					return
				default:
					res.msgs = append(res.msgs, hex.EncodeToString(msg))
				}
				mu.Unlock()
			}
		})
	}
	for i := range rec.frames {
		f := rec.frames[i]
		f.Timestamp, f.Interface, f.Echo = time.Time{}, "", false
		if err := tx.Send(t.Context(), &f); err != nil {
			t.Fatal(err)
		}
	}
	// Wait for every connection to report what it should.
	deadline := time.Now().Add(60 * time.Second)
	done := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for k, w := range want {
			r := results[k]
			if len(r.msgs) < len(w) || r.errs < wantErrs[k] {
				return false
			}
		}
		return true
	}
	for !done() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // a late extra message or error would show
	cancel()
	for _, c := range conns {
		c.Close()
	}
	wg.Wait()
	total := 0
	for _, k := range order {
		r := results[k]
		if !slices.Equal(r.msgs, want[k]) {
			t.Errorf("%#x: messages\n%v\nWireshark\n%v", uint32(k.id), r.msgs, want[k])
		}
		if r.errs != wantErrs[k] {
			t.Errorf("%#x: %d reception errors, %d first frames without their message", uint32(k.id), r.errs, wantErrs[k])
		}
		total += len(r.msgs)
	}
	if total != len(rec.gold.Messages) {
		t.Errorf("%d messages, Wireshark %d", total, len(rec.gold.Messages))
	}
	t.Logf("%d messages, %d first frames interrupted, %d open at the end of the trace", total, sumValues(wantErrs), pending)
}

func sumValues[K comparable](m map[K]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// diagPair is a tester and an ECU of a recorded trace.
type diagPair struct{ tester, ecu canbus.ID }

func recordedPairs() map[string][]diagPair {
	t1 := []diagPair{{0x721, 0x78B}, {0x784, 0x7EE}, {0x757, 0x7C1}, {0x71F, 0x789}, {0x726, 0x790},
		{0x75F, 0x7C9}, {0x75C, 0x7C6}, {0x75B, 0x7C5},
		{0x17FC0087, 0x17FE0087}, {0x17FC00CA, 0x17FE00CA}, {0x17FC00CB, 0x17FE00CB}, {0x17FC008C, 0x17FE008C}}
	for _, n := range []canbus.ID{0x12, 0x13, 0x14, 0x15, 0x17, 0x18, 0x19, 0x1D} {
		t1 = append(t1, diagPair{0x18040000 | n, 0x18060000 | n})
	}
	return map[string][]diagPair{
		"cards/tester-t2_2020-ev_read-vin":      {{0x17FC0087, 0x17FE0087}},
		"cards/tester-t1_2020-ev_recognize-car": t1,
	}
}

// exchange is the frames of one tester–ECU pair, in recorded order, with
// the line numbers of the trace.
type diagExchange struct {
	diagPair
	ext    bool
	frames []canbus.Frame
	lines  []int
}

func (rec recorded) exchange(p diagPair) diagExchange {
	x := diagExchange{diagPair: p, ext: p.tester > canbus.MaxStandardID}
	for i := range rec.frames {
		if id := rec.frames[i].ID; id == p.tester || id == p.ecu {
			x.frames = append(x.frames, rec.frames[i])
			x.lines = append(x.lines, rec.lines[i])
		}
	}
	return x
}

// padOf returns the padding byte of the first padded single frame of id.
func (x diagExchange) padOf(id canbus.ID) (byte, bool) {
	for i := range x.frames {
		f := &x.frames[i]
		if l := int(f.Data[0] & 0x0F); f.ID == id && pciOf(f) == pciSF && l < 7 && f.Len == 8 {
			return f.Data[1+l], true
		}
	}
	return 0, false
}

// flowOf returns the block size and STmin of id's first flow control.
func (x diagExchange) flowOf(id canbus.ID) (uint8, time.Duration) {
	for i := range x.frames {
		if f := &x.frames[i]; f.ID == id && pciOf(f) == pciFC {
			return f.Data[1], DecodeSTmin(f.Data[2])
		}
	}
	return 0, 0
}

// messageFrom returns the message whose first frame is x.frames[i].
func (x diagExchange) messageFrom(rec recorded, i int) []byte {
	for j := i; j < len(x.frames); j++ {
		if x.frames[j].ID != x.frames[i].ID {
			continue
		}
		if d, ok := rec.msgAt[x.lines[j]]; ok {
			return d
		}
	}
	return nil
}

func wire(f *canbus.Frame) string {
	return fmt.Sprintf("%X#%X", uint32(f.ID), f.Payload())
}

// TestCaptureTesterSide plays the recorded ECUs against a connection set
// up as each recorded tester (its padding byte, block size and STmin):
// every request and flow control it sends is the tester's frame byte for
// byte, and every response it receives is Wireshark's message.
func TestCaptureTesterSide(t *testing.T) {
	for name, pairs := range recordedPairs() {
		rec := loadRecorded(t, name)
		for _, p := range pairs {
			x := rec.exchange(p)
			t.Run(fmt.Sprintf("%s/%X", name, uint32(p.tester)), func(t *testing.T) { testerSide(t, rec, x) })
		}
	}
}

// timedPort is a virtual bus port that notes when the connection on it
// hands each consecutive frame to the bus: the moment STmin applies to,
// independent of when the test's goroutine reads the frames.
type timedPort struct {
	*virtual.Port
	mu  sync.Mutex
	cfs []time.Time
}

func (p *timedPort) Send(ctx context.Context, f *canbus.Frame) error {
	if f.Len > 0 && f.Data[0]>>4 == pciCF {
		p.mu.Lock()
		p.cfs = append(p.cfs, time.Now())
		p.mu.Unlock()
	}
	return p.Port.Send(ctx, f)
}

// sentCF returns when the connection sent its n-th consecutive frame.
func (p *timedPort) sentCF(n int) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfs[n]
}

func dialPair(t *testing.T, cfg Config) (*Conn, *virtual.Port, *timedPort) {
	t.Helper()
	vb := virtual.New(virtual.Config{})
	raw, _ := vb.Open(virtual.WithQueueSize(4096))
	port, _ := vb.Open(virtual.WithQueueSize(4096))
	tp := &timedPort{Port: port}
	bus, _ := canbus.NewBus(tp)
	c, err := Dial(bus, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); bus.Close(); vb.Close() })
	return c, raw, tp
}

func testerSide(t *testing.T, rec recorded, x diagExchange) {
	pad, _ := x.padOf(x.tester)
	bs, st := x.flowOf(x.tester)
	c, raw, _ := dialPair(t, Config{TxID: x.tester, RxID: x.ecu, ExtendedIDs: x.ext, Padding: true, PadByte: &pad,
		BlockSize: bs, STmin: st, TimeoutCr: 5 * time.Second})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	received := 0
	for i := range x.frames {
		f := x.frames[i]
		if f.ID == x.tester {
			switch pciOf(&f) {
			case pciSF:
				if err := c.Send(ctx, rec.msgAt[x.lines[i]]); err != nil {
					t.Fatalf("line %d: send: %v", x.lines[i], err)
				}
			case pciFC: // sent by c on the ECU's first frame
			default:
				t.Fatalf("line %d: the recorded tester sends a multi-frame request", x.lines[i])
			}
			var got canbus.Frame
			if err := raw.Receive(ctx, &got); err != nil {
				t.Fatalf("line %d: %v", x.lines[i], err)
			}
			if wire(&got) != wire(&f) {
				t.Fatalf("line %d: sent %s, the recorded tester %s", x.lines[i], wire(&got), wire(&f))
			}
			continue
		}
		f.Timestamp, f.Interface, f.Echo = time.Time{}, "", false
		if err := raw.Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
		if want, ok := rec.msgAt[x.lines[i]]; ok {
			got, err := c.Receive(ctx)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("line %d: received %X (%v), Wireshark %X", x.lines[i], got, err, want)
			}
			received++
		}
	}
	if received == 0 {
		t.Fatal("no response in the exchange")
	}
}

// TestCaptureECUSide plays the recorded testers against a connection set
// up as each recorded ECU (its padding byte): it receives the requests
// Wireshark decodes, and its single frames, first frames and consecutive
// frames are the ECU's frames byte for byte, the consecutive frames no
// closer together than the tester's STmin asks. Recorded ECUs leave more
// time between consecutive frames than STmin; that time is theirs.
func TestCaptureECUSide(t *testing.T) {
	for name, pairs := range recordedPairs() {
		rec := loadRecorded(t, name)
		for _, p := range pairs {
			x := rec.exchange(p)
			t.Run(fmt.Sprintf("%s/%X", name, uint32(p.ecu)), func(t *testing.T) { ecuSide(t, rec, x) })
		}
	}
}

func ecuSide(t *testing.T, rec recorded, x diagExchange) {
	pad, ok := x.padOf(x.ecu)
	if !ok {
		t.Fatal("no padded single frame of the ECU")
	}
	_, st := x.flowOf(x.tester)
	c, raw, sent := dialPair(t, Config{TxID: x.ecu, RxID: x.tester, ExtendedIDs: x.ext, Padding: true, PadByte: &pad})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	sendErr := make(chan error, 1)
	var lastCF time.Time
	cfs := 0
	for i := range x.frames {
		f := x.frames[i]
		if f.ID == x.tester {
			f.Timestamp, f.Interface, f.Echo = time.Time{}, "", false
			if err := raw.Send(ctx, &f); err != nil {
				t.Fatal(err)
			}
			if want, ok := rec.msgAt[x.lines[i]]; ok {
				got, err := c.Receive(ctx)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("line %d: received %X (%v), Wireshark %X", x.lines[i], got, err, want)
				}
			}
			if pciOf(&f) == pciFC {
				lastCF = time.Time{}
			}
			continue
		}
		switch pciOf(&f) {
		case pciSF:
			if err := c.Send(ctx, rec.msgAt[x.lines[i]]); err != nil {
				t.Fatalf("line %d: %v", x.lines[i], err)
			}
		case pciFF:
			msg := x.messageFrom(rec, i)
			if msg == nil {
				t.Fatalf("line %d: first frame without its message", x.lines[i])
			}
			go func() { sendErr <- c.Send(ctx, msg) }()
		}
		var got canbus.Frame
		if err := raw.Receive(ctx, &got); err != nil {
			t.Fatalf("line %d: %v", x.lines[i], err)
		}
		if wire(&got) != wire(&f) {
			t.Fatalf("line %d: sent %s, the recorded ECU %s", x.lines[i], wire(&got), wire(&f))
		}
		if pciOf(&f) == pciCF {
			// STmin is measured where the connection sends, not where this
			// goroutine happens to read the frames (it may read several
			// queued ones back to back).
			at := sent.sentCF(cfs)
			if !lastCF.IsZero() && at.Sub(lastCF) < st {
				t.Fatalf("line %d: consecutive frames sent %v apart, STmin %v", x.lines[i], at.Sub(lastCF), st)
			}
			lastCF = at
			cfs++
			if _, ok := rec.msgAt[x.lines[i]]; ok {
				if err := <-sendErr; err != nil {
					t.Fatalf("line %d: send: %v", x.lines[i], err)
				}
			}
		}
	}
	t.Logf("%d consecutive frames", cfs)
}

// blockingLog blocks the connection's goroutine in its next log call
// until released.
type blockingLog struct {
	entered chan struct{}
	release chan struct{}
}

func (h *blockingLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *blockingLog) Handle(context.Context, slog.Record) error {
	h.entered <- struct{}{}
	<-h.release
	return nil
}
func (h *blockingLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *blockingLog) WithGroup(string) slog.Handler      { return h }

// TestQueuedConsecutiveFrameBeatsNCr: a consecutive frame that arrived
// before N_Cr ran out continues the transfer even when the connection's
// goroutine handles it only after the timer fired (seen on a loaded CI
// runner replaying the recorded 515-byte response: one spurious ErrTimeout
// in about one run of ten). The goroutine is held in a logger call while
// the frame queues and the timer expires; before the fix select chose the
// timer half the time.
func TestQueuedConsecutiveFrameBeatsNCr(t *testing.T) {
	for range 20 {
		h := &blockingLog{entered: make(chan struct{}), release: make(chan struct{})}
		vb := virtual.New(virtual.Config{})
		raw, _ := vb.Open(virtual.WithQueueSize(256))
		port, _ := vb.Open(virtual.WithQueueSize(256))
		bus, _ := canbus.NewBus(port)
		c, err := Dial(bus, Config{TxID: 0x721, RxID: 0x78B, ListenOnly: true, TimeoutCr: 20 * time.Millisecond, Logger: slog.New(h)})
		if err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		inject(t, raw, "78B#100962F1A2303031") // first frame, 9 bytes
		inject(t, raw, "78B#3000000000000000") // a stray flow control: its drop is logged
		<-h.entered                            // the goroutine is held
		inject(t, raw, "78B#21303032AAAAAAAA")
		time.Sleep(60 * time.Millisecond) // N_Cr runs out while the frame waits
		close(h.release)
		got, err := c.Receive(ctx)
		if err != nil || hex.EncodeToString(got) != "62f1a2303031303032" {
			t.Fatalf("got %x, %v", got, err)
		}
		c.Close()
		bus.Close()
		vb.Close()
	}
}
