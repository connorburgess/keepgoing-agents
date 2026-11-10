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

func childShow(status string) string {
	return fmt.Sprintf(`{"thread":{"id":"thr_child","parentThreadId":"thr_example123","status":%q,"title":"private fixture text"}}`, status)
}

func TestChildWorkVerifiesIdentityAndRelationship(t *testing.T) {
	for _, tc := range []struct {
		name, list, show string
		fail             bool
		active           int
		unknown          bool
	}{
		{"active", `[{"id":"thr_child"}]`, childShow("active"), false, 1, false},
		{"finished", `[{"id":"thr_child"}]`, childShow("idle"), false, 0, false},
		{"failed", `[{"id":"thr_child"}]`, childShow("error"), false, 0, false},
		{"starting", `[{"id":"thr_child"}]`, childShow("starting"), false, 0, false},
		{"no_children", `[]`, "", false, 0, false},
		{"duplicate", `[{"id":"thr_child"},{"id":"thr_child"}]`, childShow("active"), false, 1, false},
		{"reparented", `[{"id":"thr_child"}]`, strings.Replace(childShow("active"), "thr_example123", "thr_other", 1), false, 0, false},
		{"no_parent", `[{"id":"thr_child"}]`, strings.Replace(childShow("active"), `"thr_example123"`, `null`, 1), false, 0, false},
		{"missing_parent_field", `[{"id":"thr_child"}]`, `{"thread":{"id":"thr_child","status":"active"}}`, false, 0, true},
		{"mismatched_id", `[{"id":"thr_child"}]`, strings.Replace(childShow("active"), "thr_child", "thr_other", 1), false, 0, true},
		{"gone", `[{"id":"thr_child"}]`, `{"ok":false,"error":{"code":"thread_not_found","message":"private fixture text"}}`, true, 0, false},
		{"command_failure", `[{"id":"thr_child"}]`, childShow("idle"), true, 0, true},
		{"error_envelope", `[{"id":"thr_child"}]`, `{"ok":false,"error":{"code":"unavailable"}}`, false, 0, true},
		{"invalid_show", `[{"id":"thr_child"}]`, `private fixture text`, false, 0, true},
		{"unknown_status", `[{"id":"thr_child"}]`, childShow("private fixture text"), false, 0, true},
		{"null_list", `null`, "", false, 0, true},
		{"envelope_list", `{"ok":true}`, "", false, 0, true},
		{"missing_id", `[{}]`, "", false, 0, true},
		{"unsafe_id", `[{"id":"--self"}]`, "", false, 0, true},
		{"parent_as_child", `[{"id":"thr_example123"}]`, "", false, 0, true},
		{"archived", `[{"id":"thr_child"}]`, strings.Replace(childShow("active"), `"title":`, `"archivedAt":123,"title":`, 1), false, 0, false},
		{"deleted", `[{"id":"thr_child"}]`, strings.Replace(childShow("active"), `"title":`, `"deletedAt":123,"title":`, 1), false, 0, false},
		{"invalid_archive", `[{"id":"thr_child"}]`, strings.Replace(childShow("active"), `"title":`, `"archivedAt":false,"title":`, 1), false, 0, true},
		{"invalid_delete", `[{"id":"thr_child"}]`, strings.Replace(childShow("active"), `"title":`, `"deletedAt":0,"title":`, 1), false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newFake(`{"thread":{"status":"idle"}}`, `[]`, false)
			shows := 0
			a.run = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
				if _, ok := ctx.Deadline(); !ok || stdin != "" {
					t.Fatal("unbounded or mutating child query")
				}
				if reflect.DeepEqual(args, []string{"thread", "list", "--parent-thread", "thr_example123", "--include-hidden", "--json"}) {
					return []byte(tc.list), nil
				}
				if !reflect.DeepEqual(args, []string{"thread", "show", "thr_child", "--json"}) {
					t.Fatalf("unexpected command: %v", args)
				}
				shows++
				if tc.fail {
					return []byte(tc.show), errors.New("private fixture text")
				}
				return []byte(tc.show), nil
			}
			active, unknown := a.childWork(context.Background())
			if active != tc.active || unknown != tc.unknown || shows > 1 {
				t.Fatalf("active=%d unknown=%v shows=%d", active, unknown, shows)
			}
		})
	}
}

func TestChildDiscoveryFailureAndBounds(t *testing.T) {
	for _, list := range []string{`[]`, `[` + strings.TrimSuffix(strings.Repeat(`{"id":"thr_child"},`, 201), ",") + `]`} {
		a, _ := newFake(`{}`, `[]`, false)
		a.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[1] != "list" {
				t.Fatal("oversized inventory queried children")
			}
			if list == `[]` {
				return []byte(list), errors.New("unsupported command")
			}
			return []byte(list), nil
		}
		if n, u := a.childWork(context.Background()); n != 0 || !u {
			t.Fatal("incomplete inventory permitted a wake")
		}
	}
	a, _ := newFake(`{}`, `[]`, false)
	a.Timeout = time.Millisecond
	a.run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return []byte(`[]`), nil
	}
	if n, u := a.childWork(context.Background()); n != 0 || !u {
		t.Fatal("expired query permitted a wake")
	}
}

func TestObserveActiveChildSuppressesParentUntilFinished(t *testing.T) {
	for _, parentStatus := range []string{"idle", "active", "error"} {
		t.Run(parentStatus, func(t *testing.T) {
			a, _ := newFake(fmt.Sprintf(`{"thread":{"status":%q}}`, parentStatus), `[]`, false)
			baseRun := a.run
			childStatus := "active"
			a.run = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
				if args[1] == "list" {
					return []byte(`[{"id":"thr_child","visibility":"hidden"}]`), nil
				}
				if args[1] == "show" && args[2] == "thr_child" {
					return []byte(childShow(childStatus)), nil
				}
				return baseRun(ctx, stdin, args...)
			}
			in := decide.Input{Control: decide.Control{Enabled: true}, InWindow: true, StallAfter: time.Minute, MinWakeInterval: time.Minute, MaxNudges: 2, MaxReprompts: 2}
			for i := 0; i < 6; i++ {
				obs, err := a.Observe(context.Background())
				if err != nil || obs.ActiveChildren != 1 || obs.ChildrenUnverified || string(obs.State) != parentStatus {
					t.Fatalf("%+v %v", obs, err)
				}
				in.Obs, in.UnchangedFor, in.SinceWake = obs, time.Duration(i+1)*time.Hour, time.Duration(i+1)*time.Hour
				if d := decide.Decide(in); d.Action != decide.None || d.Reason != "child work running" {
					t.Fatalf("%+v", d)
				}
			}
			childStatus = "idle"
			in.Obs, _ = a.Observe(context.Background())
			want := map[string]decide.Action{"idle": decide.Wake, "active": decide.Nudge, "error": decide.Reprompt}[parentStatus]
			if d := decide.Decide(in); d.Action != want {
				t.Fatalf("child finished: %+v", d)
			}
		})
	}
}
