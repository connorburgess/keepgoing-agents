//go:build unix

package supervise

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter/factory"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/testexec"
)

// On a generic process loop the context goes on the resume command's
// standard input, after the prompt, with no wake header: the stand-in's
// resume command records exactly what it read. It is not repeated while
// unchanged, and goes again after a resume. looptender cannot see which
// conversation the command reaches, so nothing else sends it again.
func TestPromptContextOnAProcessStandIn(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptContext = true
	bin := t.TempDir()
	got := filepath.Join(bin, "stdin.log")
	status := filepath.Join(bin, "status.sh")
	resume := filepath.Join(bin, "resume.sh")
	testexec.Script(t, status, "#!/bin/sh\necho '{\"state\":\"idle\"}'\n")
	testexec.Script(t, resume, "#!/bin/sh\ncat >> \""+got+"\"\nprintf '\\n<<end of send>>\\n' >> \""+got+"\"\n")
	w.cfg.Loop.Agent = config.Agent{Adapter: "process", Process: &config.Process{StatusCommand: []string{status}, ResumeCommand: []string{resume}}}
	sends := func() []string {
		b, _ := os.ReadFile(got)
		s := strings.Split(string(b), "\n<<end of send>>\n")
		return s[:len(s)-1]
	}
	wake := func(at time.Duration) string {
		t.Helper()
		o := w.opts(t0.Add(at))
		o.NewAdapter = factory.New
		before := len(sends())
		res, err := Tick(context.Background(), w.cfg, o)
		if err != nil || res.Action != "wake" || len(sends()) != before+1 {
			t.Fatalf("at %v: %+v %v", at, res, err)
		}
		return sends()[before]
	}

	first := wake(0)
	if !strings.HasPrefix(first, "keep going\n\n"+contextTag+"\n") || strings.Count(first, contextTag) != 1 {
		t.Fatalf("first send: %q", first)
	}
	if !strings.Contains(first, "\nadapter:   process\n") {
		t.Fatalf("not this loop's context: %q", first)
	}
	if again := wake(2 * time.Hour); again != "keep going" {
		t.Fatalf("repeated: %q", again)
	}
	for _, paused := range []bool{true, false} {
		if _, err := control.Update(w.dir, func(f *control.File) { f.Paused = paused }); err != nil {
			t.Fatal(err)
		}
	}
	if resumed := wake(4 * time.Hour); !strings.Contains(resumed, contextTag) {
		t.Fatalf("not sent after a resume: %q", resumed)
	}
}
