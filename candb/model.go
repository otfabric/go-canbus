// SPDX-License-Identifier: MIT

// Package candb is the format-independent model of a CAN message database
// and its signal codec.
//
// DBC files (package dbc) and KCD files (package kcd) are parsed into a
// [Database]; the code generator (package codegen) and the dynamic codec
// of this package work on it, so every input format shares one runtime.
//
// Raw signal values are exact: they are carried as 64-bit integers (the
// field's bit pattern, and its sign-extended value for signed signals)
// and never pass through floating point. Physical values are computed
// from them (raw × factor + offset) as float64.
//
// A database describes messages and signals. It is not a protocol, and a
// message identifier names a message, not a device.
package candb

import (
	"fmt"
	"sort"

	"github.com/otfabric/go-canbus"
)

// ByteOrder is the layout of a signal.
type ByteOrder uint8

// Byte orders.
const (
	LittleEndian ByteOrder = iota // Intel; DBC @1
	BigEndian                     // Motorola; DBC @0
)

// String returns "little_endian" or "big_endian".
func (b ByteOrder) String() string {
	if b == BigEndian {
		return "big_endian"
	}
	return "little_endian"
}

// MarshalText implements encoding.TextMarshaler.
func (b ByteOrder) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (b *ByteOrder) UnmarshalText(t []byte) error {
	switch string(t) {
	case "little_endian":
		*b = LittleEndian
	case "big_endian":
		*b = BigEndian
	default:
		return fmt.Errorf("candb: unknown byte order %q", t)
	}
	return nil
}

// ValueType is the representation of a signal's raw value.
type ValueType uint8

// Value types.
const (
	Integer ValueType = iota // two's complement or unsigned integer (Signal.Signed)
	Float32                  // IEEE 754 single precision (32 bits)
	Float64                  // IEEE 754 double precision (64 bits)
)

// String returns "integer", "float32" or "float64".
func (v ValueType) String() string {
	return [...]string{"integer", "float32", "float64"}[min(int(v), 2)]
}

