// SPDX-License-Identifier: MIT

// Command monitor prints every frame on a SocketCAN interface (Linux),
// passively: it never transmits.
//
//	go run ./examples/monitor -i vcan0 -filter 123:7FF
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/socketcan"
)

func main() {
	iface := flag.String("i", "vcan0", "CAN interface")
	filter := flag.String("filter", "", "optional filter <id>:<mask> (candump notation)")
	flag.Parse()
	opts := []socketcan.Option{socketcan.WithFD()}
	if *filter != "" {
		f, err := canbus.ParseFilter(*filter)
		if err != nil {
			log.Fatal(err)
		}
		opts = append(opts, socketcan.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{f}}))
	}
	conn, err := socketcan.Open(*iface, opts...)
	if err != nil {
		log.Fatal(err)
	}
	bus, err := canbus.NewBus(conn)
	if err != nil {
		log.Fatal(err)
	}
	defer bus.Close()
	sub, _ := bus.Subscribe()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for {
		f, err := sub.Receive(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%d frames, %d dropped\n", bus.Stats().RxFrames, bus.Stats().Dropped)
			return
		}
		fmt.Printf("(%d.%06d) %s %s\n", f.Timestamp.Unix(), f.Timestamp.Nanosecond()/1000, f.Interface, f.String())
	}
}
