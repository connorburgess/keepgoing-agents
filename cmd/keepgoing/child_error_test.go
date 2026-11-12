//go:build unix

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Records what keepgoing does today when direct children are in error rather
// than active: they do not count as running work, so the parent is handled on
// its own status (an idle parent gets its ordinary wake), and nothing names,
// counts, reports, messages or restarts the errored children. Surfacing or
// recovering them is an open roadmap item; this fixture is not that feature.
func TestErroredChildrenAreNotSurfacedOrRecovered(t *testing.T) {
	children := []string{"thr_child_a", "thr_child_b", "thr_child_c", "thr_child_d", "thr_child_e"}
	for _, tc := range []struct{ parent, action, reason string }{
		{"idle", "wake", "idle"},
		{"active", "none", "active"},
	} {
		t.Run(tc.parent, func(t *testing.T) {
			f := newFixture(t)
			f.run("enable")
			var list []string
			for _, id := range children {
				list = append(list, fmt.Sprintf(`{"id":%q}`, id))
				write(t, filepath.Join(f.bbDir, id+".show"), fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":"thr_primary","status":"error"}}`, id), 0600)
			}
			write(t, filepath.Join(f.bbDir, "thr_primary.children"), "["+strings.Join(list, ",")+"]", 0600)
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), fmt.Sprintf(`{"thread":{"status":%q}}`, tc.parent), 0600)
			r := f.tick()
			if r["action"] != tc.action || r["reason"] != tc.reason {
				t.Fatalf("%v", r)
			}
			_, tickJSON, _ := f.run("tick", "--dry-run")
			_, status, _ := f.run("status")
			_, statusJSON, _ := f.run("status", "--json")
			_, timeline, _ := timelineCLI(t, loadFixture(t, f.cfg).Loop.StateDir, "--json")
			for name, out := range map[string]string{"tick": fmt.Sprint(r) + tickJSON, "status": status + statusJSON, "timeline": timeline, "reports": fmt.Sprint(f.hooks.events), "prompt": f.read("thr_primary.told")} {
				if strings.Contains(out, "thr_child") {
					t.Fatalf("%s names an errored child:\n%s", name, out)
				}
			}
			if !strings.Contains(timeline, `"activeChildren": 0`) || strings.Contains(timeline, `"childrenUnverified": true`) || strings.Contains(timeline, `"activeChildren": 5`) {
				t.Fatalf("errored children were counted or left unverified:\n%s", timeline)
			}
			for _, line := range strings.Split(strings.TrimSpace(f.read("calls")), "\n") {
				for _, id := range children {
					if strings.Contains(line, id) && !strings.HasPrefix(line, "thread show "+id) && !strings.HasPrefix(line, "thread interactions list "+id) {
						t.Fatalf("errored child was acted on: %s", line)
					}
				}
			}
		})
	}
}
