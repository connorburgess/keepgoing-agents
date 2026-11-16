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
