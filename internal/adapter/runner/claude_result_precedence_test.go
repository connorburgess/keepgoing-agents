package runner

import (
	"github.com/looptender/looptender/internal/decide"
	"testing"
)

func TestClaudeStructuredFailureOverridesExitZero(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		class        decide.ErrorClass
	}{
		{"limit", `"api_error_status":429`, decide.ErrRateLimit},
		{"authentication", `"api_error_status":401`, decide.ErrAuth},
		{"restriction", `"api_error_status":403`, decide.ErrRestricted},
		{"overload", `"api_error_status":529`, decide.ErrOverloaded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := []byte(`{"type":"result","subtype":"success","is_error":false,` + tc.fields + `}`)
			r := claudeSpec.Read(Status{Phase: Finished, ExitCodeKnown: true, ExitCode: 0, Stdout: out})
			if r.Completed || r.Class != tc.class {
				t.Errorf("reading: %+v", r)
			}
			p := judgeClaudeProbe(out, 0)
			if p.OK || p.Class != tc.class {
				t.Errorf("probe: %+v", p)
			}
		})
	}
}
