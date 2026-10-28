// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/capture"
	"github.com/otfabric/go-canbus/dbc"
	"github.com/otfabric/go-canbus/internal/golden/full"
	"github.com/otfabric/go-canbus/internal/leak"
	"github.com/otfabric/go-canbus/isotp"
	"github.com/otfabric/go-canbus/virtual"
)

// network is the transport side of a scenario: it opens transports for
// ECUs and testers. The virtual implementation runs everywhere; the
// SocketCAN one in vehicle_linux_test.go.
type network interface {
	open(t *testing.T, fd bool) canbus.Transport
}

type virtualNet struct{ b *virtual.Bus }

func (v virtualNet) open(t *testing.T, fd bool) canbus.Transport {
	opts := []virtual.PortOption{virtual.WithQueueSize(8192)}
	if fd {
		opts = append(opts, virtual.WithFD())
	}
	p, err := v.b.Open(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// signalsOf decodes a frame with the database into name → raw value.
func signalsOf(t *testing.T, db *candb.Database, f *canbus.Frame) (string, map[string]uint64) {
	m, sigs, err := db.DecodeFrame(f)
	if err != nil {
		t.Fatalf("%s: %v", f, err)
	}
	out := map[string]uint64{}
	for _, s := range sigs {
		out[s.Signal.Name] = s.Raw
	}
	return m.Name, out
}

// vehicle runs the scenario on a network:
//
//  1. three ECUs send messages built with generated code (classical,
//     multiplexed, CAN FD) at their own rates;
//  2. a gateway receives them on a Bus, decodes them with the DBC and
//     records them into every trace format at once;
//  3. meanwhile a tester runs ISO-TP request/response with a diagnostic
//     server (1000-byte responses);
//  4. each recording is replayed onto a fresh virtual bus and decoded:
//     the signals must equal what the gateway decoded live.
func vehicle(t *testing.T, net network, isotpServer func(t *testing.T) isotp.Endpoint, isotpClient func(t *testing.T) isotp.Endpoint) {
	db, _, err := dbc.ParseFile("../dbc/testdata/full.dbc")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	gw, err := canbus.NewBus(net.open(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	ids := []canbus.Filter{canbus.ExactStandard(full.EngineID), canbus.ExactStandard(full.MuxedID), canbus.ExactExtended(full.FdMsgID)}
	sub, _ := gw.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: ids}), canbus.WithQueueSize(4096))

	// Recorders for every writable format.
	formats := []capture.Format{capture.FormatCandump, capture.FormatJSONL, capture.FormatPCAP, capture.FormatASC, capture.FormatBLF, capture.FormatTRC}
	bufs := map[capture.Format]*bytes.Buffer{}
	writers := map[capture.Format]capture.Writer{}
	for _, f := range formats {
		bufs[f] = &bytes.Buffer{}
		w, err := capture.NewWriter(bufs[f], f)
		if err != nil {
			t.Fatal(err)
		}
		writers[f] = w
	}

	// ECUs.
	const perECU = 60
	var wg sync.WaitGroup
	ecu := func(fd bool, build func(i int) (canbus.Frame, error), every time.Duration) {
		tr := net.open(t, fd)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perECU; i++ {
				f, err := build(i)
				if err != nil {
					t.Error(err)
					return
				}
				if err := tr.Send(ctx, &f); err != nil {
					t.Error(err)
					return
				}
				time.Sleep(every)
			}
		}()
	}
	ecu(false, func(i int) (canbus.Frame, error) {
		e := full.Engine{Speed: uint16(i * 100), Temp: int8(i - 30), Throttle: uint16(i * 7), Mode: full.EngineMode(i % 8), Flag: i%2 == 0}
		return e.Frame()
	}, time.Millisecond)
	ecu(false, func(i int) (canbus.Frame, error) {
		m := full.Muxed{Sel: full.MuxedSel(i % 2), C: uint8(i)}
		if i%2 == 0 {
			m.A = uint16(i * 1000)
		} else {
			m.B = int8(-i)
		}
		return m.Frame()
	}, 2*time.Millisecond)
	ecu(true, func(i int) (canbus.Frame, error) {
		return (&full.FdMsg{First: uint8(i), Last: uint8(255 - i)}).Frame()
	}, 3*time.Millisecond)

	// ISO-TP diagnostics alongside the traffic.
	server, client := isotpServer(t), isotpClient(t)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			req, err := server.Receive(ctx)
			if err != nil {
				t.Error("server:", err)
				return
			}
			resp := bytes.Repeat(req, 1000/len(req)+1)[:1000]
			if err := server.Send(ctx, resp); err != nil {
				t.Error("server send:", err)
				return
			}
		}
	}()
	for i := 0; i < 5; i++ {
		req := []byte(fmt.Sprintf("read %d", i))
		if err := client.Send(ctx, req); err != nil {
			t.Fatal("client:", err)
		}
		resp, err := client.Receive(ctx)
		if err != nil || len(resp) != 1000 || !bytes.HasPrefix(resp, req) {
			t.Fatalf("response %d: %d bytes, %v", i, len(resp), err)
		}
	}

	// The gateway: decode live and record.
	type decoded struct {
		name string
		sig  map[string]uint64
	}
	var live []decoded
	for len(live) < 3*perECU {
		f, err := sub.Receive(ctx)
		if err != nil {
			t.Fatalf("gateway after %d frames: %v", len(live), err)
		}
		name, sig := signalsOf(t, db, &f)
		live = append(live, decoded{name, sig})
		for _, w := range writers {
			if err := w.Write(&f); err != nil {
				t.Fatal(err)
			}
		}
	}
	wg.Wait()
	for _, w := range writers {
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Replay every recording onto a new virtual bus and decode again.
	for _, format := range formats {
		vb := virtual.New(virtual.Config{})
		tx, _ := vb.Open(virtual.WithFD())
		rx, _ := vb.Open(virtual.WithFD(), virtual.WithQueueSize(4096))
		r, got, err := capture.NewReader(bytes.NewReader(bufs[format].Bytes()))
		if err != nil || got != format {
			t.Fatalf("%s: %v (%s)", format, err, got)
		}
		st, err := capture.Replay(ctx, r, tx, capture.ReplayOptions{Speed: 10})
		if err != nil || st.Sent != len(live) {
			t.Fatalf("%s replay: %+v %v", format, st, err)
		}
		for i := range live {
			var f canbus.Frame
			if err := rx.Receive(ctx, &f); err != nil {
				t.Fatal(err)
			}
			name, sig := signalsOf(t, db, &f)
			if name != live[i].name || !reflect.DeepEqual(sig, live[i].sig) {
				t.Fatalf("%s frame %d: %s %v, live %s %v", format, i, name, sig, live[i].name, live[i].sig)
			}
		}
		vb.Close()
	}
}

func TestVehicleVirtual(t *testing.T) {
	leak.Check(t)
	vb := virtual.New(virtual.Config{Name: "vehicle"})
	defer vb.Close()
	net := virtualNet{vb}
	dial := func(tx, rx canbus.ID) func(t *testing.T) isotp.Endpoint {
		return func(t *testing.T) isotp.Endpoint {
			b, _ := canbus.NewBus(net.open(t, false))
			c, err := isotp.Dial(b, isotp.Config{TxID: tx, RxID: rx, Padding: true, BlockSize: 8})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close(); b.Close() })
			return c
		}
	}
	vehicle(t, net, dial(0x7E8, 0x7E0), dial(0x7E0, 0x7E8))
}
