// SPDX-License-Identifier: MIT

package isotp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
}

// recorded is a diagnostic trace with Wireshark's dissection of it.
type recorded struct {
	frames []canbus.Frame
	gold   golden
	msgAt  map[int][]byte // message completed by frame n (1-based)
}

func loadRecorded(t *testing.T, name string) recorded {
	t.Helper()
	m, err := corpus.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.Verify(m); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(corpus.Path(name + ".log"))
	if err != nil {
		t.Fatal(err)
	}
	var rec recorded
	r := capture.NewCandumpReader(bytes.NewReader(raw))
	for {
		var f canbus.Frame
		if err := r.Read(&f); err != nil {
			break
		}
		rec.frames = append(rec.frames, f)
	}
	b, err := os.ReadFile(corpus.Path(name + ".isotp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rec.gold); err != nil {
		t.Fatal(err)
	}
	rec.msgAt = map[int][]byte{}
	for _, msg := range rec.gold.Messages {
		d, err := hex.DecodeString(msg.Data)
		if err != nil || msg.Frame < 1 || msg.Frame > len(rec.frames) || uint32(rec.frames[msg.Frame-1].ID) != msg.ID {
			t.Fatalf("%s: bad golden message %+v", name, msg)
		}
		rec.msgAt[msg.Frame] = d
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
		"cards/Benign-Standstill-University-NA-T2_ReadVIN.can9",
		"cards/Benign-Standstill-University-NA-T1_RecognizeCar.can9",
		"cards/UDS-Standstill-University-NA-CaringCaribou_Auto_1.can9.3700-5300",
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
		if d, ok := rec.msgAt[i+1]; ok {
			want[k] = append(want[k], hex.EncodeToString(d))
		}
	}
	for _, ff := range rec.gold.FirstFrames {
		f := &rec.frames[ff.Frame-1]
		wantErrs[key{f.ID, f.IsExtended()}]++
	}
	for i := range rec.frames {
		if _, ok := rec.msgAt[i+1]; ok && pciOf(&rec.frames[i]) == pciCF {
			wantErrs[key{rec.frames[i].ID, rec.frames[i].IsExtended()}]--
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
			TimeoutCr: 50 * time.Millisecond, QueueSize: len(rec.frames) + 1})
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
				case errors.Is(err, ErrInterrupted) || errors.Is(err, ErrTimeout):
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
	deadline := time.Now().Add(10 * time.Second)
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
		"cards/Benign-Standstill-University-NA-T2_ReadVIN.can9":      {{0x17FC0087, 0x17FE0087}},
		"cards/Benign-Standstill-University-NA-T1_RecognizeCar.can9": t1,
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
			x.lines = append(x.lines, i+1)
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

func dialPair(t *testing.T, cfg Config) (*Conn, *virtual.Port) {
	t.Helper()
	vb := virtual.New(virtual.Config{})
	raw, _ := vb.Open(virtual.WithQueueSize(4096))
	port, _ := vb.Open(virtual.WithQueueSize(4096))
	bus, _ := canbus.NewBus(port)
	c, err := Dial(bus, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); bus.Close(); vb.Close() })
	return c, raw
}

func testerSide(t *testing.T, rec recorded, x diagExchange) {
	pad, _ := x.padOf(x.tester)
	bs, st := x.flowOf(x.tester)
	c, raw := dialPair(t, Config{TxID: x.tester, RxID: x.ecu, ExtendedIDs: x.ext, Padding: true, PadByte: &pad,
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
	c, raw := dialPair(t, Config{TxID: x.ecu, RxID: x.tester, ExtendedIDs: x.ext, Padding: true, PadByte: &pad})
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
			now := time.Now()
			if !lastCF.IsZero() && now.Sub(lastCF) < st-time.Millisecond/2 {
				t.Fatalf("line %d: consecutive frames %v apart, STmin %v", x.lines[i], now.Sub(lastCF), st)
			}
			lastCF = now
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
