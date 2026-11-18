package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPRBacklogCLIThresholdUnknownAndNoPrivateText(t *testing.T) {
	zero := `{"data":{"repository":{"nameWithOwner":"example/project","pullRequests":{"totalCount":0,"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`
	one := `{"data":{"repository":{"nameWithOwner":"example/project","pullRequests":{"totalCount":1,"pageInfo":{"hasNextPage":false},"nodes":[{"number":42,"createdAt":"2000-01-01T00:00:00Z","updatedAt":"2000-01-01T00:00:00Z","additions":4,"deletions":2,"changedFiles":1,"isDraft":true,"baseRefName":"main","headRefName":"private-branch","headRepository":{"nameWithOwner":"example/project"},"mergeStateStatus":"BLOCKED","reviewDecision":"REVIEW_REQUIRED","labels":{"totalCount":0,"nodes":[]}}]}}}}`
	for _, tc := range []struct {
		name, raw string
		err       error
		code      int
		status    string
	}{{"zero", zero, nil, exitOK, "below_threshold"}, {"threshold", one, nil, exitFailed, "integration_first"}, {"unknown", "private-secret", errors.New("private-secret"), exitFailed, "unknown"}, {"partial", strings.Replace(zero, `"totalCount":0`, `"totalCount":1`, 1), nil, exitFailed, "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			calls := 0
			e := env{ctx: context.Background(), stdout: &out, stderr: &errout}
			code := prBacklogCommand(e, []string{"--repo", "example/project", "--max-open", "1", "--json"}, func(context.Context, ...string) ([]byte, error) { calls++; return []byte(tc.raw), tc.err })
			if code != tc.code || calls != 1 {
				t.Fatalf("code=%d calls=%d %s", code, calls, errout.String())
			}
			var result map[string]any
			if json.Unmarshal(out.Bytes(), &result) != nil {
				t.Fatal(out.String())
			}
			if !strings.Contains(result["notice"].(string), "status="+tc.status) || strings.Contains(out.String()+errout.String(), "private-") {
				t.Fatal(out.String(), errout.String())
			}
		})
	}
}
func TestPRBacklogCLIValidationAndHelp(t *testing.T) {
	for _, args := range [][]string{{"--repo", "example/project"}, {"--repo", "https://example.com/project", "--max-open", "2"}, {"--repo", "example/project", "--max-open", "2", "extra"}} {
		var out bytes.Buffer
		code := prBacklogCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, args, func(context.Context, ...string) ([]byte, error) {
			t.Fatal("invalid args queried GitHub")
			return nil, nil
		})
		if code != exitUsage {
			t.Fatal(code)
		}
	}
	var out bytes.Buffer
	run(context.Background(), []string{"help"}, &out, &out)
	if !strings.Contains(out.String(), "pr-backlog") {
		t.Fatal("tool not discoverable")
	}
	// Use an explicitly injected bounded response; dispatch validation must not
	// load a loop configuration or invoke gh for missing required options.
	out.Reset()
	if run(context.Background(), []string{"pr-backlog"}, &out, &out) != exitUsage || !strings.Contains(out.String(), "pr-backlog:") {
		t.Fatal(out.String())
	}
}
