package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Known gap, listed in docs/adapters.md: after a reboot or a wrapper killed
// without writing its exit code, a turn record is judged by whether its saved
// PID exists. This pins both outcomes so a fix has to change the test.
func TestTurnRecordAfterTheWrapperIsGone(t *testing.T) {
	record := func(pid int) Dir {
		dir := Dir(t.TempDir())
		b, _ := json.Marshal(Record{PID: pid, StartedAt: time.Now().Add(-21 * 24 * time.Hour), Argv0: "stand-in"})
		if err := os.WriteFile(filepath.Join(string(dir), "run.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	// The PID is gone: the turn is reported as crashed, which a check treats
	// as an unknown error and re-prompts.
	if st, err := record(0).Status(); err != nil || st.Phase != Crashed {
		t.Fatalf("%+v %v", st, err)
	}
	// The PID now belongs to an unrelated live process (here, this test): the
	// three-week-old turn is reported as still running. A check then sees an
	// active session and sends nothing, for as long as that process lives.
	if st, err := record(os.Getpid()).Status(); err != nil || st.Phase != Running {
		t.Fatalf("a reused PID is no longer read as a running turn; update docs/adapters.md and this test: %+v %v", st, err)
	}
}
