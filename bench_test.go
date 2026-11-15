// SPDX-License-Identifier: MIT

package canbus_test

import (
	"context"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
)

var frameSink canbus.Frame

// BenchmarkFrameCopy copies a frame as a transport hands it to its caller
// and a Bus to each subscription: a Frame is a plain value with its payload
// in a fixed array, so the copy does not allocate.
func BenchmarkFrameCopy(b *testing.B) {
	f, err := canbus.NewFD(0x123, false, true, make([]byte, 64))
	if err != nil {
		b.Fatal(err)
	}
	queue := make(chan canbus.Frame, 1)
	b.ReportAllocs()
	for b.Loop() {
		queue <- f
		frameSink = <-queue
	}
}

// BenchmarkFramePayload reads a frame's payload and builds a frame from
// it.
func BenchmarkFramePayload(b *testing.B) {
	f, err := canbus.NewStandard(0x123, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		g, err := canbus.NewStandard(f.ID, f.Payload())
		if err != nil {
			b.Fatal(err)
		}
		frameSink = g
	}
}

// BenchmarkFrameVirtualPort sends a frame through a virtual bus and
// receives it on another port, with a context that cannot be cancelled.
func BenchmarkFrameVirtualPort(b *testing.B) {
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	tx, _ := vb.Open()
	rx, _ := vb.Open()
	f, _ := canbus.NewStandard(0x123, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := tx.Send(ctx, &f); err != nil {
			b.Fatal(err)
		}
		if err := rx.Receive(ctx, &frameSink); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFrameBus sends a frame through a Bus and receives it from a
// subscription of another Bus on the same virtual bus, with a context that
// can be cancelled: the path of an application that shares a transport.
func BenchmarkFrameBus(b *testing.B) {
	vb := virtual.New(virtual.Config{})
	defer vb.Close()
	pa, _ := vb.Open()
	pb, _ := vb.Open()
	tx, err := canbus.NewBus(pa)
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Close()
	rx, err := canbus.NewBus(pb)
	if err != nil {
		b.Fatal(err)
	}
	defer rx.Close()
	sub, err := rx.Subscribe()
	if err != nil {
		b.Fatal(err)
	}
	f, _ := canbus.NewStandard(0x123, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.ReportAllocs()
	for b.Loop() {
		if err := tx.Send(ctx, &f); err != nil {
			b.Fatal(err)
		}
		g, err := sub.Receive(ctx)
		if err != nil {
			b.Fatal(err)
		}
		frameSink = g
	}
}
