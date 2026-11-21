package config

import (
	"strings"
	"testing"
)

func TestNtfyReportDefaultsAndScheme(t *testing.T) {
	c, err := Parse([]byte(valid + "  report:\n    format: ntfy\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Loop.Report
	if r.Scheme != "bearer" || r.URLEnv != "LOOPTENDER_NTFY_URL" || r.SecretEnv != "LOOPTENDER_NTFY_TOKEN" {
		t.Fatalf("wrong defaults: %+v", r)
	}
	if _, err := Parse([]byte(valid + "  report:\n    format: ntfy\n    scheme: standard-webhooks\n")); err == nil || !strings.Contains(err.Error(), "ntfy format requires bearer") {
		t.Fatalf("wrong scheme accepted: %v", err)
	}
}
