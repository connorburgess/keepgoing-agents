package config

import (
	"fmt"
	"math"
)

// ContextWindow is the context-threshold reset of a loop's conversation.
// This build only previews it: status says what it would do at the next
// turn boundary, and nothing acts on it. It has no default: with no
// reset_at it is off.
type ContextWindow struct {
	// ResetAt is the share of the conversation's context window at or above
	// which a reset would be due, for example 0.8.
	ResetAt *float64 `yaml:"reset_at,omitempty"`
}

// Bounds of loop.context_window.reset_at. Below half a reset gives up a
// conversation with room left in it; above 95% the provider's own
// compaction or limit is likely to act first.
const (
	ContextWindowResetAtMin = 0.5
	ContextWindowResetAtMax = 0.95
)

// ContextWindowResetAt is the loop's threshold, nil when it is not set.
func (l Loop) ContextWindowResetAt() *float64 {
	if l.ContextWindow == nil {
		return nil
	}
	return l.ContextWindow.ResetAt
}

// contextWindowErrors checks loop.context_window. Only a loop whose agent
// reports a context window reading may set it: BB, today.
func (l Loop) contextWindowErrors() []error {
	cw := l.ContextWindow
	if cw == nil {
		return nil
	}
	var errs []error
	v := cw.ResetAt
	switch {
	case v == nil:
		errs = append(errs, fmt.Errorf("loop.context_window.reset_at is required when loop.context_window is set; it has no default"))
	case math.IsNaN(*v) || *v < ContextWindowResetAtMin || *v > ContextWindowResetAtMax:
		errs = append(errs, fmt.Errorf("loop.context_window.reset_at must be between %.2f and %.2f, a share of the context window", ContextWindowResetAtMin, ContextWindowResetAtMax))
	}
	if l.Adapter != "bb" {
		errs = append(errs, fmt.Errorf("loop.context_window.reset_at needs a loop on a BB thread (rule: context_window_reading): only BB reports a context window reading, and a %s loop reports none", l.Adapter))
	}
	return errs
}
