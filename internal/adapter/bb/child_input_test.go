package bb

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestChildPendingStructuredStatuses(t *testing.T) {
	for _, tc := range []struct {
		name, body             string
		fail, pending, unknown bool
	}{
		{"permission", `[{"threadId":"thr_child","status":"pending","payload":{"type":"approval","command":"fictional private command"}}]`, false, true, false},
		{"question", `[{"threadId":"thr_child","status":"pending","payload":{"type":"question","text":"fictional private question"}}]`, false, true, false},
		{"empty", `[]`, false, false, false},
		{"resolved", `[{"threadId":"thr_child","status":"resolved"}]`, false, false, false},
		{"interrupted", `[{"threadId":"thr_child","status":"interrupted"}]`, false, false, false},
		{"resolving", `[{"threadId":"thr_child","status":"resolving"}]`, false, false, true},
		{"other_thread", `[{"threadId":"thr_other","status":"pending"}]`, false, false, true},
		{"missing_identity", `[{"status":"pending"}]`, false, false, true},
		{"missing_status", `[{"threadId":"thr_child"}]`, false, false, true},
		{"unknown_status", `[{"threadId":"thr_child","status":"private fixture text"}]`, false, false, true},
		{"null", `null`, false, false, true},
		{"envelope", `{"ok":true}`, false, false, true},
		{"bad_json", `private fixture text`, false, false, true},
		{"command_failed", `[{"threadId":"thr_child","status":"pending"}]`, true, false, true},
		{"partial_known", `[{"threadId":"thr_child","status":"pending"},{"threadId":"thr_child"}]`, false, true, true},
		{"oversized", `[` + strings.TrimSuffix(strings.Repeat(`{"threadId":"thr_child","status":"pending"},`, 501), ",") + `]`, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newFake(`{}`, `[]`, false)
			a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
				if stdin != "" || !reflect.DeepEqual(args, []string{"thread", "interactions", "list", "thr_child", "--json"}) {
					t.Fatal("unexpected query")
				}
				if tc.fail {
					return []byte(tc.body), errors.New("private fixture text")
				}
				return []byte(tc.body), nil
			}
			p, u := a.childPending(context.Background(), "thr_child")
			if p != tc.pending || u != tc.unknown {
				t.Fatalf("pending=%v unknown=%v", p, u)
			}
		})
	}
}

func TestChildInputRequiresCurrentRelationshipAndDeduplicates(t *testing.T) {
	for _, which := range []string{"verified", "reparented", "archived", "deleted", "gone", "wrong_id", "unknown_sibling"} {
		t.Run(which, func(t *testing.T) {
			a, _ := newFake(`{"thread":{"status":"idle"}}`, `[]`, false)
			base := a.run
			queries := 0
			a.run = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
				if args[1] == "list" {
					return []byte(`[{"id":"thr_child"},{"id":"thr_child"},{"id":"thr_other"}]`), nil
				}
				if args[1] == "show" && args[2] != "thr_example123" {
					if args[2] == "thr_other" {
						if which == "unknown_sibling" {
							return []byte(`{}`), errors.New("unavailable")
						}
						return []byte(`{"thread":{"id":"thr_other","parentThreadId":"thr_example123","status":"active"}}`), nil
					}
					s := childShow("active")
					switch which {
					case "reparented":
						s = strings.Replace(s, "thr_example123", "thr_elsewhere", 1)
					case "archived":
						s = strings.Replace(s, `"title":`, `"archivedAt":123,"title":`, 1)
					case "deleted":
						s = strings.Replace(s, `"title":`, `"deletedAt":123,"title":`, 1)
					case "gone":
						return []byte(`{"ok":false,"error":{"code":"thread_not_found"}}`), errors.New("gone")
					case "wrong_id":
						s = strings.Replace(s, "thr_child", "thr_wrong", 1)
					}
					return []byte(s), nil
				}
				if args[1] == "interactions" && args[3] != "thr_example123" {
					if args[3] == "thr_child" {
						queries++
						return []byte(`[{"threadId":"thr_child","status":"pending","text":"private fixture text"}]`), nil
					}
					return []byte(`[]`), nil
				}
				return base(ctx, stdin, args...)
			}
			obs, err := a.Observe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := which == "verified" || which == "unknown_sibling"
			if want {
				if !reflect.DeepEqual(obs.AwaitingChildren, []string{"thr_child"}) || queries != 1 {
					t.Fatalf("%+v queries=%d", obs, queries)
				}
			} else if len(obs.AwaitingChildren) != 0 || queries != 0 {
				t.Fatalf("unverified child accepted: %+v", obs)
			}
		})
	}
}

func TestChildInputSharesDiscoveryDeadline(t *testing.T) {
	a, _ := newFake(`{}`, `[]`, false)
	a.run = func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "list" {
			return []byte(`[{"id":"thr_child"}]`), nil
		}
		if args[1] == "show" {
			return []byte(childShow("idle")), nil
		}
		<-ctx.Done()
		return []byte(fmt.Sprintf(`[{"threadId":%q,"status":"pending"}]`, args[3])), nil
	}
	a.Timeout = time.Millisecond
	if _, u, p, _ := a.childWork(context.Background()); !u || len(p) > 0 {
		t.Fatal("cancelled discovery accepted input")
	}
}

func TestChildInputEscalationDoesNotNeedParentLog(t *testing.T) {
	a, _ := newFake(`{}`, `[]`, false)
	a.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case args[1] == "show" && args[2] == "thr_example123":
			return []byte(`{"thread":{"status":"error"}}`), nil
		case args[1] == "list":
			return []byte(`[{"id":"thr_child"}]`), nil
		case args[1] == "show" && args[2] == "thr_child":
			return []byte(childShow("idle")), nil
		case args[1] == "interactions" && args[3] == "thr_child":
			return []byte(`[{"threadId":"thr_child","status":"pending"}]`), nil
		default:
			t.Fatalf("unnecessary parent query or mutation: %v", args)
			return nil, nil
		}
	}
	obs, err := a.Observe(context.Background())
	if err != nil || obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown || len(obs.AwaitingChildren) != 1 {
		t.Fatalf("%+v %v", obs, err)
	}
}
