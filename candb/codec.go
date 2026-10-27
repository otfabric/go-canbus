// SPDX-License-Identifier: MIT

package candb

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/candb/bits"
)

// Codec errors. They are wrapped with detail; test with errors.Is.
var (
	ErrUnknownMessage = errors.New("candb: unknown message")
	ErrUnknownSignal  = errors.New("candb: unknown signal")
	ErrShortPayload   = errors.New("candb: payload shorter than the message")
	ErrRange          = errors.New("candb: value out of range")
	ErrMissingSignal  = errors.New("candb: signal value missing")
	ErrInactiveSignal = errors.New("candb: signal not present for this multiplexer value")
	ErrLayout         = errors.New("candb: invalid signal layout")
)

// Value is a decoded signal value.
type Value struct {
	// Raw is the field's bit pattern, zero-extended. For floating-point
	// signals it is the IEEE 754 bit pattern.
	Raw uint64 `json:"raw,string"`
	// Signed is Raw sign-extended for signed integer signals; for
	// unsigned signals it equals Raw when Raw fits in an int64.
	Signed int64 `json:"signed,string"`
	// Physical is the scaled value: Signed (or Raw, or the float) ×
	// factor + offset.
	Physical float64 `json:"physical"`
	// Label is the value description of the raw value, if any.
	Label string `json:"label,omitempty"`
}

// RawString formats the raw integer value exactly: the signed value for
// signed signals, the unsigned value otherwise, the bit pattern for
// floating-point signals.
func (s *Signal) RawString(v Value) string {
	if s.ValueType == Integer && s.Signed {
		return strconv.FormatInt(v.Signed, 10)
	}
	return strconv.FormatUint(v.Raw, 10)
}

// layout returns the bit position for the bits package.
func (s *Signal) layout() (pos int, err error) {
	if s.Length < 1 || s.Length > 64 {
		return 0, fmt.Errorf("%w: %s has %d bits", ErrLayout, s.Name, s.Length)
	}
	if s.StartBit < 0 {
		return 0, fmt.Errorf("%w: %s starts at bit %d", ErrLayout, s.Name, s.StartBit)
	}
	if s.ByteOrder == BigEndian {
		return bits.MotorolaStart(s.StartBit), nil
	}
	return s.StartBit, nil
}

// Span returns the number of payload bytes the signal needs.
func (s *Signal) Span() int {
	pos, err := s.layout()
	if err != nil {
		return math.MaxInt32
	}
	return (pos + s.Length + 7) / 8
}

// Extract returns the raw bit pattern of the signal in data.
func (s *Signal) Extract(data []byte) (uint64, error) {
	pos, err := s.layout()
	if err != nil {
		return 0, err
	}
	if pos+s.Length > 8*len(data) {
		return 0, fmt.Errorf("%w: %s needs %d bytes, payload has %d", ErrShortPayload, s.Name, s.Span(), len(data))
	}
	if s.ByteOrder == BigEndian {
		return bits.ExtractBE(data, pos, s.Length), nil
	}
	return bits.ExtractLE(data, pos, s.Length), nil
}

// Insert writes the raw bit pattern raw (its low Length bits) into data.
func (s *Signal) Insert(data []byte, raw uint64) error {
	pos, err := s.layout()
	if err != nil {
		return err
	}
	if pos+s.Length > 8*len(data) {
		return fmt.Errorf("%w: %s needs %d bytes, payload has %d", ErrShortPayload, s.Name, s.Span(), len(data))
	}
	if s.ByteOrder == BigEndian {
		bits.InsertBE(data, pos, s.Length, raw)
	} else {
		bits.InsertLE(data, pos, s.Length, raw)
	}
	return nil
}

// Value interprets a raw bit pattern: sign extension, float conversion,
// scaling and label.
func (s *Signal) Value(raw uint64) Value {
	v := Value{Raw: raw}
	switch s.ValueType {
	case Float32:
		f := float64(math.Float32frombits(uint32(raw)))
		v.Signed = int64(raw)
		v.Physical = f*s.Factor + s.Offset
		return v
	case Float64:
		v.Signed = int64(raw)
		v.Physical = math.Float64frombits(raw)*s.Factor + s.Offset
		return v
	default:
	}
	if s.Signed {
		v.Signed = bits.SignExtend(raw, s.Length)
		v.Physical = float64(v.Signed)*s.Factor + s.Offset
	} else {
		v.Signed = int64(raw)
		v.Physical = float64(raw)*s.Factor + s.Offset
	}
	v.Label = s.Label(v.Signed)
	return v
}

