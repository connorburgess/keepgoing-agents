package config

import "testing"

func TestPushoverReportDefaultsAndBinding(t *testing.T) {
	c, err := Parse([]byte(valid + "  report:\n    format: pushover\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Loop.Report
	if r.Scheme != "pushover" || r.URLEnv != "LOOPTENDER_PUSHOVER_API_URL" || r.SecretEnv != "LOOPTENDER_PUSHOVER_APP_TOKEN" || r.UserEnv != "LOOPTENDER_PUSHOVER_USER_KEY" {
		t.Fatalf("wrong defaults: %+v", r)
	}
	for _, extra := range []string{"format: pushover\n    scheme: bearer", "format: standard\n    scheme: pushover", "format: slack\n    user_env: EXAMPLE_KEY"} {
		if _, err := Parse([]byte(valid + "  report:\n    " + extra + "\n")); err == nil {
			t.Fatalf("accepted %s", extra)
		}
	}
}
