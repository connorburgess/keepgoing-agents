package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
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
	// Without the override it is under the user's state directory.
	t.Setenv(DirEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	dir, _, err = Dir(true)
	want := filepath.Join(home, "state", "keepgoing", "sessions")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, "Library", "Application Support", "keepgoing", "sessions")
	}
	if err != nil || dir != want {
		t.Fatalf("%s %v, want %s", dir, err, want)
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
