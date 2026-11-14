package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

func TestIdentityAndKey(t *testing.T) {
	work := t.TempDir()
	real, _ := filepath.EvalSymlinks(work)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	bb := config.Agent{Adapter: "bb", BB: &config.BB{Thread: "thr_example_one"}}
	newThread := config.Agent{Adapter: "bb", BB: &config.BB{NewThread: true}}
	claude := config.Agent{Adapter: "claude", Claude: &config.Claude{Workdir: real}}
	claudeLink := config.Agent{Adapter: "claude", Claude: &config.Claude{Workdir: link}}
	codex := config.Agent{Adapter: "codex", Codex: &config.Codex{Workdir: real}}
	process := config.Agent{Adapter: "process", Process: &config.Process{ResumeCommand: []string{"/private/example/resume", "--loop", "one"}}}

	for name, a := range map[string]config.Agent{"bb": bb, "claude": claude, "process": process} {
		if _, ok := Of(a); !ok {
			t.Errorf("%s has no identity", name)
		}
	}
	if _, ok := Of(newThread); ok {
		t.Error("a fallback that will create a thread has an identity before it does")
	}
	one, _ := Of(claude)
	two, _ := Of(claudeLink)
	if one != two {
		t.Fatalf("a symbolic link to the working directory is another session: %+v %+v", one, two)
	}
	// Kind plus directory: a Codex loop in the same checkout is another
	// conversation, and so another session.
	if c, _ := Of(codex); c.Key() == one.Key() {
		t.Fatal("claude and codex in one directory share a key")
	}
	b, _ := Of(bb)
	if len(b.Key()) != 32 || strings.Contains(b.Key(), "thr_") || b.Key() == (Identity{Kind: "bb", ID: "thr_example_two"}).Key() {
		t.Fatalf("key %q", b.Key())
	}
	if b.Describe() != "BB thread thr_example_one" {
		t.Fatal(b.Describe())
	}
}