// MarshalText implements encoding.TextMarshaler.
func (v ValueType) MarshalText() ([]byte, error) { return []byte(v.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (v *ValueType) UnmarshalText(t []byte) error {
	switch string(t) {
	case "integer":
		*v = Integer
	case "float32":
		*v = Float32
	case "float64":
		*v = Float64
	default:
		return fmt.Errorf("candb: unknown value type %q", t)
	}
	return nil
}

// Database is a set of messages with their nodes, value tables and
// attributes.
type Database struct {
	Version     string       `json:"version,omitempty"`
	Comment     string       `json:"comment,omitempty"`
	Nodes       []*Node      `json:"nodes,omitempty"`
	Messages    []*Message   `json:"messages"`
	ValueTables []ValueTable `json:"valueTables,omitempty"`
	// AttributeDefinitions declare the attributes used on the database
	// and its objects; Attributes are the database's own values.
	AttributeDefinitions []AttributeDefinition `json:"attributeDefinitions,omitempty"`
	Attributes           []Attribute           `json:"attributes,omitempty"`
}

// Node is an ECU (a sender or receiver of messages).
type Node struct {
	Name       string      `json:"name"`
	Comment    string      `json:"comment,omitempty"`
	Attributes []Attribute `json:"attributes,omitempty"`
}

// Message is the layout of one CAN message.
type Message struct {
	ID       canbus.ID `json:"id"`
	Extended bool      `json:"extended,omitempty"`
	Name     string    `json:"name"`
	// Length is the payload length in bytes: 0..8, or a CAN FD length.
	Length int  `json:"length"`
	FD     bool `json:"fd,omitempty"`  // sent as a CAN FD frame
	BRS    bool `json:"brs,omitempty"` // CAN FD with bit rate switching
	// Senders are the names of the nodes that send the message.
	Senders []string  `json:"senders,omitempty"`
	Signals []*Signal `json:"signals"`
	Comment string    `json:"comment,omitempty"`
	// CycleTime is the transmission period in milliseconds, 0 when
	// unknown or not periodic (DBC attribute GenMsgCycleTime).
	CycleTime    int           `json:"cycleTime,omitempty"`
	SignalGroups []SignalGroup `json:"signalGroups,omitempty"`
	Attributes   []Attribute   `json:"attributes,omitempty"`
}

// Signal is a field of a message.
type Signal struct {
	Name string `json:"name"`
	// StartBit is the DBC start bit: the least significant bit for
	// little-endian signals, the most significant bit (in Intel bit
	// numbering) for big-endian signals.
	StartBit  int       `json:"startBit"`
	Length    int       `json:"length"` // bits, 1..64
	ByteOrder ByteOrder `json:"byteOrder"`
	Signed    bool      `json:"signed,omitempty"`
	ValueType ValueType `json:"valueType"`
	Factor    float64   `json:"factor"`
	Offset    float64   `json:"offset"`
	// Minimum and Maximum are the physical range; both 0 means none.
	Minimum   float64            `json:"minimum"`
	Maximum   float64            `json:"maximum"`
	Unit      string             `json:"unit,omitempty"`
	Receivers []string           `json:"receivers,omitempty"`
	Values    []ValueDescription `json:"values,omitempty"`
	Comment   string             `json:"comment,omitempty"`
	// Initial is the raw initial value (DBC attribute GenSigStartValue),
	// if known.
	Initial *int64 `json:"initial,omitempty"`

	// IsMultiplexer marks a multiplexer switch: its value selects which
	// multiplexed signals the payload carries.
	IsMultiplexer bool `json:"isMultiplexer,omitempty"`
	// Multiplexer names the switch this signal depends on ("" if it is
	// always present), and MuxValues the switch values for which it is
	// present. A switch may itself be multiplexed (extended
	// multiplexing).
	Multiplexer string     `json:"multiplexer,omitempty"`
	MuxValues   []MuxRange `json:"muxValues,omitempty"`

	Attributes []Attribute `json:"attributes,omitempty"`
}

// MuxRange is an inclusive range of multiplexer switch values.
type MuxRange struct {
	Low  uint64 `json:"low"`
	High uint64 `json:"high"`
}

// ValueDescription names a raw value of a signal.
type ValueDescription struct {
	Value       int64  `json:"value"`
	Description string `json:"description"`
}

// ValueTable is a named, reusable set of value descriptions.
type ValueTable struct {
	Name   string             `json:"name"`
	Values []ValueDescription `json:"values"`
}

// SignalGroup is a named group of signals of a message.
type SignalGroup struct {
	Name        string   `json:"name"`
	Repetitions int      `json:"repetitions"`
	Signals     []string `json:"signals"`
}

// ObjectType is what an attribute applies to.
type ObjectType string

// Attribute object types.
const (
	ObjectNetwork ObjectType = "network"
	ObjectNode    ObjectType = "node"
	ObjectMessage ObjectType = "message"
	ObjectSignal  ObjectType = "signal"
	ObjectEnvVar  ObjectType = "envvar"
)

// AttributeType is the value type of an attribute.
type AttributeType string

// Attribute types.
const (
	AttrInt    AttributeType = "int"
	AttrHex    AttributeType = "hex"
	AttrFloat  AttributeType = "float"
	AttrString AttributeType = "string"
	AttrEnum   AttributeType = "enum"
)

// AttributeDefinition declares an attribute.
type AttributeDefinition struct {
	Name    string        `json:"name"`
	Object  ObjectType    `json:"object"`
	Type    AttributeType `json:"type"`
	Min     float64       `json:"min,omitempty"`
	Max     float64       `json:"max,omitempty"`
	Enum    []string      `json:"enum,omitempty"`
	Default *Attribute    `json:"default,omitempty"`
}

// Attribute is an attribute value. Exactly one of the value fields is
// meaningful, by the definition's type: Int for int, hex and enum (the
// index into the definition's Enum), Float for float, String for string.
type Attribute struct {
	Name   string  `json:"name"`
	Int    int64   `json:"int,omitempty"`
	Float  float64 `json:"float,omitempty"`
	String string  `json:"string,omitempty"`
}

// Message returns the message with the given identifier and format, or
// nil.
func (db *Database) Message(id canbus.ID, extended bool) *Message {
	for _, m := range db.Messages {
		if m.ID == id && m.Extended == extended {
			return m
		}
	}
	return nil
}

// MessageByName returns the message with the given name, or nil.
func (db *Database) MessageByName(name string) *Message {
	for _, m := range db.Messages {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// Node returns the node with the given name, or nil.
func (db *Database) Node(name string) *Node {
	for _, n := range db.Nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// AttributeDefinition returns the definition of an attribute for an
// object type, or nil.
func (db *Database) AttributeDefinition(name string, obj ObjectType) *AttributeDefinition {
	for i := range db.AttributeDefinitions {
		if d := &db.AttributeDefinitions[i]; d.Name == name && d.Object == obj {
			return d
		}
	}
	return nil
}

// Signal returns the signal with the given name, or nil.
func (m *Message) Signal(name string) *Signal {
	for _, s := range m.Signals {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// Multiplexed reports whether the message has multiplexed signals.
func (m *Message) Multiplexed() bool {
	for _, s := range m.Signals {
		if s.IsMultiplexer {
			return true
		}
	}
	return false
}

// Label returns the description of a raw value, or "".
func (s *Signal) Label(raw int64) string {
	i := sort.Search(len(s.Values), func(i int) bool { return s.Values[i].Value >= raw })
	if i < len(s.Values) && s.Values[i].Value == raw {
		return s.Values[i].Description
	}
	return ""
}

// LabelValue returns the raw value of a description.
func (s *Signal) LabelValue(label string) (int64, bool) {
	for _, v := range s.Values {
		if v.Description == label {
			return v.Value, true
		}
	}
	return 0, false
}

// SortValues sorts value descriptions by value. Parsers call it; Label
// relies on it.
func SortValues(v []ValueDescription) {
	sort.SliceStable(v, func(i, j int) bool { return v[i].Value < v[j].Value })
}

// ActiveFor reports whether the signal is present when its multiplexer
// switch has the value v.
func (s *Signal) ActiveFor(v uint64) bool {
	for _, r := range s.MuxValues {
		if v >= r.Low && v <= r.High {
			return true
		}
	}
	return false
}
