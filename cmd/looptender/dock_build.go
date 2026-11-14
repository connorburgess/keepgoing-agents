package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Which keepgoing build serves the panel, and which build ran each loop's
// last check, from the label each check records. Two labels are the same
// build only when they are equal and say which build they are: "dev", the
// label of a build made without one, says nothing, so two "dev" builds may
// differ, and nor does a "-dirty" label shared by two builds. Two dev builds
// of one release (v0.1.5-dev.26f91db and
// v0.1.5-dev.ab565b1) are different builds. Which one is newer is said only
// when both are plain releases.

// dockBuild is what the records say about the build that ran a loop's last
// check, against this panel's own.
type dockBuild struct {
	// Availability is same, different, unknown (a label says nothing about
	// which build it is) or not_recorded.
	Availability string `json:"availability"`
	Recorded     string `json:"recorded,omitempty"` // the last check's build label
	Panel        string `json:"panel"`              // this panel's build label
	// Order is older or newer when both are plain releases and differ: the
	// last check's build against this panel's.
	Order string `json:"order,omitempty"`
	Text  string `json:"text"`
}

const dockMaxBuild = 64

var dockRelease = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// dockBuildKnown says whether a label identifies a build.
func dockBuildKnown(label string) bool { return label != "" && label != "dev" }

// dockReleaseOrder compares two plain release labels: -1, 0 or 1, and false
// when either is not a plain release.
func dockReleaseOrder(a, b string) (int, bool) {
	x, y := dockRelease.FindStringSubmatch(a), dockRelease.FindStringSubmatch(b)
	if x == nil || y == nil {
		return 0, false
	}
	for i := 1; i <= 3; i++ {
		p, err1 := strconv.Atoi(x[i])
		q, err2 := strconv.Atoi(y[i])
		if err1 != nil || err2 != nil {
			return 0, false
		}
		if p != q {
			if p < q {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// dockBuildLabel makes a recorded label safe to show.
func dockBuildLabel(s string) string {
	s = strings.TrimSpace(s)
	if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "unreadable"
	}
	return clip(s, dockMaxBuild)
}

// dockBuildOf compares the build that ran a loop's last check with the
// panel's. recorded is the label from the loop's last check (state), or else
// from the newest retained check record; "" when none is recorded.
func dockBuildOf(recorded, panel string) dockBuild {
	recorded, panel = dockBuildLabel(recorded), dockBuildLabel(panel)
	b := dockBuild{Recorded: recorded, Panel: panel}
	switch {
	case recorded == "":
		b.Availability, b.Text = "not_recorded", "Which keepgoing build ran its last check is not recorded."
	case !dockBuildKnown(recorded) && !dockBuildKnown(panel):
		b.Availability, b.Text = "unknown", "Its last check and this panel both ran a keepgoing build with no version recorded (dev), so whether they are the same build is unknown."
	case !dockBuildKnown(recorded):
		b.Availability, b.Text = "unknown", "Its last check ran a keepgoing build with no version recorded (dev); this panel is keepgoing "+panel+". Whether they are the same build is unknown."
	case !dockBuildKnown(panel):
		b.Availability, b.Text = "unknown", "Its last check ran keepgoing "+recorded+"; this panel is a build with no version recorded (dev). Whether they are the same build is unknown."
	case recorded == panel && strings.HasSuffix(panel, "-dirty"):
		// A dirty label names a commit plus changes that were never committed;
		// two builds can share it and differ.
		b.Availability, b.Text = "unknown", "Its last check and this panel both ran keepgoing "+panel+", built with uncommitted changes, so whether they are the same build is unknown."
	case recorded == panel:
		b.Availability, b.Text = "same", "Its last check ran this panel's build, keepgoing "+panel+"."
	default:
		b.Availability = "different"
		if order, ok := dockReleaseOrder(recorded, panel); ok && order != 0 {
			b.Order = map[int]string{-1: "older", 1: "newer"}[order]
			b.Text = "Its last check ran keepgoing " + recorded + ", an " + b.Order + " release than this panel's " + panel + "."
			if b.Order == "newer" {
				b.Text = "Its last check ran keepgoing " + recorded + ", a newer release than this panel's " + panel + "."
			}
		} else {
			b.Text = "Its last check ran keepgoing " + recorded + "; this panel is keepgoing " + panel + ". They are different builds."
		}
	}
	return b
}

// dockLastBuild is the build label of the loop's last check: what state
// recorded with it, or, for a check recorded without one, the newest retained
// check record's. "" when neither is recorded.
func dockLastBuild(st *statusOut, tl *timelineReport) string {
	if st != nil && st.State.LastCheck != nil && st.State.LastCheck.Version != "" {
		return st.State.LastCheck.Version
	}
	if tl != nil {
		for i := len(tl.Events) - 1; i >= 0; i-- {
			// The history stores a label it could not keep as "unavailable".
			if r := tl.Events[i].Incident; r != nil && r.Kind == "tick" && r.Build != "" && r.Build != "unavailable" {
				return r.Build
			}
		}
	}
	return ""
}

// dockPanelVersionText is the footer's sentence about the panel's own build.
func dockPanelVersionText(panel string) string {
	panel = dockBuildLabel(panel)
	if !dockBuildKnown(panel) {
		return "This page is served by a keepgoing build with no version recorded (dev)."
	}
	return "This page is served by keepgoing " + panel + "."
}
