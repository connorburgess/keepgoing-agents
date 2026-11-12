package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

const acpFixtureTurn = "turn-café-项目/1"

func acpHoldLoad(t *testing.T, dir string) AntigravityACPHold {
	t.Helper()
	// A fresh Dir value per read: nothing is cached between "restarts".
	h, ok, err := Dir(dir).AntigravityACPHold()
	if err != nil || !ok {
		t.Fatalf("hold record: ok=%t err=%v", ok, err)
	}
	return h
}

// acpHoldSnapshot returns every file's name and bytes, temporary files
// included, so a sequential test can assert that a call changed nothing.
func acpHoldSnapshot(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			b = []byte("(unreadable)")
		}
		files = append(files, e.Name()+"="+string(b))
	}
	sort.Strings(files)
	return strings.Join(files, "|")
}

// acpHoldRecords lists published records. A temporary file may legitimately
// remain on Windows when a concurrent reader blocked its removal; anywhere
// else, with no process killed, one is a defect.
func acpHoldRecords(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".acp-hold-") {
			if runtime.GOOS != "windows" {
				t.Fatalf("temporary file left behind: %s", e.Name())
			}
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

func acpHoldWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAntigravityACPHoldExchangeSavesBeforeCancellationAndSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "targets", "primary")
	const prompt = "fictional private task text"
	w := &acpExchangeWriter{before: func(p []byte) {
		switch method, _ := acpExchangeFrame(t, p); method {
		case "session/prompt":
			if h := acpHoldLoad(t, dir); !h.PromptAttempted || h.NeedsUser {
				t.Fatalf("prompt written before its reservation was saved: %+v", h)
			}
		case "session/cancel", "permission-cancelled":
			if h := acpHoldLoad(t, dir); !h.NeedsUser || !h.Deferred() {
				t.Fatalf("%s written before the hold evidence was saved: %+v", method, h)
			}
		}
	}}
	input := acpExchangeStream(false, acpPermission(`42`, acpFixtureSession), acpPermission(`"next"`, acpFixtureSession), acpResult("cancelled"))
	out, err := ExchangeAntigravityACP(context.Background(), strings.NewReader(input), w, "", t.TempDir(), prompt, Dir(dir).AntigravityACPHoldCheckpoint(acpFixtureTurn))
	if err != nil || !out.NeedsUser || out.TurnReturned {
		t.Fatalf("outcome %+v %v", out, err)
	}
	want := AntigravityACPHold{Turn: acpFixtureTurn, SessionID: acpFixtureSession, PromptAttempted: true, NeedsUser: true}
	if h := acpHoldLoad(t, dir); h != want {
		t.Fatalf("saved %+v", h)
	}
	saved := acpHoldSnapshot(t, dir)
	for _, private := range []string{prompt, "fictional command", "call-example", "allow", "Deny"} {
		if strings.Contains(saved, private) {
			t.Fatalf("record retains %q", private)
		}
	}
	names := acpHoldRecords(t, dir)
	if len(names) != 2 || names[0] != antigravityACPNeedsUserName || names[1] != antigravityACPReservedName {
		t.Fatalf("unexpected records: %v", names)
	}
	if runtime.GOOS != "windows" {
		for _, name := range names {
			if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v %v", info, err)
			}
		}
	}

	// Restart: a new exchange cannot reserve a second prompt, for the same turn,
	// another turn or another session, and the evidence is not replaced.
	for _, tc := range []struct{ name, turn, session string }{
		{"same_turn", acpFixtureTurn, acpFixtureSession},
		{"other_turn", "turn-2", acpFixtureSession},
		{"other_session", acpFixtureTurn, "session-other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			again := &acpExchangeWriter{}
			stream := strings.ReplaceAll(acpExchangeStream(true, acpResult("end_turn")), acpFixtureSession, tc.session)
			out, err := ExchangeAntigravityACP(context.Background(), strings.NewReader(stream), again, tc.session, t.TempDir(), prompt, Dir(dir).AntigravityACPHoldCheckpoint(tc.turn))
			if err == nil || out.TurnReturned || !out.Failed {
				t.Fatalf("restarted exchange was not refused: %+v %v", out, err)
			}
			for _, frame := range again.frames {
				if method, _ := acpExchangeFrame(t, frame); method == "session/prompt" {
					t.Fatal("prompt replayed after restart")
				}
			}
			if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), tc.session) {
				t.Fatalf("error leaks private detail: %v", err)
			}
			if acpHoldSnapshot(t, dir) != saved {
				t.Fatal("evidence changed")
			}
		})
	}

	// Adoption is bound to the turn, idempotent, kept across restart, and never
	// rewrites the evidence. Later requests in the same turn add nothing.
	if err := Dir(dir).AdoptAntigravityACPHold("turn-2"); err == nil {
		t.Fatal("adopted another turn")
	}
	if acpHoldSnapshot(t, dir) != saved {
		t.Fatal("refused adoption wrote")
	}
	for i := 0; i < 2; i++ {
		if err := Dir(dir).AdoptAntigravityACPHold(acpFixtureTurn); err != nil {
			t.Fatal(err)
		}
	}
	want.Adopted = true
	if h := acpHoldLoad(t, dir); h != want || h.Deferred() {
		t.Fatalf("adopted %+v", h)
	}
	adopted := acpHoldSnapshot(t, dir)
	if !strings.HasSuffix(adopted, saved) || strings.Count(adopted, "|") != 2 {
		t.Fatal("adoption changed the evidence")
	}
	if err := Dir(dir).AntigravityACPHoldCheckpoint(acpFixtureTurn)(context.Background(), AntigravityACPCheckpoint{SessionID: acpFixtureSession, NeedsUser: true}); err != nil {
		t.Fatal(err)
	}
	if acpHoldSnapshot(t, dir) != adopted {
		t.Fatal("repeated evidence rewrote records")
	}
}

