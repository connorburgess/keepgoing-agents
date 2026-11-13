package main

import (
	"fmt"
	"strings"
	"time"
)

// Every sentence the panel shows about a loop or a route is assembled here,
// in Go, so the table tests can reach it. The page only renders: literal
// text as written, and each time in the browser's own time zone. Ages and
// countdowns are computed against the report's read time and are text.

// dockPart is one piece of a sentence: literal text, or a time the page shows
// in its own time zone.
type dockPart struct {
	T  string     `json:"t,omitempty"`
	At *time.Time `json:"at,omitempty"`
	// F is how the time is shown: date ("Thu 12 Nov"), datetime ("Wed 18 Nov,
	// 19:12") or clock (a time of day within 18 hours of the read, otherwise
	// date and time). The year is added when the time is more than a year
	// from the read.
	F string `json:"f,omitempty"`
}

// dockText is one sentence or phrase as parts.
type dockText []dockPart

// dockNote is a sentence with how loud it is: limit (boxed) or plain.
type dockNote struct {
	Tone string   `json:"tone"`
	Text dockText `json:"text"`
}

func txt(a ...any) dockText {
	out := dockText{}
	for _, v := range a {
		switch x := v.(type) {
		case string:
			if x != "" {
				out = append(out, dockPart{T: x})
			}
		case dockPart:
			out = append(out, x)
		case dockText:
			out = append(out, x...)
		}
	}
	return out
}

func tm(t time.Time, f string) dockPart {
	u := t.UTC()
	return dockPart{At: &u, F: f}
}

// render writes the sentence with times in UTC, as the tests read it.
func (d dockText) render(readAt time.Time) string {
	var b strings.Builder
	for _, p := range d {
		if p.At == nil {
			b.WriteString(p.T)
			continue
		}
		t := p.At.UTC()
		far := t.Sub(readAt) > 365*24*time.Hour || readAt.Sub(t) > 365*24*time.Hour
		date := "Mon 2 Jan"
		if far {
			date += " 2006"
		}
		switch p.F {
		case "date":
			b.WriteString(t.Format(date))
		case "clock":
			if !far && absDuration(t.Sub(readAt)) < 18*time.Hour {
				b.WriteString(t.Format("15:04"))
			} else {
				b.WriteString(t.Format(date + ", 15:04"))
			}
		default:
			b.WriteString(t.Format(date + ", 15:04"))
		}
	}
	return b.String()
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// dockSpan is a length of time in the page's words: "under a minute",
// "47 min", "3 h 5 min", "32 h", "5 days 2 h".
func dockSpan(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	s := int64(d / time.Second)
	m := int64((d + 30*time.Second) / time.Minute)
	switch {
	case s < 45:
		return "under a minute"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	}
	h, mm := m/60, m%60
	if h < 48 {
		if mm > 0 && h < 6 {
			return fmt.Sprintf("%d h %d min", h, mm)
		}
		return fmt.Sprintf("%d h", h)
	}
	days, hh := h/24, h%24
	if hh > 0 {
		return fmt.Sprintf("%d days %d h", days, hh)
	}
	return fmt.Sprintf("%d days", days)
}

// dockAgo says how long before the read a time was.
func dockAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < -time.Minute:
		return "in the future (check this computer's clock)"
	case d < 45*time.Second:
		return "just now"
	}
	return dockSpan(d) + " ago"
}

// dockUntil is "in 5 days (Wed 18 Nov, 19:12)", or "Wed 18 Nov, 19:12 (passed)".
func dockUntil(now, t time.Time) dockText {
	if !t.After(now) {
		return txt(tm(t, "clock"), " (passed)")
	}
	return txt("in "+dockSpan(t.Sub(now))+" (", tm(t, "clock"), ")")
}

// dockSaneReset reports whether a reported reset time can be taken as one. A
// time more than 400 days from the read is treated as not reported.
func dockSaneReset(now time.Time, t *time.Time) bool {
	return t != nil && !t.IsZero() && absDuration(t.Sub(now)) <= 400*24*time.Hour
}

var dockOrdinals = []string{"first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth"}

// dockOrdinal is "first" for 1, "eleventh" style words up to ten, then "22nd".
func dockOrdinal(n int) string {
	if n >= 1 && n <= len(dockOrdinals) {
		return dockOrdinals[n-1]
	}
	suffix := "th"
	if r := n % 100; r < 11 || r > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

func dockCapital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// dockPlural is "1 loop" or "3 loops".
func dockPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// dockHandoffClasses words the error classes a loop moves on automatically.
func dockHandoffClasses(classes []string) string {
	words := []string{}
	for _, c := range classes {
		switch c {
		case "rate_limit":
			words = append(words, "usage limits")
		case "auth":
			words = append(words, "sign-in failures")
		case "overloaded":
			words = append(words, "provider overload")
		case "network":
			words = append(words, "network failures")
		case "unknown":
			words = append(words, "unclassified errors")
		default:
			words = append(words, clip(strings.ReplaceAll(c, "_", " "), 40))
		}
	}
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}
