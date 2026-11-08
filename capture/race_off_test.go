// SPDX-License-Identifier: MIT

//go:build !race

package capture

// raceDetector is true in a -race build (see race_on_test.go).
const raceDetector = false
