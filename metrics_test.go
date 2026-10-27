// SPDX-License-Identifier: MIT

package canbus

import (
	"context"
	"log/slog"
	"testing"
)

// TestNops calls the no-op implementations: they must not panic.
func TestNops(t *testing.T) {
	var m NopMetrics
	m.OnFrameSent("x", nil)
	m.OnFrameReceived("x", nil)
	m.OnDrop("x", DropFault, 1)
	m.OnError("x", "send", nil)
	m.OnQueue("x", 1, 2)
	m.OnStateChange("x", StateActive, StateDown)
	var h slog.Handler = discardHandler{}
	if h.Enabled(context.Background(), slog.LevelError) || h.Handle(context.Background(), slog.Record{}) != nil {
		t.Fatal("discard handler")
	}
	_ = h.WithAttrs(nil).WithGroup("g")
}
