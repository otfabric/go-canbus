// SPDX-License-Identifier: MIT

// Package leak checks tests for goroutines they leave behind.
package leak

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// Check records the goroutines running now and, when the test ends, fails
// it if more are still running after a grace period.
func Check(t testing.TB) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for {
			n := runtime.NumGoroutine()
			if n <= before {
				return
			}
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<20)
				buf = buf[:runtime.Stack(buf, true)]
				t.Errorf("goroutine leak: %d before, %d after\n%s", before, n, trim(string(buf)))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func trim(s string) string {
	if len(s) > 8000 {
		return s[:8000] + "\n" + strings.Repeat(".", 3)
	}
	return s
}
