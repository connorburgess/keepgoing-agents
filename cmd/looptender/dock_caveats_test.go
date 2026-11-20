package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// G8 (design pass 2): a picker name cut with an ellipsis had its full text
// only in a title attribute, which keyboard and touch users cannot reach. The
// single-line-overflow guide keeps the full text in the DOM, reveals it on
// hover and keyboard focus, and relies on the opened page's heading for touch.
func TestPickerNamesAreReachableWithoutHoverG8(t *testing.T) {
	js, css := dockUIText(t, "dock.js"), dockUIText(t, "dock.css")
	start := strings.Index(js, "function pickRow(")
	end := strings.Index(js, "function renderPicker(")
	if start < 0 || end < start {
		t.Fatal("pickRow not found")
	}
	row := js[start:end]
	if strings.Contains(row, ".title =") || strings.Contains(row, `setAttribute("title"`) {
		t.Error("a picker row must not keep its full text only in a title attribute")
	}
	if !strings.Contains(row, `n.appendChild(el("span", null, name))`) || !strings.Contains(row, `s.appendChild(el("span", null, sub))`) {
		t.Error("the full name and subtitle must stay in the row as text")
	}
	for _, want := range []string{
		".pick:is(:hover, :focus-visible) :is(.pick-name, .pick-state)",
		"@container style(--truncated: 1)",
		"animation-timeline: scroll(self inline)",
		"@media (prefers-reduced-motion: no-preference)",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("dock.css lacks %q", want)
		}
	}
	// The ellipsis stays as the fallback, and the heading of the opened
	// page wraps instead of cutting the name.
	if r := cssRule(t, css, ".pick-name"); !strings.Contains(r, "text-overflow: ellipsis") {
		t.Errorf(".pick-name: %q", r)
	}
	if r := cssRule(t, css, "h1"); !strings.Contains(r, "overflow-wrap: anywhere") || strings.Contains(r, "nowrap") {
		t.Errorf("h1: %q", r)
	}
}

// F12: the Versions line is short enough for the narrowest sidebar (about 35
// characters at 320 px) in every state, so it is never cut.
func TestVersionsPickerLineFitsWithoutCutting(t *testing.T) {
	for _, v := range []dockVersions{
		{Behind: 12},
		{different: 12},
		{uncompared: 12},
		{Behind: 12, different: 12, uncompared: 12},
		{},
	} {
		for _, kind := range []string{"newest_release", "this_panel"} {
			got := dockVersionsSummary(v, "v0.10.12", kind)
			if n := utf8.RuneCountInString(got); n > 28 {
				t.Errorf("%+v %s: %q is %d characters", v, kind, got, n)
			}
		}
	}
}

// F10: the route brief that says a missing standby leaves the route in use
// alone is drawn as a plain notice. The page matches the brief's ending.
func TestNoStandbyBriefIsAPlainNoticeF10(t *testing.T) {
	js, css := dockUIText(t, "dock.js"), dockUIText(t, "dock.css")
	if !strings.Contains(js, `var CURRENT_UNAFFECTED = "`+dockCurrentUnaffected+`";`) {
		t.Error("dock.js and dock_view.go disagree on the ending of the brief")
	}
	if r := cssRule(t, css, ".chain-ready.is-calm"); strings.Contains(r, "--warn") || !strings.Contains(r, "--ink-2") {
		t.Errorf(".chain-ready.is-calm: %q", r)
	}
	if !strings.Contains(js, `c.brief.indexOf(CURRENT_UNAFFECTED) >= 0 ? " is-calm" : c.short ? " is-short"`) {
		t.Error("the calm class must win over the warning class")
	}
}

// N4a: the caveats the page keeps are each said once, in plain words. The
// honesty statements survive, worded as what is true.
func TestOverviewSaysEachCaveatOnce(t *testing.T) {
	js := dockUIText(t, "dock.js")
	for _, gone := range []string{
		"Active does not prove progress", "do not establish completion", "This is a bounded sample",
		"cannot reconstruct it", "keepgoing timeline", "Served to this computer",
	} {
		if strings.Contains(js, gone) {
			t.Errorf("dock.js still says %q", gone)
		}
	}
	html := dockUIText(t, "index.html")
	foot := html[strings.Index(html, "<footer"):strings.Index(html, "<details")]
	if strings.Contains(html, "keepgoing") || strings.Count(foot, "<p") != 2 {
		t.Errorf("the footer is not two lines, or names the old product: %s", foot)
	}
	// The first loop is the first thing in the card: nothing is appended
	// between the heading and the list.
	i := strings.Index(js, `var p = panel("Loops");`)
	if i < 0 || !strings.HasPrefix(strings.TrimSpace(js[i+len(`var p = panel("Loops");`):]), `var ul = el("ul", "work-tree");`) {
		t.Error("something sits above the first loop in Loops")
	}
	if !strings.Contains(js, `(l.lastCheck && l.lastCheck.text) || "Last check unknown"`) {
		t.Error("each loop must show its last-check age or explicitly say it is unknown")
	}
	// The overview says once that it is not live.
	r := dockReport{Loops: []dockLoop{{State: "supervising"}}}
	if got := dockLede(r); !strings.Contains(got, "Not live") || strings.Contains(got, "nothing here is a live view") {
		t.Errorf("lede: %q", got)
	}
}
