//go:build unix

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
)

func n64(n int64) *int64 { return &n }

func countsOf(u *adapter.TurnUsage) []*int64 {
	return []*int64{u.Input, u.CachedInput, u.CacheWriteInput, u.Output, u.ReasoningOutput}
}

func sameCounts(got, want []*int64) bool {
	for i := range want {
		if (got[i] == nil) != (want[i] == nil) || got[i] != nil && *got[i] != *want[i] {
			return false
		}
	}
	return true
}

// Only the typed usage object on turn.completed is read, and only counts
// that are plain integers in a sane range. Anything else is absent, never
// estimated, and never changes how the turn itself is read.
func TestCodexTurnUsageReadsOnlyTypedCounts(t *testing.T) {
	head := `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"turn.started"}` + "\n"
	done := func(usage string) string { return head + `{"type":"turn.completed","usage":` + usage + `}` }
	cases := []struct {
		name   string
		events string
		want   []*int64 // nil: no reading at all
	}{
		{"every count", codexWithHookWarning, []*int64{n64(21000), n64(13000), n64(0), n64(5), n64(0)}},
		{"input and output only", codexOK, []*int64{n64(10), nil, nil, n64(2), nil}},
		{"no usage object", head + `{"type":"turn.completed"}`, nil},
		{"usage is not an object", done(`"many"`), nil},
		{"usage is null", done(`null`), nil},
		{"empty object", done(`{}`), nil},
		{"unknown keys only", done(`{"total_tokens":12}`), nil},
		{"negative", done(`{"input_tokens":-1,"output_tokens":3}`), []*int64{nil, nil, nil, n64(3), nil}},
		{"fraction", done(`{"input_tokens":1.5,"output_tokens":3}`), []*int64{nil, nil, nil, n64(3), nil}},
		{"exponent", done(`{"input_tokens":1e3,"output_tokens":3}`), []*int64{nil, nil, nil, n64(3), nil}},
		{"string", done(`{"input_tokens":"10","output_tokens":3}`), []*int64{nil, nil, nil, n64(3), nil}},
		{"null count", done(`{"input_tokens":null,"output_tokens":3}`), []*int64{nil, nil, nil, n64(3), nil}},
		{"leading zero", done(`{"input_tokens":012,"output_tokens":3}`), nil}, // not JSON: the event is unreadable
		{"at the bound", done(`{"input_tokens":10000000000}`), []*int64{n64(MaxTurnTokens), nil, nil, nil, nil}},
		{"over the bound", done(`{"input_tokens":10000000001,"output_tokens":3}`), []*int64{nil, nil, nil, n64(3), nil}},
		{"far over int64", done(`{"input_tokens":99999999999999999999999,"output_tokens":3}`), []*int64{nil, nil, nil, n64(3), nil}},
		{"failed turn", codexFailedCoded, nil},
		{"the last turn.completed wins", done(`{"input_tokens":7}`) + "\n" + `{"type":"turn.completed"}`, nil},
	}
	for _, c := range cases {
		o := ParseCodexEvents([]byte(c.events))
		switch {
		case c.want == nil && o.Usage != nil:
			t.Errorf("%s: read %+v, want no reading", c.name, *o.Usage)
		case c.want != nil && (o.Usage == nil || !sameCounts(countsOf(o.Usage), c.want)):
			t.Errorf("%s: read %+v, want %v", c.name, o.Usage, c.want)
		}
		if c.want != nil && !o.Completed {
			t.Errorf("%s: the turn no longer reads as completed", c.name)
		}
	}
	// A malformed usage value leaves the reading of the turn as it was.
	plain, odd := ParseCodexEvents([]byte(head+`{"type":"turn.completed"}`)), ParseCodexEvents([]byte(done(`["x"]`)))
	if plain.Completed != odd.Completed || plain.Failed != odd.Failed || plain.Class != odd.Class || plain.ThreadID != odd.ThreadID || plain.Evidence != odd.Evidence {
		t.Errorf("a malformed usage value changed the turn: %+v against %+v", odd, plain)
	}
}

// writeTurn lays down a finished turn's files as the wrapper leaves them.
func writeTurn(t *testing.T, dir, key, started, stdout string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"run.json": `{"stopKey":"` + key + `","pid":1,"startedAt":"` + started + `","argv0":"absent","liveness":"lock"}`,
		"exit":     "0\n",
		"stdout":   stdout,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The latest finished turn's usage is named by the turn: every read of one
// turn gives the same identity, a later turn another, and the identity does
// not carry the run record's stop key.
func TestLatestTurnUsageNamesTheTurn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	c := NewCodex(config.Codex{CLI: "absent"}, dir)
	if _, ok := c.LatestTurnUsage(); ok {
		t.Fatal("a target that never ran reported usage")
	}
	writeTurn(t, dir, "examplestopkey1", "2026-11-09T10:00:00Z", codexWithHookWarning)
	first, ok := c.LatestTurnUsage()
	again, ok2 := c.LatestTurnUsage()
	if !ok || !ok2 || first.ID == "" || first.ID != again.ID || *first.Input != 21000 {
		t.Fatalf("%+v %v / %+v %v", first, ok, again, ok2)
	}
	if strings.Contains(first.ID, "examplestopkey1") {
		t.Fatalf("the identity %q carries the stop key", first.ID)
	}
	writeTurn(t, dir, "examplestopkey2", "2026-11-09T11:00:00Z", codexWithHookWarning)
	if next, ok := c.LatestTurnUsage(); !ok || next.ID == first.ID {
		t.Fatalf("a later turn with the same output was named as the first: %q", next.ID)
	}
	writeTurn(t, dir, "examplestopkey3", "2026-11-09T12:00:00Z", codexFailedProse)
	if u, ok := c.LatestTurnUsage(); ok {
		t.Fatalf("a turn that reported no usage gave %+v", u)
	}
}

// Claude Code's result object has no captured usage shape, so no reading is
// taken from it, whatever its output holds.
func TestClaudeReportsNoTurnUsage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	writeTurn(t, dir, "examplestopkey1", "2026-11-09T10:00:00Z",
		`{"type":"result","subtype":"success","is_error":false,"session_id":"00000000-0000-4000-8000-000000000001","usage":{"input_tokens":10,"output_tokens":2}}`)
	c := NewClaude(config.Claude{CLI: "absent"}, dir)
	if u, ok := c.LatestTurnUsage(); ok {
		t.Fatalf("read %+v", u)
	}
	if claudeSpec.Read(Status{Phase: Finished, Stdout: []byte(`{"type":"result","usage":{"input_tokens":10}}`)}).Usage != nil {
		t.Fatal("the Claude Code reader set a usage reading")
	}
}