// Decode extracts and interprets the signal in data.
func (s *Signal) Decode(data []byte) (Value, error) {
	raw, err := s.Extract(data)
	if err != nil {
		return Value{}, err
	}
	return s.Value(raw), nil
}

// RawFromInt converts an integer raw value (signed for signed signals) to
// the field's bit pattern, checking that it fits.
func (s *Signal) RawFromInt(v int64) (uint64, error) {
	if s.ValueType != Integer {
		return 0, fmt.Errorf("%w: %s is a %s signal", ErrRange, s.Name, s.ValueType)
	}
	if s.Signed {
		if !bits.FitsSigned(v, s.Length) {
			return 0, fmt.Errorf("%w: %s: %d does not fit in %d signed bits", ErrRange, s.Name, v, s.Length)
		}
		return bits.Truncate(uint64(v), s.Length), nil
	}
	if v < 0 || !bits.FitsUnsigned(uint64(v), s.Length) {
		return 0, fmt.Errorf("%w: %s: %d does not fit in %d unsigned bits", ErrRange, s.Name, v, s.Length)
	}
	return uint64(v), nil
}

// RawFromUint checks an unsigned raw value or bit pattern against the
// field width. For signed signals the value is taken as the bit pattern.
func (s *Signal) RawFromUint(v uint64) (uint64, error) {
	if !bits.FitsUnsigned(v, s.Length) {
		return 0, fmt.Errorf("%w: %s: %d does not fit in %d bits", ErrRange, s.Name, v, s.Length)
	}
	return v, nil
}

// RawFromPhysical converts a physical value to the field's bit pattern:
// (physical − offset) / factor, rounded half to even for integer signals
// (as cantools and canmatrix round), converted to IEEE 754 for float
// signals.
func (s *Signal) RawFromPhysical(p float64) (uint64, error) {
	if math.IsNaN(p) || math.IsInf(p, 0) {
		if s.ValueType == Integer {
			return 0, fmt.Errorf("%w: %s: %v", ErrRange, s.Name, p)
		}
	}
	factor := s.Factor
	if factor == 0 {
		factor = 1
	}
	x := (p - s.Offset) / factor
	switch s.ValueType {
	case Float32:
		return uint64(math.Float32bits(float32(x))), nil
	case Float64:
		return math.Float64bits(x), nil
	default:
	}
	r := math.RoundToEven(x)
	if s.Signed {
		if r < -(1<<63) || r >= 1<<63 {
			return 0, fmt.Errorf("%w: %s: %v", ErrRange, s.Name, p)
		}
		return s.RawFromInt(int64(r))
	}
	if r < 0 || r >= 1<<64 {
		return 0, fmt.Errorf("%w: %s: %v", ErrRange, s.Name, p)
	}
	return s.RawFromUint(uint64(r))
}

// InRange reports whether a physical value lies within the signal's
// declared minimum and maximum; a signal with both 0 has no range.
func (s *Signal) InRange(p float64) bool {
	if s.Minimum == 0 && s.Maximum == 0 {
		return true
	}
	return p >= s.Minimum && p <= s.Maximum
}

// DecodedSignal is one signal of a decoded message.
type DecodedSignal struct {
	Signal *Signal
	Value
}

