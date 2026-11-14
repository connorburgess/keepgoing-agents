//go:build unix

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestBuildReviewPersonPanelRefusesAgentAndUnidentifiedOpenings(t *testing.T) {
	w := newPanelChangeWorld(t, true)
	if w.rec.BuildAuthority != "person" {
		t.Fatalf("%+v", w.rec)
	}
	for _, caller := range []string{"agent", "", "unknown"} {
		reply, e := panelControl(context.Background(), w.dir, panelRequest{Op: "open", CallerKind: caller})
		if e == nil || reply.Address != "" || reply.File != "" {
			t.Fatalf("caller %q received opening: %+v %v", caller, reply, e)
		}
	}
	t.Setenv("BB_THREAD_ID", "thr_example_agent")
	if address, e := panelRequestOpening(context.Background(), w.dir, w.rec, "", false); e == nil || address != "" {
		t.Fatalf("agent reused person panel: %s %v", address, e)
	}
	// The CLI also refuses before requesting a new opening.
	var out, errs strings.Builder
	if code := panelCommand(env{context.Background(), &out, &errs}, []string{"--allow-changes", "--no-open", "--panel-directory", w.dir, "--directory", w.root + "/registry"}); code != exitFailed || !strings.Contains(errs.String(), "authority") {
		t.Fatalf("%d %s %s", code, out.String(), errs.String())
	}
}

func TestBuildReviewAgentPanelCannotBecomePersonThroughReuse(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_agent")
	w := newPanelChangeWorld(t, true)
	e := w.entry(t, "docs-site-refresh")
	if w.rec.BuildAuthority != "agent" {
		t.Fatalf("%+v", w.rec)
	}
	// A real socket opening from the agent gets a session, but no build action.
	_, p := w.post(t, dockKey(e.ID), "prepare", w.form, `{"action":"replace_build"}`, nil)
	if p.Preview != nil || p.Result == nil || p.Result.Outcome != "refused" {
		t.Fatalf("%+v", p)
	}
	t.Setenv("BB_THREAD_ID", "")
	if address, err := panelRequestOpening(context.Background(), w.dir, w.rec, "", false); err == nil || address != "" {
		t.Fatalf("person silently reused agent panel: %s %v", address, err)
	}
	// Changing the invoking environment does not promote the old agent session.
	_, p = w.post(t, dockKey(e.ID), "prepare", w.form, `{"action":"replace_build"}`, nil)
	if p.Preview != nil || p.Result == nil || p.Result.Outcome != "refused" {
		t.Fatalf("%+v", p)
	}
	_, body := dockRequest(t, http.MethodGet, w.page+dockAPIPath, withCookie(w.cookie))
	if strings.Contains(body, `"action":"replace_build"`) {
		t.Fatal("old agent session promoted to person")
	}
	for _, caller := range []string{"person", ""} {
		if reply, err := panelControl(context.Background(), w.dir, panelRequest{Op: "open", CallerKind: caller}); err == nil || reply.Address != "" {
			t.Fatalf("%q opened agent server: %+v %v", caller, reply, err)
		}
	}
	if panelBuildReuseAllowed(panelRecord{Changes: true}, "person") {
		t.Fatal("old unidentified protocol grants person authority")
	}
}

func TestBuildReviewStartupAuthoritySurvivesProcessRestartAndReuse(t *testing.T) {
	w := newPanelProcessWorld(t)
	w.env = append(w.env, "BB_THREAD_ID=thr_example_agent")
	if code, out, errs := w.open("--allow-changes", "--no-open"); code != exitOK {
		t.Fatalf("start agent: %d %s %s", code, out, errs)
	}
	agent := w.record()
	if agent.BuildAuthority != "agent" {
		t.Fatalf("startup identity stripped: %+v", agent)
	}
	w.env = append(w.env, "BB_THREAD_ID=")
	if code, out, errs := w.open("--allow-changes", "--no-open"); code != exitFailed || !strings.Contains(errs, "authority") {
		t.Fatalf("person reuse: %d %s %s", code, out, errs)
	}
	if w.record() != agent {
		t.Fatal("reuse silently restarted panel")
	}
	if code, out, errs := w.run("panel", "stop", "--panel-directory", w.dir); code != exitOK {
		t.Fatalf("stop: %d %s %s", code, out, errs)
	}
	// Only an explicit stop and new person-started process changes the class.
	if code, out, errs := w.open("--allow-changes", "--no-open"); code != exitOK {
		t.Fatalf("explicit person restart: %d %s %s", code, out, errs)
	}
	person := w.record()
	if person.BuildAuthority != "person" || person.Instance == agent.Instance {
		t.Fatalf("restart authority: %+v %+v", agent, person)
	}
	w.env = append(w.env, "BB_THREAD_ID=thr_example_agent")
	if code, out, errs := w.open("--allow-changes", "--no-open"); code != exitFailed || !strings.Contains(errs, "authority") {
		t.Fatalf("agent reuse: %d %s %s", code, out, errs)
	}
	if w.record() != person {
		t.Fatal("agent reuse silently changed person server")
	}
}

func TestBuildReviewLegacyRestartCannotPromoteAgentAuthority(t *testing.T) {
	registryDir, legacy, current := panelTwoLocations(t)
	t.Setenv("BB_THREAD_ID", "thr_example_agent")
	done := panelServeAt(t, legacy, registryDir, true)
	defer func() {
		_, _ = panelControl(context.Background(), legacy, panelRequest{Op: "stop"})
		panelStoppedWithin(t, done, "agent legacy panel")
	}()
	first, ok := panelLive(context.Background(), legacy)
	if !ok || first.BuildAuthority != "agent" {
		t.Fatalf("%+v", first)
	}
	t.Setenv("BB_THREAD_ID", "")
	var out, errs strings.Builder
	code := panelCommand(env{context.Background(), &out, &errs}, []string{"--no-open", "--allow-changes", "--directory", registryDir})
	if code != exitFailed || !strings.Contains(errs.String(), "authority") {
		t.Fatalf("%d %s %s", code, out.String(), errs.String())
	}
	again, ok := panelLive(context.Background(), legacy)
	if !ok || again.Instance != first.Instance {
		t.Fatal("legacy panel silently retired")
	}
	if _, ok = panelLive(context.Background(), current); ok {
		t.Fatal("person panel silently started")
	}
	if _, err := panelRequestOpening(context.Background(), legacy, panelRecord{Changes: true}, "", false); err == nil || !strings.Contains(err.Error(), "authority") {
		t.Fatalf("unidentified old protocol: %v", err)
	}
}
