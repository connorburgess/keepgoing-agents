package main

import (
	"regexp"
	"strings"
	"testing"
)

func dockUIText(t *testing.T, name string) string {
	t.Helper()
	b, err := dockUI.ReadFile("dockui/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// cssRule returns the declarations of the first rule whose selector is
// exactly sel, so a test can check one rule without matching another.
func cssRule(t *testing.T, css, sel string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(sel) + ` \{([^}]*)\}`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("dock.css has no rule %q", sel)
	}
	return m[1]
}

// H1 (audit F11): the header's button labels wrapped onto two lines at
// about 1000 px, and a longer status line moved the buttons. The width
// sweep that measures this runs in a headless browser outside the suite;
// these assertions keep the rules it depends on.
func TestDockHeaderButtonLabelsStayOnOneLineH1(t *testing.T) {
	css, html := dockUIText(t, "dock.css"), dockUIText(t, "index.html")
	for sel, wants := range map[string][]string{
		// A label never wraps, and every button is one height.
		".tools .btn": {"white-space: nowrap", "min-height: 2.25rem"},
		// The group never shrinks; the status line gives way instead.
		".tools":  {"flex: none"},
		".status": {"flex: 1 1 0", "min-width: 0", "min-height: 2.25rem"},
		// The status line keeps a fixed two-line box, so a longer line does
		// not change the bar's height or push the page down.
		".readline": {"max-height: 2.25rem", "line-height: 1.125rem", "-webkit-line-clamp: 2"},
		// Comfort target: 36 px to look at, 44 px to hit.
		".tools .btn::before": {"inset: -0.25rem 0"},
	} {
		rule := cssRule(t, css, sel)
		for _, want := range wants {
			if !strings.Contains(rule, want) {
				t.Errorf("%s lacks %q", sel, want)
			}
		}
	}
	// The order of giving way: the status line takes its own row first, the
	// labels go only at phone width, with the existing phone treatment.
	stack, phone := strings.Index(css, "@media (max-width: 60rem)"), strings.Index(css, "@media (max-width: 34rem)")
	if stack < 0 || phone < 0 || !strings.Contains(css[stack:], ".status { order: 3; flex-basis: 100%;") || !strings.Contains(css[phone:], ".tools .btn span { position: absolute;") {
		t.Error("the status line must take its own row before the button labels are hidden")
	}
	// Toggles show on by shape as well as colour, and in forced colours.
	if !strings.Contains(cssRule(t, css, `.tools .btn[aria-pressed="true"]`), "box-shadow: inset 0 -2px 0") ||
		!strings.Contains(css, `.tools .btn[aria-pressed="true"] { background: Highlight;`) {
		t.Error("a pressed header toggle must be shown by more than colour, and in forced colours")
	}
	// The short visible label keeps the full name for assistive technology,
	// and the name contains what is visible.
	if !strings.Contains(html, `id="identifiers" aria-pressed="false" aria-label="Show identifiers, lane titles and notes" title="Show identifiers, lane titles and notes">`) ||
		!strings.Contains(html, `<span>Identifiers</span></button>`) {
		t.Error("the identifiers toggle must show a short label and keep its full name")
	}
}
