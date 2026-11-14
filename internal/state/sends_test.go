package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSendLedgerPersistenceAndDisabledAccounting(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if err := reserve(dir, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := reserve(dir, 2); !errors.Is(err, ErrSendLimit) {
		t.Fatal(err)
	}
	if err := reserve(dir, 0); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, File{}); err != nil {
		t.Fatal(err)
	}
	used, err := LoadSends(dir)
	if err != nil || used != 2 {
		t.Fatalf("%d %v", used, err)
	}
	if err := reserve(dir, 3); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, SendsName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestSendLedgerRejectsCorruption(t *testing.T) {
	for _, data := range []string{"", "{}", "null", `{"version":1}`, `{"version":2,"used":0}`, `{"version":1,"used":-1}`, `{"version":1,"used":1.5}`, `{"version":1,"used":9223372036854775808}`, `{"version":1,"used":1}trailing`} {
		dir := t.TempDir()
		path := filepath.Join(dir, SendsName)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSends(dir); !errors.Is(err, ErrSendLedger) {
			t.Fatalf("accepted %q", data)
		}
		if err := reserve(dir, 5); !errors.Is(err, ErrSendLedger) {
			t.Fatalf("reserved %q", data)
		}
		after, _ := os.ReadFile(path)
		if string(after) != data {
			t.Fatal("rewrote corrupt ledger")
		}
	}
}
func TestSendLedgerWriteFailureAndOverflow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SendsName), []byte(`{"version":1,"used":9223372036854775807}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reserve(dir, 9223372036854775807); !errors.Is(err, ErrSendLimit) {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := reserve(blocked, 1); !errors.Is(err, ErrSendLedger) && !errors.Is(err, ErrSendRecord) {
		t.Fatal(err)
	}
}

// A refund returns exactly one reservation, never goes below zero, and does
// nothing when accounting is off or the ledger cannot be trusted.
func TestRefundSend(t *testing.T) {
	dir := t.TempDir()
	if err := RefundSend(dir, 3, 0); err != nil {
		t.Fatal(err)
	}
	if used, err := LoadSends(dir); err != nil || used != 0 {
		t.Fatal(used, err)
	}
	for i := 0; i < 2; i++ {
		if err := reserve(dir, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := RefundSend(dir, 3, 0); err != nil {
		t.Fatal(err)
	}
	if used, err := LoadSends(dir); err != nil || used != 1 {
		t.Fatal(used, err)
	}
	// With accounting off an earlier ledger is left alone.
	if err := RefundSend(dir, 0, 0); err != nil {
		t.Fatal(err)
	}
	if used, _ := LoadSends(dir); used != 1 {
		t.Fatal(used)
	}
	if err := RefundSend(dir, 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := RefundSend(dir, 3, 0); err != nil {
		t.Fatal(err)
	}
	if used, err := LoadSends(dir); err != nil || used != 0 {
		t.Fatal(used, err)
	}
	if err := os.WriteFile(filepath.Join(dir, SendsName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RefundSend(dir, 3, 0); !errors.Is(err, ErrSendLedger) {
		t.Fatal(err)
	}
}

// Every reservation saves the intended send, with or without a limit. With
// no limit nothing is counted against one. Intents whose outcome state.json
// has recorded are dropped by the next reservation; the others are returned
// as unrecorded. A refund drops its own intent.
func TestReserveRecordsTheIntentWithOrWithoutALimit(t *testing.T) {
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, max := range []int64{0, 5} {
		dir := t.TempDir()
		first, err := Reserve(dir, max, 0, SendIntent{At: at, Action: "reprompt", Target: "primary"})
		if err != nil || first != 1 {
			t.Fatal(first, err)
		}
		second, err := Reserve(dir, max, 0, SendIntent{At: at.Add(time.Minute), Action: "child_notice", Target: "primary"})
		if err != nil || second != 2 {
			t.Fatal(second, err)
		}
		l, err := LoadLedger(dir)
		want := int64(0)
		if max > 0 {
			want = 2
		}
		if err != nil || *l.Used != want || len(l.Unrecorded(0)) != 2 || len(l.Unrecorded(1)) != 1 || l.Unrecorded(1)[0].Action != "child_notice" {
			t.Fatalf("max %d: %+v %v", max, l, err)
		}
		// State recorded both; the next reservation drops them.
		if _, err := Reserve(dir, max, 2, SendIntent{At: at.Add(time.Hour), Action: "wake", Target: "primary"}); err != nil {
			t.Fatal(err)
		}
		if l, _ = LoadLedger(dir); len(l.Pending) != 1 || l.Pending[0].Seq != 3 {
			t.Fatalf("%+v", l.Pending)
		}
		if err := RefundSend(dir, max, 3); err != nil {
			t.Fatal(err)
		}
		if l, _ = LoadLedger(dir); len(l.Pending) != 0 || *l.Used != want {
			t.Fatalf("max %d after refund: %+v", max, l)
		}
	}
}

// A reservation that cannot be saved is refused, limit or no limit, so the
// caller sends nothing.
func TestReserveFailsWhenTheRecordCannotBeSaved(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user cannot write")
	}
	for _, max := range []int64{0, 5} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		_, err := Reserve(dir, max, 0, SendIntent{Action: "reprompt"})
		os.Chmod(dir, 0o700)
		if !errors.Is(err, ErrSendRecord) || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("max %d: %v", max, err)
		}
	}
}

// An older ledger, which has no intents, still loads.
func TestLedgerWithoutIntentsLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SendsName), []byte(`{"version":1,"used":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := LoadLedger(dir)
	if err != nil || *l.Used != 4 || l.Seq != 0 || len(l.Unrecorded(0)) != 0 {
		t.Fatal(l, err)
	}
}

// reserve records a blank intent, as the removed ReserveSend did.
func reserve(dir string, max int64) error {
	_, err := Reserve(dir, max, 0, SendIntent{})
	return err
}
