//go:build unix

package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/control"
)

// The panel's and client review's resume review is refused for the loop's
// supervised session before anything is offered, and client apply refuses
// it with the same words.
func TestResumeReviewRefusesTheSupervisedSession(t *testing.T) {
	d, byName := clientReviewWorld(t)
	e := byName["docs-site-refresh"]
	key := dockKey(e.ID)
	if _, err := control.Update(e.StateDir, func(f *control.File) { f.Paused = true }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BB_THREAD_ID", "")
	_, review, _ := runClientReview(t, "review", "resume", "--loop", key, "--directory", d.directory, "--json")
	if review.Preview == nil {
		t.Fatalf("a person was not offered the resume: %+v", review.Result)
	}
	before := controlOf(t, e)
	want := panelSupervisedSession + " Nothing was changed."
	t.Setenv(control.SupervisedLoopVar, e.StateDir)
	if _, refused := panelReviewForCaller(context.Background(), d.directory, d, key, "resume", "", time.Now()); refused == nil || refused.Outcome != "refused" || refused.Text != want {
		t.Fatalf("panel review offered a resume to the supervised session: %+v", refused)
	}
	if code, reply, _ := runClientReview(t, "review", "resume", "--loop", key, "--directory", d.directory, "--json"); code != exitFailed || reply.Preview != nil || reply.Result == nil || reply.Result.Text != want {
		t.Fatalf("client review offered a resume to the supervised session: %d %+v", code, reply)
	}
	if code, reply, _ := runClientReview(t, "apply", "resume", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json"); code != exitFailed || reply.Result == nil || reply.Result.Text != want {
		t.Fatalf("client apply resumed for the supervised session: %d %+v", code, reply.Result)
	}
	if after := controlOf(t, e); !reflect.DeepEqual(after, before) || !after.Paused {
		t.Fatalf("the loop changed: %+v", after)
	}
}
