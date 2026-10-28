// SPDX-License-Identifier: MIT

// Command slcan prints every frame received by a serial-line CAN adapter
// (CANable, CANtact, USBtin, LAWICEL CANUSB) on any operating system,
// listen-only: the adapter neither transmits nor acknowledges frames.
//
//	go run ./examples/slcan -d /dev/ttyACM0 -bitrate 500000
//	go run ./examples/slcan -d COM3 -bitrate 250000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/slcan"
)

func main() {
	dev := flag.String("d", "/dev/ttyACM0", "serial device of the adapter")
	bitrate := flag.Int("bitrate", 500000, "CAN bit rate")
	flag.Parse()
	conn, err := slcan.Open(*dev, 0, slcan.Config{Bitrate: *bitrate, ListenOnly: true})
	if err != nil {
		log.Fatal(err)
	}
	bus, err := canbus.NewBus(conn, canbus.WithStartOnSubscribe())
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