func TestAntigravityACPHoldCheckpointOnlyAccumulates(t *testing.T) {
	attempt := AntigravityACPCheckpoint{SessionID: acpFixtureSession, BeforePrompt: true}
	needs := AntigravityACPCheckpoint{SessionID: acpFixtureSession, NeedsUser: true}
	long := strings.Repeat("<", 256) // six bytes each once JSON-escaped
	type step struct {
		turn string
		c    AntigravityACPCheckpoint
		ok   bool
	}
	for _, tc := range []struct {
		name  string
		steps []step
		want  AntigravityACPHold
		none  bool
	}{
		{"attempt", []step{{acpFixtureTurn, attempt, true}}, AntigravityACPHold{PromptAttempted: true}, false},
		{"attempt_then_hold", []step{{acpFixtureTurn, attempt, true}, {acpFixtureTurn, needs, true}, {acpFixtureTurn, needs, true}}, AntigravityACPHold{PromptAttempted: true, NeedsUser: true}, false},
		{"second_attempt_refused", []step{{acpFixtureTurn, attempt, true}, {acpFixtureTurn, attempt, false}}, AntigravityACPHold{PromptAttempted: true}, false},
		{"hold_without_reservation_refused", []step{{acpFixtureTurn, needs, false}, {acpFixtureTurn, attempt, true}}, AntigravityACPHold{PromptAttempted: true}, false},
		{"attempt_after_hold_refused", []step{{acpFixtureTurn, attempt, true}, {acpFixtureTurn, needs, true}, {acpFixtureTurn, attempt, false}}, AntigravityACPHold{PromptAttempted: true, NeedsUser: true}, false},
		{"combined", []step{{acpFixtureTurn, AntigravityACPCheckpoint{SessionID: acpFixtureSession, BeforePrompt: true, NeedsUser: true}, true}}, AntigravityACPHold{PromptAttempted: true, NeedsUser: true}, false},
		{"other_session_refused", []step{{acpFixtureTurn, attempt, true}, {acpFixtureTurn, AntigravityACPCheckpoint{SessionID: "session-other", NeedsUser: true}, false}}, AntigravityACPHold{PromptAttempted: true}, false},
		{"other_turn_refused", []step{{acpFixtureTurn, attempt, true}, {"turn-2", needs, false}, {"turn-2", attempt, false}}, AntigravityACPHold{PromptAttempted: true}, false},
		{"other_turn_after_hold_refused", []step{{acpFixtureTurn, attempt, true}, {acpFixtureTurn, needs, true}, {"turn-2", needs, false}}, AntigravityACPHold{PromptAttempted: true, NeedsUser: true}, false},
		{"no_evidence", []step{{acpFixtureTurn, AntigravityACPCheckpoint{SessionID: acpFixtureSession}, false}}, AntigravityACPHold{}, true},
		{"empty_session", []step{{acpFixtureTurn, AntigravityACPCheckpoint{NeedsUser: true}, false}}, AntigravityACPHold{}, true},
		{"control_session", []step{{acpFixtureTurn, AntigravityACPCheckpoint{SessionID: "session\nother", NeedsUser: true}, false}}, AntigravityACPHold{}, true},
		{"oversized_session", []step{{acpFixtureTurn, AntigravityACPCheckpoint{SessionID: long + "<", NeedsUser: true}, false}}, AntigravityACPHold{}, true},
		{"empty_turn", []step{{"", needs, false}}, AntigravityACPHold{}, true},
		{"control_turn", []step{{"turn\x00", needs, false}}, AntigravityACPHold{}, true},
		{"oversized_turn", []step{{long + "<", needs, false}}, AntigravityACPHold{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "target")
			for i, s := range tc.steps {
				before := acpHoldSnapshot(t, dir)
				err := Dir(dir).AntigravityACPHoldCheckpoint(s.turn)(context.Background(), s.c)
				if (err == nil) != s.ok {
					t.Fatalf("step %d: %v", i, err)
				}
				if !s.ok && acpHoldSnapshot(t, dir) != before {
					t.Fatalf("step %d: refused checkpoint wrote", i)
				}
			}
			h, ok, err := Dir(dir).AntigravityACPHold()
			if err != nil || ok == tc.none {
				t.Fatalf("ok=%t err=%v", ok, err)
			}
			if tc.none {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("refused checkpoint created runtime files")
				}
				return
			}
			tc.want.Turn, tc.want.SessionID = acpFixtureTurn, acpFixtureSession
			if h != tc.want {
				t.Fatalf("saved %+v", h)
			}
		})
	}

	t.Run("largest_identifiers_round_trip", func(t *testing.T) {
		dir := t.TempDir()
		c := AntigravityACPCheckpoint{SessionID: long, BeforePrompt: true, NeedsUser: true}
		if err := Dir(dir).AntigravityACPHoldCheckpoint(long)(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if err := Dir(dir).AdoptAntigravityACPHold(long); err != nil {
			t.Fatal(err)
		}
		if h := acpHoldLoad(t, dir); h.Turn != long || h.SessionID != long || !h.PromptAttempted || !h.Adopted {
			t.Fatal("bounded identifiers did not round trip")
		}
	})

	t.Run("cancelled_context", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "target")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Dir(dir).AntigravityACPHoldCheckpoint(acpFixtureTurn)(ctx, needs); err == nil {
			t.Fatal("cancelled checkpoint reported saved")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("cancelled checkpoint wrote")
		}
	})

	t.Run("adoption_needs_matching_evidence", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "target")
		if err := Dir(dir).AdoptAntigravityACPHold(acpFixtureTurn); err == nil {
			t.Fatal("adopted without a record")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("refused adoption wrote")
		}
		if err := Dir(dir).AntigravityACPHoldCheckpoint(acpFixtureTurn)(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
		// A reservation alone is not human-input evidence and cannot be pre-cleared.
		if err := Dir(dir).AdoptAntigravityACPHold(acpFixtureTurn); err == nil {
			t.Fatal("adopted a prompt reservation")
		}
		if err := Dir(dir).AntigravityACPHoldCheckpoint(acpFixtureTurn)(context.Background(), needs); err != nil {
			t.Fatal(err)
		}
		if h := acpHoldLoad(t, dir); !h.Deferred() {
			t.Fatalf("later evidence was pre-cleared: %+v", h)
		}
	})

	t.Run("unwritable_target_fails_without_private_detail", func(t *testing.T) {
		// The target path is a file, so no record can be created under it.
		dir := filepath.Join(t.TempDir(), "fictional-private-target")
		if err := os.WriteFile(dir, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, c := range []AntigravityACPCheckpoint{attempt, needs} {
			err := Dir(dir).AntigravityACPHoldCheckpoint(acpFixtureTurn)(context.Background(), c)
			if err == nil {
				t.Fatal("unsaved checkpoint reported saved")
			}
			if strings.Contains(err.Error(), "fictional-private-target") || strings.Contains(err.Error(), acpFixtureTurn) || strings.Contains(err.Error(), acpFixtureSession) {
				t.Fatalf("error leaks private detail: %v", err)
			}
		}
	})
}

