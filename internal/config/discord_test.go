package config

import "testing"

func TestDiscordReportDefaultsAndBinding(t *testing.T) {
	c, err := Parse([]byte(valid + "  report:\n    format: discord\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Loop.Report
	if r.Scheme != "discord-webhook" || r.URLEnv != "LOOPTENDER_DISCORD_WEBHOOK_URL" || r.SecretEnv != "" {
		t.Fatalf("wrong defaults: %+v", r)
	}
	for _, extra := range []string{"format: discord\n    scheme: bearer", "format: standard\n    scheme: discord-webhook", "format: discord\n    secret_env: EXAMPLE_TOKEN"} {
		if _, err := Parse([]byte(valid + "  report:\n    " + extra + "\n")); err == nil {
			t.Fatalf("accepted invalid binding: %s", extra)
		}
	}
}
