// SPDX-License-Identifier: MIT

// Command capture-replay records traffic of a virtual bus into a candump
// log, replays it at double speed onto another bus and compares. On Linux
// the same calls work with socketcan.Open.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
	"github.com/otfabric/go-canbus/virtual"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Record 20 frames, 5 ms apart.
	src := virtual.New(virtual.Config{Name: "can0"})
	defer src.Close()
	node, _ := src.Open()
	recPort, _ := src.Open()
	recBus, _ := canbus.NewBus(recPort, canbus.WithStartOnSubscribe())
	defer recBus.Close()
	sub, _ := recBus.Subscribe()
	go func() {
		for i := 0; i < 20; i++ {
			f, _ := canbus.NewStandard(0x200+canbus.ID(i%4), []byte{byte(i)})
			_ = node.Send(ctx, &f)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	var log1 bytes.Buffer
	w := capture.NewCandumpWriter(&log1, "")
	n, err := capture.RecordFrames(ctx, sub, w, 20)
	if err != nil {
		log.Fatal(err)
	}
	_ = w.Close()
	fmt.Printf("recorded %d frames, first line: %s", n, bytes.SplitAfter(log1.Bytes(), []byte("\n"))[0])

	// Replay at 2x onto another bus.
	dst := virtual.New(virtual.Config{Name: "can1"})
	defer dst.Close()
	out, _ := dst.Open()
	in, _ := dst.Open(virtual.WithQueueSize(100))
	r := capture.NewCandumpReader(bytes.NewReader(log1.Bytes()))
	st, err := capture.Replay(ctx, r, out, capture.ReplayOptions{Speed: 2})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("replayed %d frames in %v (recorded over about 95 ms)\n", st.Sent, st.Elapsed.Round(time.Millisecond))
	for i := 0; i < st.Sent; i++ {
		var f canbus.Frame
		if err := in.Receive(ctx, &f); err != nil || f.Data[0] != byte(i) {
			log.Fatalf("frame %d differs: %v", i, err)
		}
	}
	fmt.Println("replayed frames match the recording")
}
