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

