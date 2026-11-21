package config

import "testing"

func TestSlackReportDefaultsAndBinding(t *testing.T) {
	c, err := Parse([]byte(valid + "  report:\n    format: slack\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Loop.Report
	if r.Scheme != "slack-webhook" || r.URLEnv != "LOOPTENDER_SLACK_WEBHOOK_URL" || r.SecretEnv != "" {
		t.Fatalf("wrong defaults: %+v", r)
	}
	for _, extra := range []string{"format: slack\n    scheme: bearer", "format: standard\n    scheme: slack-webhook", "format: slack\n    secret_env: EXAMPLE_TOKEN"} {
		if _, err := Parse([]byte(valid + "  report:\n    " + extra + "\n")); err == nil {
			t.Fatalf("accepted invalid binding: %s", extra)
		}
	}
}