func TestClaimIsExclusiveAndSaysWhoHoldsIt(t *testing.T) {
	dir := t.TempDir()
	id := Identity{Kind: "bb", ID: "thr_example_one"}
	since := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	first, err := Take(dir, id, Holder{Loop: "example-loop", PID: os.Getpid(), Since: since, Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Take(dir, id, Holder{Loop: "example-loop-b", PID: 1})
	var claimed *ClaimedError
	if !errors.As(err, &claimed) || !errors.Is(err, lock.ErrHeld) || claimed.Holder == nil || claimed.Holder.Loop != "example-loop" || claimed.Stuck() {
		t.Fatalf("second take: %v %+v", err, claimed)
	}
	if msg := err.Error(); !strings.Contains(msg, "BB thread thr_example_one is being checked by loop example-loop (pid ") || !strings.Contains(msg, "nothing sent") {
		t.Fatal(msg)
	}
	if h := Peek(dir, id); h == nil || h.Loop != "example-loop" {
		t.Fatalf("peek: %+v", h)
	}
	// Another session is not affected.
	other, err := Take(dir, Identity{Kind: "bb", ID: "thr_example_two"}, Holder{Loop: "example-loop-b", PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
	first.Release()
	if Peek(dir, id) != nil {
		t.Fatal("holder details outlived the claim")
	}
	again, err := Take(dir, id, Holder{Loop: "example-loop-b", PID: os.Getpid()})
	if err != nil {
		t.Fatalf("not free after release: %v", err)
	}
	again.Release()
	for _, e := range mustList(t, dir) {
		if strings.Contains(e, "thr_") {
			t.Fatalf("a file name contains a thread ID: %s", e)
		}
	}
}

func TestAStuckHolderIsNamedWithWhatToDo(t *testing.T) {
	dir := t.TempDir()
	id := Identity{Kind: "bb", ID: "thr_example_one"}
	c, err := Take(dir, id, Holder{Loop: "example-loop", PID: os.Getpid(), Since: time.Now().Add(-time.Hour), Until: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	_, err = Take(dir, id, Holder{Loop: "example-loop-b", PID: 1})
	var claimed *ClaimedError
	if !errors.As(err, &claimed) || !claimed.Stuck() {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{"passed its own time limit", "is stuck", "Find process", "keepgoing does not break the claim", "nothing sent"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message lacks %q: %s", want, err)
		}
	}
}

// The operating system releases a claim when its holder dies; the details it
// left behind are not believed.
func TestAClaimEndsWithItsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix helper")
	}
	dir := t.TempDir()
	id := Identity{Kind: "bb", ID: "thr_example_one"}
	if os.Getenv("KEEPGOING_SESSION_HELPER") == "1" {
		_, err := Take(os.Getenv("KEEPGOING_SESSION_HELPER_DIR"), id, Holder{Loop: "example-loop", PID: os.Getpid(), Until: time.Now().Add(time.Hour)})
		if err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("held\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestAClaimEndsWithItsProcess$")
	cmd.Env = append(os.Environ(), "KEEPGOING_SESSION_HELPER=1", "KEEPGOING_SESSION_HELPER_DIR="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil || string(buf) != "held\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper: %q %v", buf, err)
	}
	if _, err := Take(dir, id, Holder{Loop: "example-loop-b", PID: os.Getpid()}); err == nil {
		t.Fatal("taken while the helper held it")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if Peek(dir, id) != nil {
		t.Fatal("a dead holder's details were believed")
	}
	c, err := Take(dir, id, Holder{Loop: "example-loop-b", PID: os.Getpid()})
	if err != nil {
		t.Fatalf("not free after the holder died: %v", err)
	}
	c.Release()
}

func TestSupervisorsRecord(t *testing.T) {
	dir := t.TempDir()
	id := Identity{Kind: "bb", ID: "thr_example_one"}
	at := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxSupervisors+4; i++ {
		s := Supervisor{Loop: "example-loop", Config: "/private/example/" + string(rune('a'+i)) + "/keepgoing.yaml", StateDir: "/private/example/" + string(rune('a'+i)) + "/state", SeenAt: at.Add(time.Duration(i) * time.Minute)}
		if err := Note(dir, id, s, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	list := Known(dir, id)
	if len(list) != maxSupervisors || list[0].StateDir != "/private/example/"+string(rune('a'+maxSupervisors+3))+"/state" {
		t.Fatalf("%d entries, first %+v", len(list), list[0])
	}
	// Noting a loop again moves it to the front, once.
	if err := Note(dir, id, Supervisor{Loop: "example-loop", Config: "/private/example/e/keepgoing.yaml", StateDir: "/private/example/e/state", SeenAt: at.Add(time.Hour)}, time.Second); err != nil {
		t.Fatal(err)
	}
	list = Known(dir, id)
	count := 0
	for _, s := range list {
		if s.StateDir == "/private/example/e/state" {
			count++
		}
	}
	if list[0].StateDir != "/private/example/e/state" || count != 1 {
		t.Fatalf("%+v", list[:2])
	}
	if got := All(dir, 100); len(got) != maxSupervisors {
		t.Fatalf("all: %d", len(got))
	}
	// A record copied under another session's key is not believed.
	other := Identity{Kind: "bb", ID: "thr_example_two"}
	b, _ := os.ReadFile(filepath.Join(dir, id.Key()+".json"))
	if err := os.WriteFile(filepath.Join(dir, other.Key()+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if Known(dir, other) != nil {
		t.Fatal("a record for another session was believed")
	}
	if runtime.GOOS != "windows" {
		for _, name := range mustList(t, dir) {
			info, _ := os.Stat(filepath.Join(dir, name))
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s is %v", name, info.Mode())
			}
		}
	}
}

func TestDirIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes differ")
	}
	t.Setenv(DirEnv, filepath.Join(t.TempDir(), "sessions"))
	dir, _, err := Dir(true)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("%v", info.Mode())
	}
	// This user's directory, merely too open: looked at, it is reported; with
	// repair, it is made private.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, tightened, err := Dir(); err != nil || tightened || Private(dir) {
		t.Fatalf("looking repaired it: %v %v", tightened, err)
	}
	if _, tightened, err := Dir(true); err != nil || !tightened || !Private(dir) {
		t.Fatalf("repair: %v %v", tightened, err)
	}
	var unsafe *UnsafeError
	t.Setenv(DirEnv, "relative/sessions")
	if _, _, err := Dir(); !errors.As(err, &unsafe) {
		t.Fatalf("a relative directory: %v", err)
	}
	// Something planted at the path, or a link there, is not repaired.
	planted := filepath.Join(t.TempDir(), "sessions")
	if err := os.WriteFile(planted, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DirEnv, planted)
	if _, _, err := Dir(true); !errors.As(err, &unsafe) || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("a file: %v", err)
	}
	link := filepath.Join(t.TempDir(), "sessions")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DirEnv, link)
	if _, _, err := Dir(true); !errors.As(err, &unsafe) || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a link: %v", err)
	}
	// Without the override it is under the user's state directory: in
	// looptender/ on a computer with no earlier files.
	t.Setenv(DirEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	base := filepath.Join(home, "state")
	if runtime.GOOS == "darwin" {
		base = filepath.Join(home, "Library", "Application Support")
	}
	dir, _, err = Dir(true)
	if want := filepath.Join(base, "looptender", "sessions"); err != nil || dir != want {
		t.Fatalf("%s %v, want %s", dir, err, want)
	}
}

// A computer where the tool already keeps files under its earlier name
// keeps its claims there, wherever those files are: a build from before the
// rename, and every other loop, take their claims in the same place. A
// loop's own state in a directory of that name is not such a file.
func TestDirKeepsTheEarlierDirectory(t *testing.T) {
	for _, where := range []string{"state", "config", "only a loop's state_dir"} {
		t.Run(where, func(t *testing.T) {
			t.Setenv(DirEnv, "")
			t.Setenv("KEEPGOING_SESSION_DIR", "")
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
			t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
			t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
			config, err := userdir.ConfigHome()
			if err != nil {
				t.Fatal(err)
			}
			state, err := userdir.StateHome()
			if err != nil {
				t.Fatal(err)
			}
			earlier := map[string]string{
				"state":                   filepath.Join(state, "keepgoing", "panel"),
				"config":                  filepath.Join(config, "keepgoing", "registry"),
				"only a loop's state_dir": filepath.Join(state, "keepgoing", "example-loop"),
			}[where]
			if err := os.MkdirAll(earlier, 0o700); err != nil {
				t.Fatal(err)
			}
			want, unwanted := "keepgoing", "looptender"
			if where == "only a loop's state_dir" {
				want, unwanted = "looptender", filepath.Join("keepgoing", "sessions")
			}
			dir, _, err := Dir(true)
			if want := filepath.Join(state, want, "sessions"); err != nil || dir != want {
				t.Fatalf("%s %v, want %s", dir, err, want)
			}
			if _, err := os.Lstat(filepath.Join(state, unwanted)); err == nil {
				t.Fatalf("%s was made as well", unwanted)
			}
		})
	}
}

func mustList(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Something planted where a claim file belongs is not used as one.
func TestAPlantedClaimFileIsUnsafe(t *testing.T) {
	dir := t.TempDir()
	id := Identity{Kind: "bb", ID: "thr_example_one"}
	if err := os.Mkdir(filepath.Join(dir, id.Key()+".lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Take(dir, id, Holder{Loop: "example-loop", PID: os.Getpid()})
	var unsafe *UnsafeError
	if !errors.As(err, &unsafe) || errors.Is(err, lock.ErrHeld) || !strings.Contains(unsafe.Fix(), "remove ") {
		t.Fatalf("%v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	other := Identity{Kind: "bb", ID: "thr_example_two"}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, other.Key()+".lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := Take(dir, other, Holder{Loop: "example-loop", PID: os.Getpid()}); !errors.As(err, &unsafe) {
		t.Fatalf("a link at the claim file: %v", err)
	}
}

// On a case-insensitive volume, two spellings of one working directory are
// one session.
func TestTwoSpellingsOfOneDirectoryAreOneSession(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "Lab4", "Project")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "lab4", "PROJECT")
	if _, err := os.Stat(other); err != nil {
		t.Skip("this temporary volume is case-sensitive")
	}
	a, _ := Of(config.Agent{Adapter: "claude", Claude: &config.Claude{Workdir: real}})
	b, _ := Of(config.Agent{Adapter: "claude", Claude: &config.Claude{Workdir: other}})
	if a.Key() != b.Key() || a.ID != b.ID || !strings.HasSuffix(a.ID, filepath.Join("Lab4", "Project")) {
		t.Fatalf("%q and %q are different sessions", a.ID, b.ID)
	}
}

// A holder file whose process is gone, or whose pid was reused by another
// process that does not hold the claim, is not believed.
func TestALeftoverHolderFileIsNotBelieved(t *testing.T) {
	dir := t.TempDir()
	id := Identity{Kind: "bb", ID: "thr_example_one"}
	c, err := Take(dir, id, Holder{Loop: "example-loop", PID: os.Getpid(), Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	holder := filepath.Join(dir, id.Key()+".holder")
	b, _ := os.ReadFile(holder)
	c.Release()
	// The details are back, naming a live process (this one) that holds
	// nothing: a reused pid.
	if err := os.WriteFile(holder, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if h := Peek(dir, id); h != nil {
		t.Fatalf("believed %+v", h)
	}
	// Probing does not hold the claim.
	again, err := Take(dir, id, Holder{Loop: "example-loop-b", PID: os.Getpid()})
	if err != nil {
		t.Fatalf("the probe kept the claim: %v", err)
	}
	again.Release()
}

func TestProcessSessionsAreDescribedWithoutTheirCommand(t *testing.T) {
	id, _ := Of(config.Agent{Adapter: "process", Process: &config.Process{ResumeCommand: []string{"/private/example/resume", "--secret-flag"}}})
	if strings.Contains(id.Describe(), "/private/example") || strings.Contains(id.Describe(), "--secret-flag") {
		t.Fatal(id.Describe())
	}
}

// The directory variable is still read under its earlier name. The two names
// set to different directories are refused, naming both and what to do,
// rather than one being chosen: the claims kept in the other would be hidden.
func TestDirReadsTheEarlierName(t *testing.T) {
	const earlier = "KEEPGOING_SESSION_DIR"
	old, current := filepath.Join(t.TempDir(), "sessions"), filepath.Join(t.TempDir(), "sessions")
	t.Setenv(DirEnv, "")
	t.Setenv(earlier, old)
	if dir, _, err := Dir(true); err != nil || dir != old {
		t.Fatalf("the earlier name alone: %q %v", dir, err)
	}
	t.Setenv(DirEnv, current)
	t.Setenv(earlier, current+string(filepath.Separator))
	if dir, _, err := Dir(true); err != nil || dir != current {
		t.Fatalf("both names, one place: %q %v", dir, err)
	}
	t.Setenv(earlier, old)
	var unsafe *UnsafeError
	_, _, err := Dir(true)
	if !errors.As(err, &unsafe) {
		t.Fatalf("both names, different places: %v", err)
	}
	for _, want := range []string{DirEnv, earlier} {
		if !strings.Contains(unsafe.Error(), want) {
			t.Fatalf("%q does not name %s", unsafe.Error(), want)
		}
	}
	if strings.Contains(unsafe.Error(), old) || strings.Contains(unsafe.Fix(), "remove ") || !strings.Contains(unsafe.Fix(), "unset one of them") {
		t.Fatalf("error %q, fix %q", unsafe.Error(), unsafe.Fix())
	}
}

// The default claim directory has two possible names, and two commands can
// for a moment disagree on which is in use. A claim held under either name
// stops a claim of the same session under the other, and the records of who
// supervises a session are read from both. A directory a person chose has
// no other name.
func TestClaimsAndRecordsAreHonouredUnderBothNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv(DirEnv, "")
	t.Setenv("KEEPGOING_SESSION_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	state, err := userdir.StateHome()
	if err != nil {
		t.Fatal(err)
	}
	current, earlier := filepath.Join(state, "looptender", "sessions"), filepath.Join(state, "keepgoing", "sessions")
	id := Identity{Kind: "bb", ID: "thr_example"}
	me := func(loop string) Holder {
		return Holder{Loop: loop, PID: os.Getpid(), Since: time.Now(), Until: time.Now().Add(time.Hour)}
	}

	// One name only: nothing else to look at.
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := Sibling(current); got != "" {
		t.Fatalf("Sibling with one name = %q", got)
	}
	a, err := Take(current, id, me("example-loop-a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Note(current, id, Supervisor{Loop: "example-loop-a", Config: filepath.Join(home, "a", "keepgoing.yaml"), StateDir: filepath.Join(home, "a", "state"), SeenAt: time.Now()}, 0); err != nil {
		t.Fatal(err)
	}

	// The other name appears.
	if err := os.MkdirAll(earlier, 0o700); err != nil {
		t.Fatal(err)
	}
	if Sibling(current) != earlier || Sibling(earlier) != current {
		t.Fatalf("Sibling = %q, %q", Sibling(current), Sibling(earlier))
	}
	// A claim of the same session under it is refused, naming the holder.
	var claimed *ClaimedError
	if _, err := Take(earlier, id, me("example-loop-c")); !errors.As(err, &claimed) || claimed.Holder == nil || claimed.Holder.Loop != "example-loop-a" {
		t.Fatalf("a claim under the other name was taken: %v", err)
	}
	// The refused claim left nothing held: another session is free, and
	// so is this one once the first claim ends.
	other, err := Take(earlier, Identity{Kind: "bb", ID: "thr_other"}, me("example-loop-c"))
	if err != nil {
		t.Fatalf("another session: %v", err)
	}
	other.Release()
	// The records are read from both.
	if err := Note(earlier, id, Supervisor{Loop: "example-loop-c", Config: filepath.Join(home, "c", "keepgoing.yaml"), StateDir: filepath.Join(home, "c", "state"), SeenAt: time.Now()}, 0); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{current, earlier} {
		names := func(list []Supervisor) string {
			var out []string
			for _, s := range list {
				out = append(out, s.Loop)
			}
			sort.Strings(out)
			return strings.Join(out, ",")
		}
		if got := names(Known(dir, id)); got != "example-loop-a,example-loop-c" {
			t.Fatalf("Known(%s) = %s", dir, got)
		}
		recorded, err := Recorded(dir, id)
		if err != nil || names(recorded) != "example-loop-a,example-loop-c" {
			t.Fatalf("Recorded(%s) = %s %v", dir, names(recorded), err)
		}
		if got := names(All(dir, 8)); got != "example-loop-a,example-loop-c" {
			t.Fatalf("All(%s) = %s", dir, got)
		}
	}
	// Each record file holds only what was written there.
	if list := known(earlier, id); len(list) != 1 || list[0].Loop != "example-loop-c" {
		t.Fatalf("the record under the earlier name: %+v", list)
	}

	a.Release()
	c, err := Take(earlier, id, me("example-loop-c"))
	if err != nil {
		t.Fatalf("after the first claim ended: %v", err)
	}
	// And the other way round.
	if _, err := Take(current, id, me("example-loop-a")); !errors.As(err, &claimed) || claimed.Holder == nil || claimed.Holder.Loop != "example-loop-c" {
		t.Fatalf("a claim under the current name was taken while the earlier one is held: %v", err)
	}
	c.Release()

	// A directory a person chose has no other name.
	chosen := filepath.Join(home, "chosen")
	if err := os.MkdirAll(chosen, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := Sibling(chosen); got != "" {
		t.Fatalf("Sibling of a chosen directory = %q", got)
	}
}
