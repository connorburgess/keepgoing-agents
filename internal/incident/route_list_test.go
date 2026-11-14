package incident

import (
	"testing"
	"time"
)

// A check that read a changed standard list is never folded into a run of
// identical checks, so the note survives the run.
func TestRouteListChangedCheckIsNeverFolded(t *testing.T) {
	dir := t.TempDir()
	for i := range 6 {
		r := retentionTick(i)
		r.RouteListChanged = i == 3
		mustAppend(t, dir, r)
	}
	j := loadAccounted(t, dir)
	noted := 0
	for _, r := range j.Records {
		if r.RouteListChanged {
			noted++
			if r.Collapsed != nil || r.Tier != TierActivity {
				t.Fatalf("noted check folded or routine: %+v", r)
			}
		}
	}
	if noted != 1 || j.Collapsed == 0 {
		t.Fatalf("noted %d, collapsed %d: %+v", noted, j.Collapsed, j.Records)
	}
}

// List edits have their own cap inside the key tier: a run of them never
// pushes out the loop's moves.
func TestRouteChangesAreCappedWithinTheKeyTier(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		mustAppend(t, dir, retargetAt(i))
	}
	for i := range RouteChangeCap + 9 {
		mustAppend(t, dir, Record{Kind: "route_change", Build: "v0.2.0", CheckStartedAt: retentionStart.Add(time.Hour + time.Duration(i)*time.Minute),
			RouteChange: &RouteChange{Operation: "move", TargetKey: TargetKey("fictional-route"), Caller: "terminal"}})
	}
	j := loadAccounted(t, dir)
	changes, moves := 0, 0
	for _, r := range j.Records {
		switch r.Kind {
		case "route_change":
			changes++
			if r.Tier != TierKey || r.Outcome != "list_saved" || r.RouteChange.Caller != "terminal" {
				t.Fatalf("record: %+v", r)
			}
		case "retarget":
			moves++
		}
	}
	if changes != RouteChangeCap || moves != 10 || j.Pruned.Key.Count != 9 {
		t.Fatalf("changes %d moves %d pruned %+v", changes, moves, j.Pruned)
	}
	// The newest edits are the ones kept.
	if last := j.Records[len(j.Records)-1]; !last.CheckStartedAt.Equal(retentionStart.Add(time.Hour + time.Duration(RouteChangeCap+8)*time.Minute)) {
		t.Fatalf("newest edit lost: %+v", last)
	}
}
