package evidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDetailPrivacyAndNumericAvailability(t *testing.T) {
	d := Clean(Detail{Source: "bb_provider_error", Category: "stream-disconnected", Code: "responseStreamDisconnected", HTTPStatus: Number(503), ExitCode: Number(-1)})
	if d.Category != "stream-disconnected" || d.Code != "responsestreamdisconnected" || d.HTTPStatus == nil || *d.HTTPStatus != 503 || d.ExitCode != nil {
		t.Fatal(d)
	}
	for _, source := range []string{"bb_provider_error", "SECRET_BODY"} {
		d = Clean(Detail{Source: source, Category: "SECRET_BODY", Code: "https://example.com/SECRET_BODY", HTTPStatus: Number(999), ExitCode: Number(-1)})
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), "SECRET_BODY") || d.HTTPStatus != nil || d.ExitCode != nil {
			t.Fatal(string(b))
		}
	}
	zero := Clean(Detail{Source: "process_exit", ExitCode: Number(0)})
	if zero.ExitCode == nil || *zero.ExitCode != 0 {
		t.Fatal("zero exit became unavailable")
	}
}

func TestGenericThreadFailureCode(t *testing.T) {
	for _, raw := range []string{"thread_command_failed", "thread_command_failed PRIVATE_DETAIL"} {
		d := Clean(Detail{Source: "bb_system_error", Code: raw})
		want := "unavailable"
		if raw == "thread_command_failed" {
			want = raw
		}
		if d.Code != want || d.Category != "unavailable" || d.HTTPStatus != nil {
			t.Fatal(d)
		}
	}
}
