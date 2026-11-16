package accounts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Age is information, not something for a person to act on: a card needs a
// person only for no login, a failed check or a changed context, and an
// observation older than ConfirmAfter only carries a hint to check again.
func TestProjectionAttentionIsNotAge(t *testing.T) {
	dir, e := fixture(t)
	base, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	obs := func(age time.Duration, auth, reason, plan string) *Observation {
		return &Observation{Revision: e.Revision, Context: e.Context, At: now.Add(-age), Auth: auth, Reason: reason, Plan: plan, Caller: "thr_example_owner"}
	}
	cases := []struct {
		name          string
		last, attempt *Observation
		needs         bool
		attention     string
		authAge       int64 // -1 when the login evidence has no age
		authAgain     bool
		planAgain     bool
		failedAt      bool // the card carries failedCheckAt
	}{
		{name: "never checked", needs: false, authAge: -1},
		{name: "login reported a minute ago", last: obs(time.Minute, "reported_present", "", "max"), attempt: obs(time.Minute, "reported_present", "", "max"), authAge: 60},
		{name: "login reported 3 hours ago is not a warning", last: obs(3*time.Hour, "reported_present", "", "max"), attempt: obs(3*time.Hour, "reported_present", "", "max"), authAge: 3 * 3600},
		{name: "at exactly 24 hours no hint yet", last: obs(ConfirmAfter, "reported_present", "", "max"), attempt: obs(ConfirmAfter, "reported_present", "", "max"), authAge: 24 * 3600},
		{name: "login and plan older than 24 hours ask to confirm", last: obs(30*time.Hour, "reported_present", "", "pro"), attempt: obs(30*time.Hour, "reported_present", "", "pro"), authAge: 30 * 3600, authAgain: true, planAgain: true},
		{name: "no login reported", last: obs(time.Minute, "reported_absent", "", ""), attempt: obs(time.Minute, "reported_absent", "", ""), needs: true, attention: "no_login", authAge: 60},
		{name: "old no login still needs a person", last: obs(50*time.Hour, "reported_absent", "", ""), attempt: obs(50*time.Hour, "reported_absent", "", ""), needs: true, attention: "no_login", authAge: 50 * 3600, authAgain: true},
		{name: "last check failed", last: obs(2*time.Hour, "reported_present", "", "max"), attempt: obs(time.Minute, "unavailable", "timeout", ""), needs: true, attention: "check_failed", authAge: 2 * 3600, failedAt: true},
		{name: "last check cancelled", attempt: obs(time.Minute, "unavailable", "cancelled", ""), needs: true, attention: "check_failed", authAge: 60},
		{name: "failed check over an old login hints on the old plan", last: obs(40*time.Hour, "reported_present", "", "max"), attempt: obs(time.Minute, "unavailable", "status_unavailable", ""), needs: true, attention: "check_failed", authAge: 40 * 3600, authAgain: true, planAgain: true, failedAt: true},
		{name: "context changed", last: obs(2*time.Hour, "reported_present", "", "max"), attempt: obs(time.Minute, "unavailable", "context_changed", ""), needs: true, attention: "context_changed", authAge: 2 * 3600, failedAt: true},
		{name: "externally managed is not a person's to fix here", attempt: obs(time.Minute, "unavailable", "unsupported", ""), authAge: 60},
		{name: "observation dated in the future has no age", last: obs(-time.Hour, "reported_present", "", "max"), attempt: obs(-time.Hour, "reported_present", "", "max"), authAge: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			f.Entries = append([]Entry(nil), base.Entries...)
			f.Entries[0].Last, f.Entries[0].Attempt = tc.last, tc.attempt
			c := Project(f, now).Cards[0]
			if c.NeedsPerson != tc.needs || c.Attention != tc.attention {
				t.Errorf("needsPerson %v attention %q, want %v %q", c.NeedsPerson, c.Attention, tc.needs, tc.attention)
			}
			if tc.authAge < 0 {
				if c.Auth.AgeSeconds != nil {
					t.Errorf("login age %d, want none", *c.Auth.AgeSeconds)
				}
			} else if c.Auth.AgeSeconds == nil || *c.Auth.AgeSeconds != tc.authAge {
				t.Errorf("login age %v, want %d", c.Auth.AgeSeconds, tc.authAge)
			}
			if c.Auth.CheckAgain != tc.authAgain || c.Plan.CheckAgain != tc.planAgain {
				t.Errorf("check again: login %v plan %v, want %v %v", c.Auth.CheckAgain, c.Plan.CheckAgain, tc.authAgain, tc.planAgain)
			}
			if (c.FailedCheckAt != nil) != tc.failedAt {
				t.Errorf("failedCheckAt %v, want %v", c.FailedCheckAt, tc.failedAt)
			}
			// The existing field keeps its meaning for consumers.
			if c.Auth.ObservedAt != nil && tc.attempt != nil && tc.attempt.Auth != "unavailable" {
				want := "fresh"
				if now.Sub(tc.attempt.At) >= 5*time.Minute || tc.attempt.At.After(now) {
					want = "stale"
				}
				if c.Auth.Freshness != want {
					t.Errorf("freshness %q, want %q", c.Auth.Freshness, want)
				}
			}
		})
	}

	// The fields are additive: freshness stays, needsPerson is always sent,
	// and the hint and attention are absent when they do not apply.
	f := base
	f.Entries = append([]Entry(nil), base.Entries...)
	f.Entries[0].Last, f.Entries[0].Attempt = obs(time.Minute, "reported_present", "", "max"), obs(time.Minute, "reported_present", "", "max")
	raw, err := json.Marshal(Project(f, now))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"freshness":"fresh"`, `"needsPerson":false`, `"ageSeconds":60`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	for _, absent := range []string{`"checkAgain"`, `"attention"`} {
		if strings.Contains(s, absent) {
			t.Errorf("%s present when it does not apply", absent)
		}
	}
}
