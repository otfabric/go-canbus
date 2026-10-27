// SPDX-License-Identifier: MIT

// Command dbc-decode parses a DBC database and decodes and encodes a frame
// with it. It needs no hardware.
package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb"
	"github.com/otfabric/go-canbus/dbc"
)

const db = `VERSION ""
BU_: ECU
BO_ 256 Engine: 8 ECU
 SG_ Speed : 0|16@1+ (0.125,0) [0|8191.875] "rpm" Vector__XXX
 SG_ Temp : 16|8@1- (1,-40) [-40|87] "degC" Vector__XXX
 SG_ Gear : 31|4@0+ (1,0) [0|15] "" Vector__XXX
VAL_ 256 Gear 0 "Neutral" 1 "First" 2 "Second" ;
`

func main() {
	database, diags, err := dbc.Parse(strings.NewReader(db))
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range diags {
		log.Println(d)
	}
	frame, _ := canbus.NewStandard(0x100, []byte{0x34, 0x12, 0x3C, 0x20, 0, 0, 0, 0})
	msg, signals, err := database.DecodeFrame(&frame)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(frame, "is", msg.Name)
	for _, s := range signals {
		fmt.Printf("  %-6s raw %-6s = %g %s %s\n", s.Signal.Name, s.Signal.RawString(s.Value), s.Physical, s.Signal.Unit, s.Label)
	}
	out, err := msg.Frame(map[string]candb.Input{
		"Speed": candb.Phys(3000), "Temp": candb.Phys(85), "Gear": candb.Label("Second"),
	}, candb.EncodeOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("encoded:", out)
}