// A restarted worker may overlap one that is still alive. Exactly one exchange
// may be told to write a prompt, however the checkpoints interleave.
func TestAntigravityACPHoldConcurrentReservationsAdmitOnePrompt(t *testing.T) {
	for round := 0; round < 20; round++ {
		dir := filepath.Join(t.TempDir(), "target")
		const workers = 8
		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make([]error, workers)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results[i] = Dir(dir).AntigravityACPHoldCheckpoint(acpFixtureTurn)(context.Background(), AntigravityACPCheckpoint{SessionID: acpFixtureSession, BeforePrompt: true})
			}()
		}
		close(start)
		wg.Wait()
		admitted := 0
		for _, err := range results {
			if err == nil {
				admitted++
			}
		}
		if admitted != 1 {
			t.Fatalf("round %d: %d exchanges reserved a prompt", round, admitted)
		}
		if h := acpHoldLoad(t, dir); !h.PromptAttempted || h.NeedsUser {
			t.Fatalf("%+v", h)
		}
		if names := acpHoldRecords(t, dir); len(names) != 1 || names[0] != antigravityACPReservedName {
			t.Fatalf("records: %v", names)
		}
	}
}

// Human-input evidence for one identity racing a reservation for another must
// not leave a pair of records that disagree, which nothing could later repair.
func TestAntigravityACPHoldEvidenceRequiresItsOwnReservation(t *testing.T) {
	for round := 0; round < 50; round++ {
		dir := filepath.Join(t.TempDir(), "target")
		var wg sync.WaitGroup
		start := make(chan struct{})
		var needsErr, reserveErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			needsErr = Dir(dir).AntigravityACPHoldCheckpoint("turn-a")(context.Background(), AntigravityACPCheckpoint{SessionID: "session-a", NeedsUser: true})
		}()
		go func() {
			defer wg.Done()
			<-start
			reserveErr = Dir(dir).AntigravityACPHoldCheckpoint("turn-b")(context.Background(), AntigravityACPCheckpoint{SessionID: "session-b", BeforePrompt: true})
		}()
		close(start)
		wg.Wait()
		if needsErr == nil || reserveErr != nil {
			t.Fatalf("round %d: needs=%v reserve=%v", round, needsErr, reserveErr)
		}
		want := AntigravityACPHold{Turn: "turn-b", SessionID: "session-b", PromptAttempted: true}
		if h := acpHoldLoad(t, dir); h != want {
			t.Fatalf("round %d: %+v", round, h)
		}
		if names := acpHoldRecords(t, dir); len(names) != 1 {
			t.Fatalf("round %d: records %v", round, names)
		}
	}
}

