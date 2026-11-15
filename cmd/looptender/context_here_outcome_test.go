package main

import (
	"strings"
	"testing"
)

// The outcome sentence for each combination of counted and unchecked loops.
// "no loop targets this session" is said only when every recorded loop was
// checked.
func TestContextHereOutcome(t *testing.T) {
	loop := func(name string) hereLoop { return hereLoop{Loop: name} }
	checked := hereSkip{Count: 2, Reason: "its configuration file no longer exists", Verified: true}
	unchecked := hereSkip{Count: 1, Loop: "odd-loop", Reason: "its state.json cannot be read, so its active route is unknown"}
	for _, c := range []struct {
		name        string
		subject     string
		loops       []hereLoop
		skips       []hereSkip
		unavailable string
		outcome     string
		text        string
	}{
		{"nothing recorded", "", nil, nil, "", hereNone, hereNoLoop},
		{"only stale records", "", nil, []hereSkip{checked}, "", hereNone, hereNoLoop},
		{"one", "", []hereLoop{loop("example-loop")}, []hereSkip{checked}, "", hereOne, "loop example-loop targets this session"},
		{"several", "", []hereLoop{loop("example-loop"), loop("second-loop")}, nil, "", hereSeveral, "2 loops target this session: example-loop, second-loop. Two loops on one session can both prompt it, though never at the same moment"},
		{"none known, one unchecked", "", nil, []hereSkip{unchecked, checked}, "", hereUnknown, "no loop is known to target this session; whether 1 other recorded loop(s) do could not be checked"},
		{"one, one unchecked", "", []hereLoop{loop("example-loop")}, []hereSkip{unchecked}, "", hereUnknown, "loop example-loop targets this session; whether 1 other recorded loop(s) do could not be checked"},
		{"records unreadable", "", nil, nil, "the session record of BB thread thr_example cannot be read", hereUnknown, "whether a loop targets this session is unknown: the session record of BB thread thr_example cannot be read"},
		{"working directory, none", "a session working in /example/project/sub", nil, []hereSkip{checked}, "", hereNone, "no loop targets a session working in /example/project/sub"},
		{"working directory, one", "a session working in /example/project", []hereLoop{loop("example-loop")}, nil, "", hereOne, "loop example-loop targets a session working in /example/project"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := hereView{subject: c.subject, Loops: c.loops, NotCounted: c.skips, Unavailable: c.unavailable}
			v.finish()
			if v.Outcome != c.outcome || v.Text != c.text {
				t.Fatalf("got %q %q", v.Outcome, v.Text)
			}
			if c.outcome != hereNone && strings.Contains(v.Text, hereNoLoop) {
				t.Fatalf("%q says no loop without having checked every one", v.Text)
			}
		})
	}
}

// The caller sentences say how the caller was identified, and the basis
// sentences what the search does not cover.
func TestContextHereFixedSentences(t *testing.T) {
	for got, want := range map[string]string{
		hereBBCaller:     "taken from BB_THREAD_ID, which is checked only for its form and not with BB; the working directory is not used.",
		hereDirCaller:    "BB_THREAD_ID is not set, so the caller is identified only by its working directory: a loop whose active target is a headless agent working in this directory is found, whichever conversation in that directory is asking.",
		hereProcessLimit: "A process loop's session is its resume command, which a working directory cannot match, so a process loop is not found this way.",
		hereRecordsBasis: "Loops are found only in the session records that enable and each check write, then checked against each loop's current configuration and state; a loop that has not recorded this session is not found.",
		hereNoParent:     "Parent threads are not looked up, so a lane is not found through the loop that supervises its parent.",
		hereExactDir:     "Only this exact directory matches: a session working in a parent or a subdirectory of it is not found.",
		hereInherited:    "An agent that looptender's tick or daemon starts inherits the BB_THREAD_ID they were started with, so a headless or process agent asking from such a turn is identified as that BB thread, not as its own session, and may be shown another loop or none.",
	} {
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	c, ids := hereCallerOf("", "/example/project")
	if c.Source != "working_directory" || len(ids) != 4 {
		t.Fatalf("%+v %v", c, ids)
	}
	for _, id := range ids {
		if id.Kind == "process" || id.Kind == "bb" || id.ID != "/example/project" {
			t.Fatalf("%v", ids)
		}
	}
	c, ids = hereCallerOf("thr_example", "")
	if c.Source != "bb_thread_id" || len(ids) != 1 || ids[0].Kind != "bb" || ids[0].ID != "thr_example" {
		t.Fatalf("%+v %v", c, ids)
	}
}
