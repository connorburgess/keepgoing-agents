package bb

import (
	"context"
	"strings"
	"testing"
)

func TestAssignmentChildrenExactStatesAndInteractions(t *testing.T) {
	for _, status := range []string{"idle", "active", "pending", "starting", "stopping", "error", "unknown"} {
		t.Run(status, func(t *testing.T) {
			a, _ := newFake(`{"thread":{"status":"idle"}}`, `[]`, false)
			a.run = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
				if _, bounded := ctx.Deadline(); !bounded || stdin != "" {
					t.Fatal("unbounded or mutating scan")
				}
				switch strings.Join(args[:2], " ") {
				case "thread list":
					return []byte(`[{"id":"thr_example_child"}]`), nil
				case "thread show":
					return []byte(`{"thread":{"id":"thr_example_child","parentThreadId":"thr_example123","status":"` + status + `"}}`), nil
				case "thread interactions":
					return []byte(`[{"threadId":"thr_example_child","status":"pending"}]`), nil
				default:
					t.Fatal("unexpected scan command", args)
					return nil, nil
				}
			}
			s := a.AssignmentChildren(context.Background())
			if s.Complete != (status != "unknown") || s.States["thr_example_child"].Execution != status || !s.States["thr_example_child"].PendingInteraction {
				t.Fatal(s)
			}
		})
	}
}
