// SPDX-License-Identifier: MIT

package canbus

// fdLens maps a data length code to a CAN FD payload length.
var fdLens = [16]uint8{0, 1, 2, 3, 4, 5, 6, 7, 8, 12, 16, 20, 24, 32, 48, 64}

// DLCToLen returns the payload length of a data length code (only its low
// 4 bits are used). fd selects the CAN FD table: codes 9..15 mean 12, 16,
// 20, 24, 32, 48 and 64 bytes. For classical CAN they all mean 8 bytes.
func DLCToLen(dlc uint8, fd bool) int {
	dlc &= 0xF
	if !fd {
		if dlc > 8 {
			return 8
		}
		return int(dlc)
	}
	return int(fdLens[dlc])
}

// LenToDLC returns the smallest data length code whose CAN FD length holds
// n bytes: 0..8 map to themselves, 9..12 to 9, ..., 49..64 to 15. Lengths
// above 64 return 15.
func LenToDLC(n int) uint8 {
	if n <= 8 {
		if n < 0 {
			return 0
		}
		return uint8(n)
	}
	for dlc := uint8(9); dlc < 16; dlc++ {
		if int(fdLens[dlc]) >= n {
			return dlc
		}
	}
	return 15
}

// ValidFDLen reports whether n is a payload length a CAN FD frame can
// carry: 0..8, 12, 16, 20, 24, 32, 48 or 64.
func ValidFDLen(n int) bool {
	if n < 0 || n > MaxFDLen {
		return false
	}
	return int(fdLens[LenToDLC(n)]) == n
}

// RoundUpFDLen returns the smallest valid CAN FD length of at least n
// bytes, or 64 when n exceeds 64.
func RoundUpFDLen(n int) int { return int(fdLens[LenToDLC(n)]) }

// PadFD returns data padded with pad to the next valid CAN FD length. data
// is returned unchanged when its length is already valid.
func PadFD(data []byte, pad byte) []byte {
	n := RoundUpFDLen(len(data))
	if n <= len(data) {
		return data
	}
	out := make([]byte, n)
	copy(out, data)
	for i := len(data); i < n; i++ {
		out[i] = pad
	}
	return out
}