// active computes which signals the payload carries: unmultiplexed
// signals always, multiplexed ones when their switch is present and has
// one of their values. switches maps switch names to their raw values.
func (m *Message) active(switchValue func(*Signal) (uint64, bool, error)) (map[*Signal]bool, error) {
	byName := make(map[string]*Signal, len(m.Signals))
	for _, s := range m.Signals {
		byName[s.Name] = s
	}
	act := make(map[*Signal]bool, len(m.Signals))
	state := make(map[*Signal]int) // 1 = visiting, 2 = done
	var visit func(s *Signal) (bool, error)
	visit = func(s *Signal) (bool, error) {
		switch state[s] {
		case 1:
			return false, fmt.Errorf("%w: multiplexer cycle at %s", ErrLayout, s.Name)
		case 2:
			return act[s], nil
		}
		state[s] = 1
		on := true
		if s.Multiplexer != "" {
			sw := byName[s.Multiplexer]
			if sw == nil || !sw.IsMultiplexer {
				return false, fmt.Errorf("%w: %s depends on unknown multiplexer %q", ErrLayout, s.Name, s.Multiplexer)
			}
			swOn, err := visit(sw)
			if err != nil {
				return false, err
			}
			on = false
			if swOn {
				v, ok, err := switchValue(sw)
				if err != nil {
					return false, err
				}
				on = ok && s.ActiveFor(v)
			}
		}
		act[s], state[s] = on, 2
		return on, nil
	}
	for _, s := range m.Signals {
		if _, err := visit(s); err != nil {
			return nil, err
		}
	}
	return act, nil
}

// Decode decodes the signals the payload carries, in the order of
// m.Signals. The payload must hold at least Length bytes.
func (m *Message) Decode(data []byte) ([]DecodedSignal, error) {
	if len(data) < m.Length {
		return nil, fmt.Errorf("%w: %s needs %d bytes, got %d", ErrShortPayload, m.Name, m.Length, len(data))
	}
	data = data[:m.Length]
	act, err := m.active(func(sw *Signal) (uint64, bool, error) {
		v, err := sw.Extract(data)
		return v, err == nil, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]DecodedSignal, 0, len(m.Signals))
	for _, s := range m.Signals {
		if !act[s] {
			continue
		}
		v, err := s.Decode(data)
		if err != nil {
			return nil, err
		}
		out = append(out, DecodedSignal{Signal: s, Value: v})
	}
	return out, nil
}

// Input is a value to encode: physical, raw or a value description.
type Input struct {
	kind  uint8 // 0 physical, 1 raw unsigned, 2 raw signed, 3 label, 4 physical text
	phys  float64
	raw   uint64
	sraw  int64
	label string
}

// Phys is a physical value.
func Phys(v float64) Input { return Input{kind: 0, phys: v} }

// Raw is a raw unsigned value or bit pattern.
func Raw(v uint64) Input { return Input{kind: 1, raw: v} }

// RawInt is a raw integer value, signed for signed signals.
func RawInt(v int64) Input { return Input{kind: 2, sraw: v} }

// Label is a value description, encoded as its raw value.
func Label(s string) Input { return Input{kind: 3, label: s} }

// PhysText is a physical value given as decimal text (a JSON number, a
// command-line argument). For an integer signal with factor 1 and offset
// 0, an integer text is the raw value exactly, without passing through
// float64, so 64-bit values keep every bit (as cantools' identity
// conversion and canmatrix's decimal arithmetic do). Otherwise it is the
// same as [Phys] of the parsed number.
func PhysText(s string) Input { return Input{kind: 4, label: s} }

// EncodeOptions control [Message.EncodeWith].
type EncodeOptions struct {
	// Lax skips the check of physical values against the signal's
	// minimum and maximum. Raw values are always checked against the
	// field width.
	Lax bool
	// FillMissing encodes absent signals with their initial value (or 0)
	// instead of failing with ErrMissingSignal.
	FillMissing bool
}

// Encode encodes a message strictly: every signal the payload carries
// must be given and physical values must lie within their range.
func (m *Message) Encode(values map[string]Input) ([]byte, error) {
	return m.EncodeWith(values, EncodeOptions{})
}

