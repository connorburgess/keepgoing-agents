package scenario

import (
	"os/exec"
	"strings"
	"testing"
)

// The built keepgoing command, the one every scenario runs, cannot skip the
// flush of its records to the device. Test packages that run thousands of
// checks in-process may skip it with snapshot.SkipSyncInTests; this checks
// that nothing in the command can. The function is kept out of line, so it is
// in a binary exactly when something there calls it, and the flag it sets has
// no other writer.
func TestBuiltBinaryCannotSkipTheFlush(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("not run: reading the binary's symbols needs the Go toolchain, which is not on PATH")
	}
	out, err := exec.Command("go", "tool", "nm", keepgoingBin).Output()
	if err != nil {
		t.Fatalf("go tool nm: %v", err)
	}
	symbols := string(out)
	const pkg = "github.com/connorburgess/keepgoing-agents/internal/snapshot."
	// The flag Sync reads must be there: it shows the helper is linked in and
	// that this binary still has its symbols to inspect.
	if !strings.Contains(symbols, pkg+"skipSync") {
		t.Fatalf("the built binary has no %sskipSync symbol: the flush helper is not linked in, or the binary is stripped and cannot be checked", pkg)
	}
	if strings.Contains(symbols, pkg+"SkipSyncInTests") {
		t.Fatal("the built keepgoing binary contains snapshot.SkipSyncInTests: something in the command can turn the flush off")
	}
	// testing.Testing reads this; without it the command has no notion of
	// being a test binary at all.
	if strings.Contains(symbols, " testing.testBinary") {
		t.Fatal("the built keepgoing binary links testing.testBinary")
	}
}
