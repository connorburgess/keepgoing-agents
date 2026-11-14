//go:build !windows

package conformance

import "testing"

// Unix permits removing a completed wrapper's cwd while it exits. Keep the
// fixture's existing logical Settle and process behavior unchanged there.
func trackFixtureWrappers(t *testing.T, project string) func(int) error {
	return func(int) error { return nil }
}
