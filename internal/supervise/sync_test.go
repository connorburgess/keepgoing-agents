package supervise

import "github.com/connorburgess/keepgoing-agents/internal/snapshot"

// These tests run thousands of checks in this process. None can observe a
// flush to the device, and the flush is nearly all of their wall time. See
// "Slow test packages" in docs/testing.md.
func init() { snapshot.SkipSyncInTests() }
