package report

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/looptender/looptender/internal/config"
)

func TestBBPushContentPreservesEventAtDisplayLimits(t *testing.T) {
	for _, event := range config.ReportEvents {
		title, body := bbPushContent(Event{Type: event, Loop: strings.Repeat("🙂", 200), Text: "private task", Data: map[string]any{"detail": "private data"}}, strings.Repeat("🚀", 200))
		if len(utf16.Encode([]rune(title))) > 80 || len(utf16.Encode([]rune(body))) > 180 || !utf8.ValidString(body) {
			t.Fatal("content exceeds host display budget")
		}
		if !strings.Contains(title, event) || !strings.Contains(body, "Loop: ") || !strings.Contains(body, "Device: ") {
			t.Fatal("event or configured labels lost")
		}
		if strings.Contains(body, "private") || strings.Contains(body, "�") {
			t.Fatal("private text or split scalar")
		}
	}
	title, body := bbPushContent(Event{Type: "private unknown", Loop: "\x00\n\u202e"}, "\t")
	if title != "Looptender: unknown" || body != "Loop: unspecified · Device: unspecified" {
		t.Fatalf("unsafe content %q %q", title, body)
	}
	_, body = bbPushContent(Event{Type: "test", Loop: strings.Repeat("a", 79) + "🙂"}, strings.Repeat("e\u0301", 100))
	if strings.Contains(body, "🙂") || len(utf16.Encode([]rune(body))) > 180 {
		t.Fatal("non-BMP boundary exceeded")
	}
}

func TestBBPushAcknowledgementContract(t *testing.T) {
	for _, reply := range []string{`{"ok":true,"channels":["mobile"]}`, `{"channels":["web","desktop","mobile"],"ok":true}`} {
		if !validBBPushReply([]byte(reply)) {
			t.Fatalf("refused valid acknowledgement %s", reply)
		}
	}
	for _, reply := range []string{
		`null`, `[]`, `{}`, `{"ok":true}`, `{"ok":true,"channels":null}`, `{"ok":true,"channels":[]}`,
		`{"ok":false,"channels":["web"]}`, `{"ok":null,"channels":["web"]}`, `{"ok":"true","channels":["web"]}`,
		`{"OK":true,"channels":["web"]}`, `{"ok":true,"Channels":["web"]}`,
		`{"ok":true,"ok":true,"channels":["web"]}`, `{"ok":true,"channels":["web"],"channels":null}`,
		`{"ok":true,"channels":["unknown"]}`, `{"ok":true,"channels":["web","web"]}`, `{"ok":true,"channels":[null]}`,
		`{"ok":true,"channels":["web"],"extra":"private"}`, `{"ok":true,"channels":["web"]} {}`,
		`{"ok":true,"channels":["web"]`, strings.Repeat(" ", maxBBPushReply) + `{"ok":true,"channels":["web"]}`,
	} {
		if validBBPushReply([]byte(reply)) {
			t.Fatalf("accepted ambiguous acknowledgement %s", reply)
		}
	}
}
