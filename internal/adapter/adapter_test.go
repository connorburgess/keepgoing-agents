package adapter

import (
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestClassifyCode(t *testing.T) {
	cases := map[string]decide.ErrorClass{
		"429": decide.ErrRateLimit, "rate_limit": decide.ErrRateLimit, "usage_limit": decide.ErrRateLimit,
		"401": decide.ErrAuth, "authentication_error": decide.ErrAuth, "oauth_token_expired": decide.ErrAuth,
		// Forbidden is a provider's decision about the account or the work,
		// not an expired login and not a limit.
		"403": decide.ErrRestricted, "permission_denied": decide.ErrRestricted, "permission_error": decide.ErrRestricted,
		"forbidden": decide.ErrRestricted, "restricted": decide.ErrRestricted,
		"oauth_org_not_allowed": decide.ErrRestricted,
		// The model or the provider declining the turn itself.
		"refusal": decide.ErrRefused, "refused": decide.ErrRefused, "policy": decide.ErrRefused,
		"cyberPolicy": decide.ErrRefused, "misalignmentPolicyViolation": decide.ErrRefused,
		// Prose about a refusal must not classify.
		"I can't help with that": decide.ErrUnknown, "policy violation": decide.ErrUnknown, "refused by policy": decide.ErrUnknown,
		"529": decide.ErrOverloaded, "overloaded_error": decide.ErrOverloaded,
		"ECONNRESET": decide.ErrNetwork,
		"":           decide.ErrUnknown, "500": decide.ErrUnknown, "boom": decide.ErrUnknown,
		// Prose must never classify, even when it contains the words.
		"usage limit reached": decide.ErrUnknown, "Merged PR about rate limits": decide.ErrUnknown,
	}
	for in, want := range cases {
		if got := ClassifyCode(in); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}

// A refusal or restriction in any field outranks every other class, in any
// order; otherwise the first known field decides, as before.
func TestClassifyPrefersARefusalOrRestrictionInAnyField(t *testing.T) {
	for _, tc := range []struct {
		codes []string
		want  decide.ErrorClass
	}{
		// BB's Claude Code provider reports 401 and 403 with the same category.
		{[]string{"unauthorized", "403", "unknown"}, decide.ErrRestricted},
		{[]string{"unauthorized", "401", "authentication_failed"}, decide.ErrAuth},
		{[]string{"unauthorized", "", "oauth_org_not_allowed"}, decide.ErrRestricted},
		{[]string{"rate-limit", "429", "permission_denied"}, decide.ErrRestricted},
		{[]string{"rate_limit", "403"}, decide.ErrRestricted},
		{[]string{"policy", "", "cyberPolicy"}, decide.ErrRefused},
		{[]string{"rate-limit", "403", "policy"}, decide.ErrRefused},
		{[]string{"forbidden", "refusal"}, decide.ErrRefused},
		{[]string{"rate-limit", "529"}, decide.ErrRateLimit},
		{[]string{"", "unknown", "overloaded_error"}, decide.ErrOverloaded},
		{[]string{"", "500", "boom"}, decide.ErrUnknown},
		{nil, decide.ErrUnknown},
	} {
		if got := Classify(tc.codes...); got != tc.want {
			t.Errorf("%v: got %s want %s", tc.codes, got, tc.want)
		}
	}
}

func TestParseState(t *testing.T) {
	if ParseState("ACTIVE") != decide.Active || ParseState("failed") != decide.Error || ParseState("weird") != decide.Unknown {
		t.Error("state mapping")
	}
}
