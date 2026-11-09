package adapter

import (
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestClassifyCode(t *testing.T) {
	cases := map[string]decide.ErrorClass{
		"429": decide.ErrRateLimit, "rate_limit": decide.ErrRateLimit, "usage_limit": decide.ErrRateLimit,
		"401": decide.ErrAuth, "403": decide.ErrAuth, "authentication_error": decide.ErrAuth,
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

func TestParseState(t *testing.T) {
	if ParseState("ACTIVE") != decide.Active || ParseState("failed") != decide.Error || ParseState("weird") != decide.Unknown {
		t.Error("state mapping")
	}
}