// A supervisor reading the records must never make the worker's checkpoint
// fail. Records are never replaced, so open readers do not block a save; this
// runs natively on Windows, where an open handle blocks replacement.
func TestAntigravityACPHoldOpenReadersDoNotBlockEvidence(t *testing.T) {
	dir := t.TempDir()
	checkpoint := Dir(dir).AntigravityACPHoldCheckpoint(acpFixtureTurn)
	if err := checkpoint(context.Background(), AntigravityACPCheckpoint{SessionID: acpFixtureSession, BeforePrompt: true}); err != nil {
		t.Fatal(err)
	}
	hold := func(name string) {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
	}
	hold(antigravityACPReservedName)
	if err := checkpoint(context.Background(), AntigravityACPCheckpoint{SessionID: acpFixtureSession, NeedsUser: true}); err != nil {
		t.Fatalf("open reader blocked the hold evidence: %v", err)
	}
	hold(antigravityACPNeedsUserName)
	if err := checkpoint(context.Background(), AntigravityACPCheckpoint{SessionID: acpFixtureSession, NeedsUser: true}); err != nil {
		t.Fatal(err)
	}
	if err := Dir(dir).AdoptAntigravityACPHold(acpFixtureTurn); err != nil {
		t.Fatalf("open reader blocked adoption: %v", err)
	}
	hold(antigravityACPAdoptedName)
	if err := Dir(dir).AdoptAntigravityACPHold(acpFixtureTurn); err != nil {
		t.Fatal(err)
	}
	want := AntigravityACPHold{Turn: acpFixtureTurn, SessionID: acpFixtureSession, PromptAttempted: true, NeedsUser: true, Adopted: true}
	if h := acpHoldLoad(t, dir); h != want {
		t.Fatalf("%+v", h)
	}
}

