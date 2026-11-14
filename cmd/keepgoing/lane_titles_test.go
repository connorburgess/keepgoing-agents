//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

const laneSecret = "PRIVATE-LANE-TITLE"

// laneFixture is a loop whose BB thread has three direct children: one with
// a title, one whose title carries a control character, one with none.
func laneFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child_a"},{"id":"thr_child_b"},{"id":"thr_child_c"}]`, 0o600)
	write(t, filepath.Join(f.bbDir, "thr_child_a.show"), `{"thread":{"id":"thr_child_a","parentThreadId":"thr_primary","status":"active","title":"Fix the login form `+laneSecret+`"}}`, 0o600)
	write(t, filepath.Join(f.bbDir, "thr_child_b.show"), `{"thread":{"id":"thr_child_b","parentThreadId":"thr_primary","status":"error","title":"Second\u0007 lane\n"}}`, 0o600)
	write(t, filepath.Join(f.bbDir, "thr_child_c.show"), `{"thread":{"id":"thr_child_c","parentThreadId":"thr_primary","status":"active"}}`, 0o600)
	return f
}

// Through a real check against the stand-in bb: the titles are kept in the
// private file only, cleaned, keyed by the lanes' hashed keys, and dated by
// the listing they came from; nothing else carries them.
func TestLaneTitlesAreRecordedPrivatelyAndNowhereElse(t *testing.T) {
	f := laneFixture(t)
	f.tick()
	dir := filepath.Join(f.dir, "state")
	titles, err := state.LoadLaneTitles(dir)
	if err != nil || titles == nil {
		t.Fatalf("%+v %v", titles, err)
	}
	info, err := os.Stat(filepath.Join(dir, state.LaneTitlesName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info.Mode(), err)
	}
	g, _ := rungraph.Load(dir)
	if g.Waiting == nil || !g.Waiting.CheckedAt.Equal(titles.CheckedAt) {
		t.Fatalf("titles dated %v, listing %+v", titles.CheckedAt, g.Waiting)
	}
	want := map[string]string{childKeyOf("thr_child_a"): "Fix the login form " + laneSecret, childKeyOf("thr_child_b"): "Second lane"}
	got := map[string]string{}
	for _, x := range titles.Titles {
		got[x.Key] = x.Title
	}
	if len(got) != len(want) || got[childKeyOf("thr_child_a")] != want[childKeyOf("thr_child_a")] || got[childKeyOf("thr_child_b")] != want[childKeyOf("thr_child_b")] {
		t.Fatalf("%v", got)
	}
	// No other file of the loop holds a title.
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == state.LaneTitlesName {
			return err
		}
		if b, err := os.ReadFile(path); err == nil && bytes.Contains(b, []byte(laneSecret)) {
			t.Errorf("%s holds a lane title", d.Name())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Nor does any command's output, or any report.
	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"timeline"}, {"timeline", "--json"}, {"runs"}, {"runs", "--json"}} {
		if _, out, errs := f.run(args...); strings.Contains(out+errs, laneSecret) {
			t.Errorf("%v shows a lane title", args)
		}
	}
	reports, _ := json.Marshal(f.hooks.events)
	if bytes.Contains(reports, []byte(laneSecret)) {
		t.Error("a report carries a lane title")
	}
}

// The panel shows a recorded title only with identifiers, as hold notes;
// by default a lane says one is withheld. A file written by another check
// than the listing shown gives no titles.
func TestLaneTitlesAreWithheldByDefaultInThePanel(t *testing.T) {
	f := laneFixture(t)
	f.tick()
	regDir := filepath.Join(f.dir, "registry")
	if code, out, errs := run2(f, "registry", "add", "--directory", regDir, "--json"); code != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	d := &dockReader{directory: regDir, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon}
	body, err := d.json(false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(laneSecret)) || bytes.Contains(body, []byte("Fix the login form")) {
		t.Fatal("a lane title in the default report")
	}
	var r dockReport
	if err := json.Unmarshal(body, &r); err != nil || len(r.Loops) != 1 {
		t.Fatal(err)
	}
	evidence := map[string]int{}
	for _, l := range r.Loops[0].Lanes.Lanes {
		evidence[l.TitleEvidence]++
		if l.Title != "" {
			t.Errorf("a title in the default view: %+v", l)
		}
	}
	if evidence["withheld"] != 2 || evidence["not_recorded"] != 1 {
		t.Errorf("%v", evidence)
	}
	d.cache = nil
	withIDs, err := d.json(true)
	if err != nil || !bytes.Contains(withIDs, []byte("Fix the login form "+laneSecret)) || !bytes.Contains(withIDs, []byte(`"titleEvidence":"recorded"`)) {
		t.Fatalf("identifier view: %v", err)
	}
	// A file from another check than the listing shown is not used.
	dir := filepath.Join(f.dir, "state")
	titles, _ := state.LoadLaneTitles(dir)
	if err := state.SaveLaneTitles(dir, titles.CheckedAt.Add(-1), map[string]string{childKeyOf("thr_child_a"): "an older title"}); err != nil {
		t.Fatal(err)
	}
	d.cache = nil
	body, _ = d.json(true)
	if bytes.Contains(body, []byte("an older title")) || bytes.Contains(body, []byte(`"titleEvidence":"recorded"`)) {
		t.Fatal("a title from another listing was shown")
	}
}

func run2(f *fixture, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(context.Background(), append(args, "-c", f.cfg), &out, &errb)
	return code, out.String(), errb.String()
}

// An incomplete listing leaves the titles of the last complete one, dated by
// it; removing the file changes no decision.
func TestLaneTitlesChangeNoDecision(t *testing.T) {
	f := laneFixture(t)
	f.tick()
	dir := filepath.Join(f.dir, "state")
	first, _ := state.LoadLaneTitles(dir)
	// The listing fails: the file is left as it was.
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `not json`, 0o600)
	f.tick()
	if again, _ := state.LoadLaneTitles(dir); again == nil || !again.CheckedAt.Equal(first.CheckedAt) || len(again.Titles) != len(first.Titles) {
		t.Fatalf("an incomplete listing changed the titles: %+v", again)
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child_a"},{"id":"thr_child_b"},{"id":"thr_child_c"}]`, 0o600)
	// The same check twice from the same records, with and without the file.
	backup := t.TempDir()
	copyTree(t, dir, backup)
	a := f.tick()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	copyTree(t, backup, dir)
	if err := os.Remove(filepath.Join(dir, state.LaneTitlesName)); err != nil {
		t.Fatal(err)
	}
	b := f.tick()
	if a["action"] != b["action"] || a["reason"] != b["reason"] || a["state"] != b["state"] {
		t.Fatalf("with titles %v, without %v", a, b)
	}
	if again, _ := state.LoadLaneTitles(dir); again == nil || len(again.Titles) != 2 {
		t.Fatalf("the next complete listing did not record titles again: %+v", again)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	if err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
}
