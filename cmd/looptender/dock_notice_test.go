package main

import (
	"strings"
	"testing"
)

// G4 (design pass 2): a failed read was announced twice, by an alert role
// on the banner and by the polite region; it re-alerted on every failed
// read; it pushed the page down; and it was drawn as a warning while the
// page was still recovering by itself.
func TestDockFailureNoticeAnnouncedOnceG4(t *testing.T) {
	html, css, js := dockUIText(t, "index.html"), dockUIText(t, "dock.css"), dockUIText(t, "dock.js")
	// One live region: the polite status line. The banner has no live role.
	if strings.Count(html, "aria-live=") != 1 || strings.Contains(html, `role="alert"`) || !strings.Contains(html, `<p class="banner" id="banner" hidden></p>`) {
		t.Error("the page must have one polite live region and a banner with no live role")
	}
	// Every notice goes through one function, which announces only when the
	// episode starts or turns into an alarm, not on each failed read.
	if strings.Contains(js, "banner(") || strings.Count(js, "announce(text)") != 2 {
		t.Error("notices must go through notice(), the one place a failure is announced")
	}
	for _, want := range []string{
		`var said = text ? key + (alarm ? "!" : "") : "";`,
		"if (said !== state.notice) {",
		`notice(schema ? "schema" : "failed", text, schema || state.failedLong);`,
		"state.failedLong = Date.now() - state.failedSince >= FAIL_LONG_MS;",
		`r.classList.toggle("is-stale", state.failedLong);`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("dock.js lacks %q", want)
		}
	}
	// Placed over the foot of the window, so it never moves what is read;
	// neutral by default, a warning only as an alarm.
	banner := cssRule(t, css, ".banner")
	for _, want := range []string{"position: fixed", "bottom: 1rem", "background: var(--surface)", "border: 1px solid var(--line-2)"} {
		if !strings.Contains(banner, want) {
			t.Errorf(".banner lacks %q", want)
		}
	}
	if !strings.Contains(cssRule(t, css, ".banner.is-alarm"), "background: var(--warn-soft)") || !strings.Contains(cssRule(t, css, "body.has-banner"), "padding-bottom") {
		t.Error("the warning colour belongs to the alarm only, and the page must make room under the banner")
	}
}
