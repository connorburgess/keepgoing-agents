package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestHeadlessResultEvidence(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		for _, exit := range []string{"1", "invalid"} {
			dir := t.TempDir()
			body := `{"type":"result","is_error":true,"api_error_status":503,"subtype":"connection_error","result":"SECRET_BODY"}`
			if kind == "codex" {
				body = `{"type":"turn.failed","error":{"code":"connection_error","status":503,"message":"SECRET_BODY"}}`
			}
			for name, value := range map[string]string{"run.json": `{"pid":0}`, "exit": exit, "stdout": body} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var o decide.Observation
			var err error
			if kind == "claude" {
				o, err = NewClaude(config.Claude{}, dir).Observe(context.Background())
			} else {
				o, err = NewCodex(config.Codex{}, dir).Observe(context.Background())
			}
			if err != nil || o.Evidence.HTTPStatus == nil || *o.Evidence.HTTPStatus != 503 || o.Evidence.Code != "connection_error" {
				t.Fatal(kind, o, err)
			}
			if (o.Evidence.ExitCode != nil) != (exit == "1") {
				t.Fatal("invented exit status", o)
			}
		}
	}
}

func TestClaudeInvalidStatusIsNotRecordedAsHTTP(t *testing.T) {
	for _, v := range []any{429.5, "SECRET_BODY", 999.0} {
		d := (ClaudeResult{APIErrorStatus: v}).evidence(Status{})
		if d.HTTPStatus != nil || d.Code != "unavailable" {
			t.Fatal(d)
		}
	}
}
