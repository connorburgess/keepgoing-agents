package main

import (
	"regexp"
	"strings"
	"testing"
)

// mediaBlock returns the body of the first @media block whose query is
// exactly query, up to its closing brace at the start of a line.
func mediaBlock(t *testing.T, css, query string) string {
	t.Helper()
	m := regexp.MustCompile(`(?ms)^@media ` + regexp.QuoteMeta(query) + ` \{\n(.*?)^\}`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("dock.css has no @media %s block", query)
	}
	return m[1]
}

// G3 (hand check against Modern Web Guidance, motion and spinner guides):
// reduced motion switched off every transition and animation at once, so
// "Read now" gave no sign that it was reading. Motion is now opted in under
// no-preference, and reduced motion keeps a slow turn of the busy icon.
// Emulated reduced motion is checked in a headless browser outside the suite.
func TestDockReducedMotionKeepsBusyCueG3(t *testing.T) {
	css := dockUIText(t, "dock.css")
	if strings.Contains(css, "animation: none !important") || strings.Contains(css, "transition: none !important") {
		t.Error("reduced motion must not switch off every animation and transition at once")
	}
	if !strings.Contains(mediaBlock(t, css, "(prefers-reduced-motion: no-preference)"), `.btn[aria-busy="true"] svg { animation: spin 900ms linear infinite; }`) {
		t.Error("the busy spin must be opted in under prefers-reduced-motion: no-preference")
	}
	if !strings.Contains(mediaBlock(t, css, "(prefers-reduced-motion: reduce)"), `.btn[aria-busy="true"] svg { animation: spin 6s linear infinite; }`) {
		t.Error("reduced motion must keep a slow busy cue on a busy button")
	}
	// The busy spin is declared nowhere outside those two blocks.
	if n := strings.Count(css, `.btn[aria-busy="true"] svg {`); n != 2 {
		t.Errorf("busy spin rule appears %d times, want 2 (one per motion preference)", n)
	}
}

// G5 (hand check, accessibility guide on skip links and html §6): after a
// change of view focus went to the whole <main>, which shows no outline.
// It now lands on the view's heading, which the page's own :focus-visible
// ring marks, and an ordinary read moves no focus. Focus after a click and
// after the keyboard is checked in a headless browser outside the suite.
func TestDockViewChangeFocusesHeadingG5(t *testing.T) {
	js, css := dockUIText(t, "dock.js"), dockUIText(t, "dock.css")
	if !strings.Contains(js, "h.id = \"pane-title\";\n    // Focus lands here after a change of view or the skip link.\n    h.tabIndex = -1;") {
		t.Error("the view's heading must take programmatic focus (tabindex -1) and stay out of the tab order")
	}
	if !strings.Contains(js, `($("pane-title") || $("pane")).focus(`) {
		t.Error("focus must go to the view's heading, and to the pane only when there is none")
	}
	// Called from the change of view and the skip link only; a read that is
	// not a change of view must not take the keyboard away.
	if n := strings.Count(js, "focusPaneTitle("); n != 3 {
		t.Errorf("focusPaneTitle appears %d times, want its definition and two calls", n)
	}
	nav := strings.Index(js, "    if (navigated) {")
	end := strings.Index(js[nav:], "} else if (key) {")
	if nav < 0 || end < 0 || !strings.Contains(js[nav:nav+end], "else focusPaneTitle();") {
		t.Error("a change of view must focus the view's heading")
	}
	if strings.Contains(js, `$("pane").focus(`) || strings.Contains(js, "pane.focus(") {
		t.Error("nothing may focus the whole pane directly")
	}
	// The heading shows the page's focus ring; nothing turns it off.
	if !strings.Contains(css, ":focus-visible { outline: 2px solid var(--focus);") {
		t.Error("the page's :focus-visible ring is missing")
	}
	if regexp.MustCompile(`(?m)(h1|#pane-title|\.pane-head[^{]*)\s*:focus[^{]*\{[^}]*outline:\s*none`).MatchString(css) {
		t.Error("the focused heading must keep its outline")
	}
}
