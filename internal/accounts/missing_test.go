package accounts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A value that is missing by design says why, in words, and is never shown
// as a failure; a check that did not establish a login leaves it unavailable.
func TestProjectionSaysWhyDataIsMissing(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	obs := func(auth, reason, plan string) *Observation {
		return &Observation{At: now.Add(-time.Hour), Auth: auth, Reason: reason, Plan: plan, Version: "1.0.0"}
	}
	cases := []struct {
		name          string
		provider      string
		last, attempt *Observation
		missing, text string
	}{
		{name: "codex never reports a plan", provider: "codex", last: obs("reported_present", "", ""), attempt: obs("reported_present", "", ""), missing: MissingNotReported, text: "Codex does not report a plan."},
		{name: "codex unchecked still reports no plan", provider: "codex", missing: MissingNotReported, text: "Codex does not report a plan."},
		{name: "codex with no login reports no plan", provider: "codex", last: obs("reported_absent", "", ""), attempt: obs("reported_absent", "", ""), missing: MissingNotReported, text: "Codex does not report a plan."},
		{name: "claude login without a recognized plan", provider: "claude-code", last: obs("reported_present", "", ""), attempt: obs("reported_present", "", ""), missing: MissingNotRecognized, text: "Claude Code's status reported no recognized plan."},
		{name: "claude without a login reads no plan", provider: "claude-code", last: obs("reported_absent", "", ""), attempt: obs("reported_absent", "", ""), missing: MissingNoLogin, text: "No plan is read without a login."},
		{name: "claude never checked", provider: "claude-code", missing: MissingNotChecked, text: "Not checked"},
		{name: "claude reported plan has no reason", provider: "claude-code", last: obs("reported_present", "", "max"), attempt: obs("reported_present", "", "max"), text: "Max"},
		{name: "claude failed check with nothing before is unavailable", provider: "claude-code", attempt: obs("unavailable", "timeout", ""), text: "Unavailable"},
		{name: "claude failed check keeps the earlier plan", provider: "claude-code", last: obs("reported_present", "", "pro"), attempt: obs("unavailable", "timeout", ""), text: "Pro"},
		{name: "unsupported provider is unavailable", provider: "example-tool", text: "Unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := File{Entries: []Entry{{ID: "00000000000000000000000000000001", Label: "Example", Provider: tc.provider, Mode: "inherited", Last: tc.last, Attempt: tc.attempt}}}
			c := Project(f, now).Cards[0]
			if c.Plan.Missing != tc.missing || c.Plan.Text != tc.text {
				t.Errorf("plan missing %q text %q, want %q %q", c.Plan.Missing, c.Plan.Text, tc.missing, tc.text)
			}
			if tc.missing != "" && (c.Plan.ObservedAt != nil || c.Plan.Source != "" || c.Plan.CheckAgain) {
				t.Errorf("a plan missing by design carries an observation: %+v", c.Plan)
			}
			// The catalog records no model; both stay as fields, not recorded.
			for _, m := range []Evidence{c.ConfiguredModel, c.ObservedModel} {
				if m.Missing != MissingNotRecorded || m.Text != "Not recorded" || m.State != "unavailable" {
					t.Errorf("model evidence %+v, want not recorded", m)
				}
			}
			// Missing data never asks for a person.
			if tc.attempt == nil || tc.attempt.Auth == "reported_present" {
				if c.NeedsPerson {
					t.Errorf("needsPerson for %s", tc.name)
				}
			}
		})
	}
}

func TestLinkRoutes(t *testing.T) {
	a, b := "00000000000000000000000000000001", "00000000000000000000000000000002"
	cases := []struct {
		name     string
		named    map[string]bool
		complete bool
		want     [2]string
	}{
		{name: "no route names either", complete: true, want: [2]string{RoutesNone, RoutesNone}},
		{name: "one named", named: map[string]bool{a: true}, complete: true, want: [2]string{RoutesNamed, RoutesNone}},
		{name: "incomplete read never says none", named: map[string]bool{b: true}, want: [2]string{RoutesUnknown, RoutesNamed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Project(File{Entries: []Entry{{ID: a, Provider: "codex"}, {ID: b, Provider: "claude-code"}}}, time.Now())
			LinkRoutes(&v, tc.named, tc.complete)
			if got := [2]string{v.Cards[0].Routes, v.Cards[1].Routes}; got != tc.want {
				t.Errorf("routes %v, want %v", got, tc.want)
			}
		})
	}
	// Without a comparison the field is absent, and the existing fields stay.
	raw, _ := json.Marshal(Project(File{Entries: []Entry{{ID: a, Provider: "codex"}}}, time.Now()))
	s := string(raw)
	if strings.Contains(s, `"routes"`) {
		t.Errorf("routes present without a comparison: %s", s)
	}
	for _, want := range []string{`"configuredModel":{`, `"observedModel":{`, `"missing":"not_recorded"`, `"missing":"not_reported"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}