func TestAntigravityACPHoldUnreadableRecordsFailClosed(t *testing.T) {
	const valid = `{"version":1,"turn":"turn-1","sessionId":"session-example"}`
	const secret = "fictional-private-record-detail"
	needs := AntigravityACPCheckpoint{SessionID: "session-example", NeedsUser: true}
	attempt := AntigravityACPCheckpoint{SessionID: "session-example", BeforePrompt: true}
	other := func(key, value string) string { return strings.Replace(valid, key, value, 1) }
	malformed := []struct{ name, content string }{
		{"truncated", `{"version":1,"turn":"turn-1"`},
		{"empty", ""},
		{"not_object", `[1]`},
		{"unknown_version", other(`"version":1`, `"version":2`)},
		{"string_version", other(`"version":1`, `"version":"1"`)},
		{"missing_key", `{"version":1,"turn":"turn-1"}`},
		{"extra_key", other(`}`, `,"note":"`+secret+`"}`)},
		{"duplicate_key", `{"version":1,"turn":"turn-0","turn":"turn-1","sessionId":"session-example"}`},
		{"non_string", other(`"turn-1"`, `1`)},
		{"empty_turn", other(`"turn-1"`, `""`)},
		{"control_session", other(`session-example`, `session\u0000`+secret)},
		{"lossy_escape", other(`session-example`, `\ud800`)},
		{"trailing_data", valid + "\n" + valid},
		{"oversized", valid + strings.Repeat(" ", antigravityACPHoldLimit)},
	}
	// Every record is read on every operation, so one unreadable file blocks
	// all of them, whichever file it is.
	for _, name := range []string{antigravityACPReservedName, antigravityACPNeedsUserName, antigravityACPAdoptedName} {
		for _, tc := range malformed {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				files := map[string]string{antigravityACPReservedName: valid, antigravityACPNeedsUserName: valid, antigravityACPAdoptedName: valid}
				files[name] = tc.content
				acpHoldWrite(t, dir, files)
				before := acpHoldSnapshot(t, dir)
				check := func(err error) {
					t.Helper()
					if err == nil {
						t.Fatal("unreadable record accepted")
					}
					if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "turn-1") || strings.Contains(err.Error(), dir) {
						t.Fatalf("error echoes the record: %v", err)
					}
				}
				h, ok, err := Dir(dir).AntigravityACPHold()
				check(err)
				if ok || h != (AntigravityACPHold{}) {
					t.Fatalf("partial record returned: %+v", h)
				}
				check(Dir(dir).AdoptAntigravityACPHold("turn-1"))
				check(Dir(dir).AntigravityACPHoldCheckpoint("turn-1")(context.Background(), needs))
				check(Dir(dir).AntigravityACPHoldCheckpoint("turn-1")(context.Background(), attempt))
				if acpHoldSnapshot(t, dir) != before {
					t.Fatal("unreadable evidence was replaced or extended")
				}
			})
		}
	}

	// Readable files that disagree, evidence without its reservation, or an
	// adoption without the evidence it names are never trusted, extended or
	// repaired. The checkpoint applies the same rule as the reader, so no frame
	// is released on top of records the supervisor could not read.
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"evidence_turns_differ", map[string]string{antigravityACPReservedName: valid, antigravityACPNeedsUserName: other("turn-1", "turn-0")}},
		{"evidence_sessions_differ", map[string]string{antigravityACPReservedName: valid, antigravityACPNeedsUserName: other("session-example", "session-other")}},
		{"evidence_without_reservation", map[string]string{antigravityACPNeedsUserName: valid}},
		{"adopted_evidence_without_reservation", map[string]string{antigravityACPNeedsUserName: valid, antigravityACPAdoptedName: valid}},
		{"adoption_names_another_turn", map[string]string{antigravityACPReservedName: valid, antigravityACPNeedsUserName: valid, antigravityACPAdoptedName: other("turn-1", "turn-0")}},
		{"adoption_names_another_session", map[string]string{antigravityACPReservedName: valid, antigravityACPNeedsUserName: valid, antigravityACPAdoptedName: other("session-example", "session-other")}},
		{"adoption_beside_reservation_only", map[string]string{antigravityACPReservedName: valid, antigravityACPAdoptedName: valid}},
		{"adoption_alone", map[string]string{antigravityACPAdoptedName: valid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			acpHoldWrite(t, dir, tc.files)
			before := acpHoldSnapshot(t, dir)
			if h, ok, err := Dir(dir).AntigravityACPHold(); err == nil || ok {
				t.Fatalf("inconsistent records accepted: %+v", h)
			}
			if err := Dir(dir).AdoptAntigravityACPHold("turn-1"); err == nil {
				t.Fatal("adopted inconsistent records")
			}
			for _, turn := range []string{"turn-1", "turn-0"} {
				for _, c := range []AntigravityACPCheckpoint{needs, attempt, {SessionID: "session-other", NeedsUser: true}} {
					if err := Dir(dir).AntigravityACPHoldCheckpoint(turn)(context.Background(), c); err == nil {
						t.Fatalf("checkpoint %s %+v accepted inconsistent records", turn, c)
					}
				}
			}
			if acpHoldSnapshot(t, dir) != before {
				t.Fatal("inconsistent records were changed")
			}
		})
	}

	t.Run("not_a_regular_file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, antigravityACPNeedsUserName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := Dir(dir).AntigravityACPHold(); err == nil || ok {
			t.Fatal("directory accepted as a record")
		}
		if err := Dir(dir).AntigravityACPHoldCheckpoint("turn-1")(context.Background(), needs); err == nil {
			t.Fatal("checkpoint accepted a non-file")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		elsewhere := filepath.Join(t.TempDir(), "record")
		if err := os.WriteFile(elsewhere, []byte(valid), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(dir, antigravityACPNeedsUserName)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, ok, err := Dir(dir).AntigravityACPHold(); err == nil || ok {
			t.Fatal("symlinked record accepted")
		}
	})
}
