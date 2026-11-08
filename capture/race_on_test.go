// SPDX-License-Identifier: MIT

//go:build race

package capture

// raceDetector is true in a -race build: the recorded-traffic tests then
// check the first raceLines lines of each log, not the whole corpus (the
// codecs are single-goroutine; the whole corpus runs without -race).
const raceDetector = true
