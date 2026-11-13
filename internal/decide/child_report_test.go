package decide

import (
	"reflect"
	"testing"
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

// The report record follows the same growth-only rule as the parent notice,
// except that an incomplete listing never forgets a child.
func TestChildReportRecord(t *testing.T) {
	h := func(ids ...string) []string { return ChildHashes(ids) }
	cases := []struct {
		name           string
		errored        []string
		reported       []string
		complete       bool
		kept           []string
		wantUnreported int
	}{
		{"first failure", h("a", "b"), h(), true, h(), 2},
		{"same set", h("a", "b"), h("a", "b"), true, h("a", "b"), 0},
		{"shrank", h("b"), h("a", "b"), true, h("b"), 0},
		{"all recovered", h(), h("a", "b"), true, h(), 0},
		{"one more", h("a", "b", "c"), h("a", "b"), true, h("a", "b"), 1},
		{"recovered and failed again after being forgotten", h("a"), h(), true, h(), 1},
		{"incomplete listing missing a reported child keeps it", h("b"), h("a", "b"), false, h("a", "b"), 0},
		{"incomplete listing showing nothing keeps everything", h(), h("a", "b"), false, h("a", "b"), 0},
		{"incomplete listing still reports a new child", h("b", "c"), h("a", "b"), false, h("a", "b"), 1},
		{"incomplete first sight", h("a"), h(), false, h(), 1},
	}
	for _, tc := range cases {
		kept, unreported := ChildReportRecord(tc.errored, tc.reported, tc.complete)
		if !reflect.DeepEqual(kept, tc.kept) || len(unreported) != tc.wantUnreported {
			t.Errorf("%s: kept %v, unreported %v", tc.name, kept, unreported)
		}
		// With a complete listing it is exactly the parent-notice rule.
		if tc.complete {
			k, u := ChildAnnouncements(tc.errored, tc.reported)
			if !reflect.DeepEqual(k, kept) || !reflect.DeepEqual(u, unreported) {
				t.Errorf("%s: differs from ChildAnnouncements", tc.name)
			}
		}
	}
	// The caller's record is never modified in place.
	reported := h("a", "b")
	kept, _ := ChildReportRecord(h("b"), reported, false)
	kept[0] = "changed"
	if reported[0] == "changed" {
		t.Error("record aliased")
	}
}
