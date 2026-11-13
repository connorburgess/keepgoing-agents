package bb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// fakeLanes answers bb for a stop that covers the lanes. Its answers are
// keyed by the command line; a missing answer fails the command.
func fakeLanes(t *testing.T, answers map[string]string) (*Adapter, *[]string) {
	t.Helper()
	a := New(config.BB{Thread: "thr_example123"})
	var calls []string
	a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		line := strings.Join(args, " ")
		calls = append(calls, line)
		if stdin != "" {
			t.Errorf("%s was given stdin", line)
		}
		out, ok := answers[line]
		if !ok {
			return nil, errors.New("exit status 1")
		}
		if strings.Contains(out, `"ok":false`) {
			return []byte(out), errors.New("exit status 1")
		}
		return []byte(out), nil
	}
	return a, &calls
}

func show(id, parent, status string, extra string) string {
	return fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":%q,"status":%q,"title":"PRIVATE_TITLE"%s}}`, id, parent, status, extra)
}

func TestLaneChildrenVerifiesEachChild(t *testing.T) {
	list := `[{"id":"thr_lane_a"},{"id":"thr_lane_b"},{"id":"thr_lane_c"},{"id":"thr_lane_d"},{"id":"thr_lane_e"},{"id":"thr_lane_a"},{"id":"thr_example123"},{"id":"not a thread"}]`
	a, calls := fakeLanes(t, map[string]string{
		"thread list --parent-thread thr_example123 --include-hidden --json": list,
		"thread show thr_lane_a --json":                                      show("thr_lane_a", "thr_example123", "active", ""),
		"thread show thr_lane_b --json":                                      show("thr_lane_b", "thr_other", "active", ""),                                 // another parent
		"thread show thr_lane_c --json":                                      show("thr_lane_c", "thr_example123", "error", `,"queuedMessageCount":1`),      // a retry waits
		"thread show thr_lane_d --json":                                      show("thr_lane_d", "thr_example123", "active", `,"archivedAt":1700000000000`), // archived
		"thread show thr_lane_e --json":                                      `{"ok":false,"error":{"code":"thread_not_found"}}`,
	})
	kids, notChecked, err := a.LaneChildren(context.Background(), "thr_example123", 50)
	if err != nil || notChecked != 0 {
		t.Fatalf("%v %d", err, notChecked)
	}
	got := []string{}
	for _, k := range kids {
		got = append(got, fmt.Sprintf("%s:%v:%s:%d", k.ID, k.Verified, k.Status, k.Queued))
	}
	want := "thr_lane_a:true:active:0 thr_lane_b:false::0 thr_lane_c:true:error:1 thr_lane_d:false::0 thr_lane_e:false::0 not a thread:false::0"
	if strings.Join(got, " ") != want {
		t.Fatalf("%s", strings.Join(got, " "))
	}
	for _, c := range *calls {
		if !strings.HasPrefix(c, "thread list ") && !strings.HasPrefix(c, "thread show ") {
			t.Fatalf("ran %q", c)
		}
	}
	// At most max are read; the rest are counted.
	a, calls = fakeLanes(t, map[string]string{
		"thread list --parent-thread thr_example123 --include-hidden --json": list,
		"thread show thr_lane_a --json":                                      show("thr_lane_a", "thr_example123", "active", ""),
		"thread show thr_lane_b --json":                                      show("thr_lane_b", "thr_example123", "idle", ""),
	})
	kids, notChecked, err = a.LaneChildren(context.Background(), "thr_example123", 2)
	if err != nil || len(kids) != 2 || notChecked != 4 || len(*calls) != 3 {
		t.Fatalf("%v %d %d %v", err, len(kids), notChecked, *calls)
	}
	// A listing that fails or is not a bounded array is an error, by class.
	for _, out := range []string{"", `{"ok":false,"error":{"code":"thread_host_offline"}}`, `null`, `{"not":"an array"}`} {
		answers := map[string]string{}
		if out != "" {
			answers["thread list --parent-thread thr_example123 --include-hidden --json"] = out
		}
		a, _ := fakeLanes(t, answers)
		_, _, err := a.LaneChildren(context.Background(), "thr_example123", 50)
		var le *adapter.LaneStopError
		if !errors.As(err, &le) || le.Class == "" {
			t.Errorf("%q: %v", out, err)
		}
	}
	if _, _, err := a.LaneChildren(context.Background(), "thr_x --json; rm", 50); err == nil {
		t.Fatal("listed a parent that is not a thread ID")
	}
}

func TestStopLaneAndCancelLaneRetry(t *testing.T) {
	a, calls := fakeLanes(t, map[string]string{
		"thread stop thr_lane_a --json":           `{"ok":true}`,
		"thread stop thr_lane_b --json":           `{"ok":false,"error":{"code":"thread_host_offline","message":"PRIVATE"}}`,
		"provider-retry cancel thr_lane_a --json": `{"ok":true,"threadId":"thr_lane_a","queuedMessageId":"qm_example1"}`,
		"provider-retry cancel thr_lane_b --json": `{"ok":false,"error":{"code":"no_pending_retry","message":"PRIVATE"}}`,
		"provider-retry cancel thr_lane_c --json": `{"ok":false,"error":{"code":"unknown_command"}}`,
		"provider-retry cancel thr_lane_d --json": `{"ok":true,"threadId":"thr_lane_other"}`,
	})
	ctx := context.Background()
	if err := a.StopLane(ctx, "thr_lane_a"); err != nil {
		t.Fatal(err)
	}
	var le *adapter.LaneStopError
	if err := a.StopLane(ctx, "thr_lane_b"); !errors.As(err, &le) || le.Class != "thread_host_offline" || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("%v", err)
	}
	if err := a.StopLane(ctx, "thr_lane_z"); !errors.As(err, &le) || le.Class != "failed" {
		t.Fatalf("%v", err)
	}
	if ok, err := a.CancelLaneRetry(ctx, "thr_lane_a"); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if ok, err := a.CancelLaneRetry(ctx, "thr_lane_b"); ok || err != nil {
		t.Fatalf("no pending retry: %v %v", ok, err)
	}
	if _, err := a.CancelLaneRetry(ctx, "thr_lane_c"); !errors.As(err, new(adapter.ErrUnsupported)) {
		t.Fatalf("no plugin: %v", err)
	}
	if ok, err := a.CancelLaneRetry(ctx, "thr_lane_d"); ok || err == nil {
		t.Fatalf("another thread's answer was believed: %v %v", ok, err)
	}
	// The supervised thread and anything that is not a thread ID are refused
	// without a command.
	before := len(*calls)
	for _, id := range []string{"thr_example123", "", "thr_x --json; rm"} {
		if a.StopLane(ctx, id) == nil {
			t.Errorf("stopped %q", id)
		}
		if _, err := a.CancelLaneRetry(ctx, id); err == nil {
			t.Errorf("cancelled %q", id)
		}
	}
	if len(*calls) != before {
		t.Fatalf("ran %v", (*calls)[before:])
	}
	for _, c := range *calls {
		if !strings.HasPrefix(c, "thread stop ") && !strings.HasPrefix(c, "provider-retry cancel ") {
			t.Fatalf("ran %q", c)
		}
	}
}
