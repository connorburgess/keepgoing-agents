package prbacklog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var at = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

func policy() Policy {
	return Policy{Repo: "example/project", MaxOpen: 2, Interval: 15 * time.Minute, MaxAge: time.Hour, HeldLabel: "workflow-held", SupersededLabel: "workflow-superseded"}
}
func pr(n int) map[string]any {
	return map[string]any{"number": n, "createdAt": at.Add(-time.Hour), "updatedAt": at.Add(-time.Minute), "additions": 12, "deletions": 3, "changedFiles": 2, "isDraft": false, "baseRefName": "main", "headRefName": fmt.Sprintf("cb/example-%d", n), "headRepository": map[string]any{"nameWithOwner": "example/project"}, "mergeStateStatus": "CLEAN", "reviewDecision": "APPROVED", "labels": map[string]any{"totalCount": 0, "nodes": []any{}}}
}
func response(total int, next bool, cursor string, prs ...map[string]any) []byte {
	nodes := []any{}
	for _, v := range prs {
		nodes = append(nodes, v)
	}
	b, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"nameWithOwner": "example/project", "pullRequests": map[string]any{"totalCount": total, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": cursor}, "nodes": nodes}}}})
	return b
}
func collect(t *testing.T, p Policy, pages ...[]byte) Report {
	t.Helper()
	calls := 0
	r := Collect(context.Background(), func(ctx context.Context, args ...string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		joined := strings.Join(args, " ")
		for _, bad := range []string{"mutation", "title", "body", "comments", "statusCheckRollup"} {
			if strings.Contains(joined, bad) {
				t.Fatalf("unwanted query %s", bad)
			}
		}
		if !strings.Contains(joined, "--hostname github.com") || !strings.Contains(joined, "owner=example") {
			t.Fatal(joined)
		}
		if calls >= len(pages) {
			t.Fatal("unexpected extra query")
		}
		b := pages[calls]
		calls++
		return b, nil
	}, p, func() time.Time { return at })
	if calls != len(pages) {
		t.Fatalf("calls %d want %d", calls, len(pages))
	}
	return r
}
func TestInventoryThresholdAndPrivacy(t *testing.T) {
	p := policy()
	a, b := pr(1), pr(2)
	a["labels"] = map[string]any{"totalCount": 2, "nodes": []any{map[string]any{"name": "workflow-held"}, map[string]any{"name": "workflow-superseded"}}}
	a["isDraft"] = true
	b["baseRefName"] = "cb/example-1"
	b["mergeStateStatus"] = "BEHIND"
	r := collect(t, p, response(2, true, "second", a), response(2, false, "", b))
	if !r.Complete || r.Summary.Open != 2 || r.Summary.Draft != 1 || r.Summary.Held != 1 || r.Summary.Superseded != 1 || r.Summary.Dependencies != 1 || r.Summary.Blocked != 2 || len(r.Rows[1].DependsOn) != 1 || r.Rows[1].DependsOn[0] != 1 {
		t.Fatalf("%+v %+v", r, r.Summary)
	}
	c := Update(nil, p, r, at)
	text, _ := Notice(p, c, at)
	if !strings.Contains(text, "integration_first") || !strings.Contains(text, "explicit reason") {
		t.Fatal(text)
	}
	raw, _ := json.Marshal(c)
	for _, s := range []string{"example/project", "cb/example-1", "workflow-held", "workflow-superseded", "number", "APPROVED"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("private field retained: %s", raw)
		}
	}
}
func TestInventoryUnknownBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages [][]byte
		code  string
	}{
		{"null", [][]byte{[]byte(`{"data":{"repository":null}}`)}, "invalid_response"},
		{"graphql-error", [][]byte{[]byte(`{"errors":[{"message":"private-secret"}],"data":null}`)}, "invalid_response"},
		{"changed", [][]byte{response(2, true, "second", pr(1)), response(1, false, "", pr(2))}, "inventory_changed"},
		{"duplicate", [][]byte{response(2, true, "second", pr(1)), response(2, false, "", pr(1))}, "inventory_changed"},
		{"missing", [][]byte{response(1, false, "")}, "inventory_changed"},
		{"cursor", [][]byte{response(2, true, "", pr(1))}, "invalid_response"},
		{"labels", func() [][]byte {
			v := pr(1)
			v["labels"] = map[string]any{"totalCount": 101, "nodes": []any{}}
			return [][]byte{response(1, false, "", v)}
		}(), "partial_labels"},
		{"required", func() [][]byte { v := pr(1); delete(v, "additions"); return [][]byte{response(1, false, "", v)} }(), "invalid_response"},
		{"cap", [][]byte{response(501, true, "a", pr(1)), response(501, true, "b", pr(2)), response(501, true, "c", pr(3)), response(501, true, "d", pr(4)), response(501, true, "e", pr(5))}, "inventory_capped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policy()
			p.MaxOpen = 5000
			r := collect(t, p, tc.pages...)
			if r.Complete || r.Failure != tc.code || r.Summary != nil {
				t.Fatalf("%+v", r)
			}
			text, _ := Notice(p, Update(nil, p, r, at), at)
			if strings.Contains(text, "below_threshold") || strings.Contains(text, "private-secret") {
				t.Fatal(text)
			}
		})
	}
}
func TestZeroForeignDependencyAndUnknownStates(t *testing.T) {
	r := collect(t, policy(), response(0, false, ""))
	if !r.Complete || r.Summary == nil || r.Summary.Open != 0 {
		t.Fatal(r)
	}
	a, b := pr(1), pr(2)
	a["headRepository"] = map[string]any{"nameWithOwner": "other/project"}
	b["baseRefName"] = "cb/example-1"
	b["mergeStateStatus"] = "NEW_STATUS"
	b["reviewDecision"] = nil
	r = collect(t, policy(), response(2, false, "", a, b))
	if !r.Complete || r.Summary.Dependencies != 0 || r.Summary.Unknown != 1 {
		t.Fatal(r)
	}
}
func TestQueryFailureIsBoundedAndSanitized(t *testing.T) {
	calls := 0
	r := Collect(context.Background(), func(context.Context, ...string) ([]byte, error) {
		calls++
		return []byte("private-secret"), errors.New("private-secret")
	}, policy(), func() time.Time { return at })
	if calls != 1 || r.Failure != "query_failed" || strings.Contains(fmt.Sprint(r), "private-secret") {
		t.Fatal(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = Collect(ctx, func(ctx context.Context, _ ...string) ([]byte, error) { return nil, ctx.Err() }, policy(), func() time.Time { return at })
	if r.Failure != "query_failed" {
		t.Fatal(r)
	}
	r = collect(t, policy(), []byte(strings.Repeat("x", MaxBytes+1)))
	if r.Failure != "output_limit" {
		t.Fatal(r)
	}
}
func TestCacheFreshnessFailureBindingAndRequiredFields(t *testing.T) {
	p := policy()
	r := collect(t, p, response(0, false, ""))
	c := Update(nil, p, r, at)
	if !c.Valid(p, at) || c.Due(p, at.Add(time.Minute)) {
		t.Fatal(c)
	}
	text, d := Notice(p, c, at)
	if !strings.Contains(text, "below_threshold") {
		t.Fatal(text)
	}
	c.Note(d, "primary", "example-conversation", false, true)
	r = collect(t, p, response(0, false, ""))
	c = Update(c, p, r, at.Add(15*time.Minute))
	_, d2 := Notice(p, c, at.Add(15*time.Minute))
	if d != d2 || c.WantsNotice(d2, "primary", "example-conversation", false) {
		t.Fatal("timestamp caused repeat")
	}
	c = Update(c, p, Report{Failure: "query_failed"}, at.Add(30*time.Minute))
	text, d2 = Notice(p, c, at.Add(30*time.Minute))
	if d == d2 || !strings.Contains(text, "status=unknown") || !strings.Contains(text, "historical") {
		t.Fatal(text)
	}
	text, _ = Notice(p, c, at.Add(2*time.Hour))
	if strings.Contains(text, "below_threshold") {
		t.Fatal(text)
	}
	changed := p
	changed.MaxOpen++
	if c.Valid(changed, at.Add(time.Hour)) {
		t.Fatal("wrong binding accepted")
	}
	if c.Valid(p, at.Add(-time.Minute)) {
		t.Fatal("future accepted")
	}
	good := Update(nil, p, r, at)
	b, _ := json.Marshal(good)
	for _, key := range []string{"version", "binding", "attemptedAt", "failure", "goodAt", "notice"} {
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		delete(m, key)
		damaged, _ := json.Marshal(m)
		var got Cache
		if json.Unmarshal(damaged, &got) != nil {
			t.Fatal("decode")
		}
		if got.Valid(p, at) {
			t.Fatalf("accepted missing %s", key)
		}
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["good"] = map[string]any{}
	damaged, _ := json.Marshal(m)
	var got Cache
	_ = json.Unmarshal(damaged, &got)
	if got.Valid(p, at) {
		t.Fatal("empty summary accepted as zero")
	}
}
func TestNoticeAttemptsAndTransitions(t *testing.T) {
	p := policy()
	c := Update(nil, p, Report{Failure: "query_failed"}, at)
	_, d := Notice(p, c, at)
	for i := 0; i < 3; i++ {
		if !c.WantsNotice(d, "primary", "example", false) {
			t.Fatal(i)
		}
		c.Note(d, "primary", "example", false, false)
	}
	if c.WantsNotice(d, "primary", "example", false) {
		t.Fatal("unbounded retries")
	}
	if !c.WantsNotice(d, "primary", "new-example", false) || !c.WantsNotice("changed", "primary", "example", false) {
		t.Fatal("transition suppressed")
	}
}

func TestInvalidCacheCannotBecomeAValidZeroOnRoundTrip(t *testing.T) {
	p := policy()
	c := Update(nil, p, Report{Complete: true, Summary: &Summary{}}, at)
	b, _ := json.Marshal(c)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	delete(fields, "failure")
	b, _ = json.Marshal(fields)
	var damaged Cache
	if err := json.Unmarshal(b, &damaged); err != nil {
		t.Fatal(err)
	}
	if damaged.Valid(p, at) {
		t.Fatal("damaged accepted")
	}
	b, _ = json.Marshal(damaged)
	var saved Cache
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Valid(p, at) {
		t.Fatal("unrelated save repaired damage into all-clear")
	}
	text, _ := Notice(p, &saved, at)
	if strings.Contains(text, "below_threshold") {
		t.Fatal(text)
	}
}
