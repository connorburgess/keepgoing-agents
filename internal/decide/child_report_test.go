package decide

import (
	"reflect"
	"testing"
	"time"
)

func TestChildErrorReport(t *testing.T) {
	cases := []struct {
		name                     string
		unreported, reports, max int
		subscribed               bool
		want                     ChildReport
	}{
		{"nothing new", 0, 0, 3, true, ChildReportNone},
		{"nothing new, unsubscribed", 0, 0, 3, false, ChildReportNone},
		{"nothing new after the cap", 0, 3, 3, true, ChildReportNone},
		{"first failure", 5, 0, 3, true, ChildReportSend},
		{"one more child failed", 1, 1, 3, true, ChildReportSend},
		{"last report of the episode", 1, 2, 3, true, ChildReportSend},
		{"at the cap", 1, 3, 3, true, ChildReportCapped},
		{"past the cap", 4, 9, 3, true, ChildReportCapped},
		{"not subscribed", 5, 0, 3, false, ChildReportNotSubscribed},
		{"not subscribed wins over the cap, so subscribing later is not pre-capped", 5, 3, 3, false, ChildReportNotSubscribed},
		{"negative count is nothing new", -1, 0, 3, true, ChildReportNone},
	}
	for _, tc := range cases {
		if got := ChildErrorReport(tc.unreported, tc.reports, tc.max, tc.subscribed); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// The episode's record reports only growth, forgets a child only after it has
// stayed out of error for the floor, and learns nothing about a child that an
// incomplete listing does not show.
func TestCoveredChildren(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const floor = time.Hour
	h := func(ids ...string) []string { return ChildHashes(ids) }
	one := func(id string) string { return ChildHashes([]string{id})[0] }
	inError := func(id string, ago time.Duration) CoveredChild {
		return CoveredChild{Hash: one(id), ErroredAt: now.Add(-ago)}
	}
	recovered := func(id string, ago time.Duration) CoveredChild {
		return CoveredChild{Hash: one(id), ErroredAt: now.Add(-ago - time.Minute), RecoveredAt: now.Add(-ago)}
	}
	cases := []struct {
		name           string
		errored        []string
		healthy        []string
		covered        []CoveredChild
		complete       bool
		wantKept       map[string]string // id -> "error", "recovering" or "unchanged"
		wantUnreported int
	}{
		{"first failure", h("a", "b"), nil, nil, true, map[string]string{}, 2},
		{"same set", h("a", "b"), nil, []CoveredChild{inError("a", time.Minute), inError("b", time.Minute)}, true, map[string]string{"a": "error", "b": "error"}, 0},
		{"one more", h("a", "b", "c"), nil, []CoveredChild{inError("a", time.Minute), inError("b", time.Minute)}, true, map[string]string{"a": "error", "b": "error"}, 1},
		{"just recovered: kept, not forgotten", h("b"), nil, []CoveredChild{inError("a", time.Minute), inError("b", time.Minute)}, true, map[string]string{"a": "recovering", "b": "error"}, 0},
		{"recovered for less than the floor", h(), nil, []CoveredChild{recovered("a", 59*time.Minute)}, true, map[string]string{"a": "unchanged"}, 0},
		{"recovered for exactly the floor: forgotten", h(), nil, []CoveredChild{recovered("a", time.Hour)}, true, map[string]string{}, 0},
		{"fails again inside the floor: the same failure, not reported", h("a"), nil, []CoveredChild{recovered("a", 30*time.Minute)}, true, map[string]string{"a": "error"}, 0},
		{"fails again after being forgotten: reported", h("a"), nil, nil, true, map[string]string{}, 1},
		{"fails again after the floor with no check in between: reported", h("a"), nil, []CoveredChild{recovered("a", time.Hour)}, true, map[string]string{}, 1},
		{"fails again one minute short of the floor: the same failure", h("a"), nil, []CoveredChild{recovered("a", 59*time.Minute)}, true, map[string]string{"a": "error"}, 0},
		{"flapping beside a sibling does not report", h("a", "s"), nil, []CoveredChild{recovered("a", time.Minute), inError("s", time.Hour)}, true, map[string]string{"a": "error", "s": "error"}, 0},
		{"incomplete listing not showing a child learns nothing", h("b"), nil, []CoveredChild{inError("a", 5*time.Hour), inError("b", time.Minute)}, false, map[string]string{"a": "unchanged", "b": "error"}, 0},
		{"incomplete listing showing nothing learns nothing", h(), nil, []CoveredChild{inError("a", 5*time.Hour), recovered("b", 5*time.Hour)}, false, map[string]string{"a": "unchanged", "b": "unchanged"}, 0},
		{"incomplete listing with the child verifiably healthy starts its recovery", h(), h("a"), []CoveredChild{inError("a", time.Minute)}, false, map[string]string{"a": "recovering"}, 0},
		{"incomplete listing with the child healthy past the floor forgets it", h(), h("a"), []CoveredChild{recovered("a", 2*time.Hour)}, false, map[string]string{}, 0},
		{"incomplete listing still reports a new child", h("b", "c"), nil, []CoveredChild{inError("a", time.Minute), inError("b", time.Minute)}, false, map[string]string{"a": "unchanged", "b": "error"}, 1},
		{"incomplete first sight", h("a"), nil, nil, false, map[string]string{}, 1},
		{"a recovery recorded by a later clock is kept, not forgotten", h(), nil, []CoveredChild{{Hash: one("a"), ErroredAt: now, RecoveredAt: now.Add(time.Hour)}}, true, map[string]string{"a": "unchanged"}, 0},
		{"a repeated entry is kept once", h("a"), nil, []CoveredChild{inError("a", time.Minute), inError("a", time.Hour)}, true, map[string]string{"a": "error"}, 0},
	}
	for _, tc := range cases {
		before := map[string]CoveredChild{}
		for _, c := range tc.covered {
			if _, ok := before[c.Hash]; !ok {
				before[c.Hash] = c
			}
		}
		kept, unreported := CoveredChildren(tc.errored, tc.healthy, tc.covered, tc.complete, now, floor)
		if len(kept) != len(tc.wantKept) || len(unreported) != tc.wantUnreported {
			t.Errorf("%s: kept %+v, unreported %v", tc.name, kept, unreported)
			continue
		}
		for id, want := range tc.wantKept {
			var got *CoveredChild
			for i := range kept {
				if kept[i].Hash == one(id) {
					got = &kept[i]
				}
			}
			ok := got != nil
			switch {
			case !ok:
			case want == "error":
				ok = got.ErroredAt.Equal(now) && got.RecoveredAt.IsZero()
			case want == "recovering":
				ok = got.RecoveredAt.Equal(now)
			default:
				ok = *got == before[got.Hash]
			}
			if !ok {
				t.Errorf("%s: %s is %+v, want %s", tc.name, id, got, want)
			}
		}
	}
}

// The record is bounded: past the limit the children seen in error longest ago
// are dropped, and the caller's slices are not modified.
func TestCoverChildrenIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	var kept []CoveredChild
	for i := 0; i < 5; i++ {
		kept = append(kept, CoveredChild{Hash: string(rune('a' + i)), ErroredAt: now.Add(-time.Duration(5-i) * time.Hour)})
	}
	got := CoverChildren(kept, []string{"x", "y"}, now, 4)
	var hashes []string
	for _, c := range got {
		hashes = append(hashes, c.Hash)
	}
	// "a", "b" and "c" were seen in error longest ago.
	if !reflect.DeepEqual(hashes, []string{"d", "e", "x", "y"}) || len(kept) != 5 || kept[0].Hash != "a" {
		t.Fatalf("got %v, kept %v", hashes, kept)
	}
	for _, c := range got {
		if (c.Hash == "x" || c.Hash == "y") && (!c.ErroredAt.Equal(now) || !c.RecoveredAt.IsZero()) {
			t.Fatalf("new entry %+v", c)
		}
	}
	if got := CoverChildren(nil, nil, now, 4); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}
