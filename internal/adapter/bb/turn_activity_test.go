package bb

import (
	"context"
	"testing"
	"time"
)

// item builds an item event in the shape a real BB 0.48 log records:
// {"providerThreadId", "item": {"id", "type", ...}}. IDs are fictional.
func item(kind, id, typ string, t time.Time) string {
	return at(`{"type":"`+kind+`","data":{"providerThreadId":"p1","item":{"id":"`+id+`","type":"`+typ+`"}}}`, t)
}

// An active thread's observation says what is known of its current turn:
// which turn it is, when it last logged something, and whether a tool item
// it started is still open. Anything the bookkeeping cannot be sure of
// counts as a tool running, so the turn is treated as slow, never as hung.
func TestTurnActivityFromRealItemShapes(t *testing.T) {
	start := now.Add(-time.Hour)
	for _, tc := range []struct {
		name  string
		items []string
		tools bool
	}{
		{"model items only, all ended", []string{item("item/started", "it_r1", "reasoning", start), item("item/completed", "it_r1", "reasoning", start), item("item/started", "it_m1", "agentMessage", start)}, false},
		{"model request open (where a hung turn stops)", []string{item("item/started", "it_r2", "reasoning", start)}, false},
		{"command running", []string{item("item/started", "it_c1", "commandExecution", start)}, true},
		{"command ended", []string{item("item/started", "it_c1", "commandExecution", start), item("item/completed", "it_c1", "commandExecution", start)}, false},
		{"tool call ended, file read open", []string{item("item/started", "it_t1", "toolCall", start), item("item/completed", "it_t1", "toolCall", start), item("item/started", "it_f1", "fileRead", start)}, true},
		{"background task open", []string{item("item/started", "it_b1", "backgroundTask", start)}, true},
		{"background task ended by its own event", []string{item("item/started", "it_b1", "backgroundTask", start), item("item/backgroundTask/completed", "it_b1", "backgroundTask", start)}, false},
		{"unknown item type", []string{item("item/started", "it_x1", "somethingNew", start)}, true},
		{"item without an ID", []string{item("item/started", "", "commandExecution", start)}, true},
		{"completion of an item never seen", []string{item("item/completed", "it_gone", "commandExecution", start)}, false},
		{"another provider thread's tool", []string{at(`{"type":"item/started","data":{"providerThreadId":"other","item":{"id":"it_o1","type":"commandExecution"}}}`, start)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := `[` + at(turnStarted, start)
			for _, it := range tc.items {
				log += `,` + it
			}
			a, _ := newFake(`{"thread":{"status":"active"}}`, log+`]`, false)
			o, err := a.Observe(context.Background())
			if err != nil || o.Turn == nil || o.Turn.ToolsRunning != tc.tools || o.Turn.ID == "" || o.Turn.LastActivityAt.IsZero() {
				t.Fatalf("%+v %v", o.Turn, err)
			}
		})
	}
}

// A new turn starts with clean bookkeeping and a new ID. The turn is known
// only while the thread is active and the log could be read.
func TestTurnActivityIsPerTurnAndOnlyForAnActiveThread(t *testing.T) {
	start := now.Add(-time.Hour)
	first := `[` + at(turnStarted, start) + `,` + item("item/started", "it_c1", "commandExecution", start) + `]`
	a, _ := newFake(`{"thread":{"status":"active"}}`, first, false)
	dir := t.TempDir()
	a.WithStateDir(dir)
	o1, _ := a.Observe(context.Background())
	second := first[:len(first)-1] + `,` + at(turnStarted, start.Add(time.Minute)) + `]`
	b, _ := newFake(`{"thread":{"status":"active"}}`, second, false)
	b.WithStateDir(dir)
	o2, _ := b.Observe(context.Background())
	if o1.Turn == nil || o2.Turn == nil || !o1.Turn.ToolsRunning || o2.Turn.ToolsRunning || o1.Turn.ID == o2.Turn.ID {
		t.Fatalf("%+v %+v", o1.Turn, o2.Turn)
	}
	for _, status := range []string{"idle", "error"} {
		c, _ := newFake(`{"thread":{"status":"`+status+`"}}`, first, false)
		if o, _ := c.Observe(context.Background()); o.Turn != nil {
			t.Fatalf("%s: %+v", status, o.Turn)
		}
	}
}
