package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSendLedgerPersistenceAndDisabledAccounting(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if err := ReserveSend(dir, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := ReserveSend(dir, 2); !errors.Is(err, ErrSendLimit) {
		t.Fatal(err)
	}
	if err := ReserveSend(dir, 0); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, File{}); err != nil {
		t.Fatal(err)
	}
	used, err := LoadSends(dir)
	if err != nil || used != 2 {
		t.Fatalf("%d %v", used, err)
	}
	if err := ReserveSend(dir, 3); err != nil {
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
		if err := ReserveSend(dir, 5); !errors.Is(err, ErrSendLedger) {
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
	if err := ReserveSend(dir, 9223372036854775807); !errors.Is(err, ErrSendLimit) {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ReserveSend(blocked, 1); !errors.Is(err, ErrSendLedger) {
		t.Fatal(err)
	}
}

// A refund returns exactly one reservation, never goes below zero, and does
// nothing when accounting is off or the ledger cannot be trusted.
func TestRefundSend(t *testing.T) {
	dir := t.TempDir()
	if err := RefundSend(dir, 3); err != nil {
		t.Fatal(err)
	}
	if used, err := LoadSends(dir); err != nil || used != 0 {
		t.Fatal(used, err)
	}
	for i := 0; i < 2; i++ {
		if err := ReserveSend(dir, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := RefundSend(dir, 3); err != nil {
		t.Fatal(err)
	}
	if used, err := LoadSends(dir); err != nil || used != 1 {
		t.Fatal(used, err)
	}
	// With accounting off an earlier ledger is left alone.
	if err := RefundSend(dir, 0); err != nil {
		t.Fatal(err)
	}
	if used, _ := LoadSends(dir); used != 1 {
		t.Fatal(used)
	}
	if err := RefundSend(dir, 3); err != nil {
		t.Fatal(err)
	}
	if err := RefundSend(dir, 3); err != nil {
		t.Fatal(err)
	}
	if used, err := LoadSends(dir); err != nil || used != 0 {
		t.Fatal(used, err)
	}
	if err := os.WriteFile(filepath.Join(dir, SendsName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RefundSend(dir, 3); !errors.Is(err, ErrSendLedger) {
		t.Fatal(err)
	}
}