// rawOf converts an input for one signal.
func (s *Signal) rawOf(in Input, opts EncodeOptions) (uint64, error) {
	switch in.kind {
	case 1:
		return s.RawFromUint(in.raw)
	case 2:
		if s.ValueType != Integer {
			return s.RawFromUint(uint64(in.sraw))
		}
		return s.RawFromInt(in.sraw)
	case 4:
		if s.ValueType == Integer && s.Factor == 1 && s.Offset == 0 {
			if s.Signed {
				if v, err := strconv.ParseInt(in.label, 10, 64); err == nil {
					if !opts.Lax && !s.InRange(float64(v)) {
						return 0, fmt.Errorf("%w: %s: %s outside [%v, %v]", ErrRange, s.Name, in.label, s.Minimum, s.Maximum)
					}
					return s.RawFromInt(v)
				}
			} else if v, err := strconv.ParseUint(in.label, 10, 64); err == nil {
				if !opts.Lax && !s.InRange(float64(v)) {
					return 0, fmt.Errorf("%w: %s: %s outside [%v, %v]", ErrRange, s.Name, in.label, s.Minimum, s.Maximum)
				}
				return s.RawFromUint(v)
			}
		}
		f, err := strconv.ParseFloat(in.label, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %s: %q is not a number", ErrRange, s.Name, in.label)
		}
		return s.rawOf(Phys(f), opts)
	case 3:
		v, ok := s.LabelValue(in.label)
		if !ok {
			return 0, fmt.Errorf("%w: %s has no value %q", ErrRange, s.Name, in.label)
		}
		if s.Signed {
			return s.RawFromInt(v)
		}
		return s.RawFromUint(uint64(v))
	}
	if !opts.Lax && !s.InRange(in.phys) {
		return 0, fmt.Errorf("%w: %s: %v outside [%v, %v]", ErrRange, s.Name, in.phys, s.Minimum, s.Maximum)
	}
	return s.RawFromPhysical(in.phys)
}

// EncodeWith encodes a message. The payload has Length bytes (for CAN FD
// messages rounded up to a CAN FD length) with unused bits 0. Values for
// unknown signals, or for multiplexed signals the multiplexer values do
// not select, are errors.
func (m *Message) EncodeWith(values map[string]Input, opts EncodeOptions) ([]byte, error) {
	for name := range values {
		if m.Signal(name) == nil {
			return nil, fmt.Errorf("%w: %s has no signal %q", ErrUnknownSignal, m.Name, name)
		}
	}
	raws := make(map[*Signal]uint64, len(m.Signals))
	get := func(s *Signal) (uint64, bool, error) {
		if r, ok := raws[s]; ok {
			return r, true, nil
		}
		in, ok := values[s.Name]
		if !ok {
			if !opts.FillMissing {
				return 0, false, fmt.Errorf("%w: %s.%s", ErrMissingSignal, m.Name, s.Name)
			}
			r := uint64(0)
			if s.Initial != nil {
				var err error
				if r, err = s.rawOf(RawInt(*s.Initial), opts); err != nil {
					return 0, false, err
				}
			}
			raws[s] = r
			return r, true, nil
		}
		r, err := s.rawOf(in, opts)
		if err != nil {
			return 0, false, err
		}
		raws[s] = r
		return r, true, nil
	}
	act, err := m.active(get)
	if err != nil {
		return nil, err
	}
	n := m.Length
	if m.FD {
		n = canbus.RoundUpFDLen(n)
	}
	data := make([]byte, n)
	for _, s := range m.Signals {
		if !act[s] {
			if _, given := values[s.Name]; given {
				return nil, fmt.Errorf("%w: %s.%s", ErrInactiveSignal, m.Name, s.Name)
			}
			continue
		}
		r, _, err := get(s)
		if err != nil {
			return nil, err
		}
		if err := s.Insert(data[:m.Length], r); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// Frame encodes a message into a frame of its format.
func (m *Message) Frame(values map[string]Input, opts EncodeOptions) (canbus.Frame, error) {
	data, err := m.EncodeWith(values, opts)
	if err != nil {
		return canbus.Frame{}, err
	}
	switch {
	case m.FD:
		return canbus.NewFD(m.ID, m.Extended, m.BRS, data)
	case m.Extended:
		return canbus.NewExtended(m.ID, data)
	}
	return canbus.NewStandard(m.ID, data)
}

// DecodeFrame finds the message of a frame and decodes it.
func (db *Database) DecodeFrame(f *canbus.Frame) (*Message, []DecodedSignal, error) {
	m := db.Message(f.ID, f.IsExtended())
	if m == nil || f.IsRemote() || f.IsError() {
		return nil, nil, fmt.Errorf("%w: %s", ErrUnknownMessage, f.String())
	}
	sigs, err := m.Decode(f.Payload())
	return m, sigs, err
}
