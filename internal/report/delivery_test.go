package report

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func recordedWebhook(t *testing.T, dir, url string) recorded {
	t.Helper()
	t.Setenv("KG_TEST_URL", url)
	t.Setenv("KG_TEST_SECRET", secret)
	r, err := NewRecorded(cfg(""), dir)
	if err != nil {
		t.Fatal(err)
	}
	return r.(recorded)
}

func history(t *testing.T, dir string) DeliveryHistory {
	t.Helper()
	h, err := LoadDeliveries(dir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDeliveryOutcomesAndExistingAttemptPolicy(t *testing.T) {
	for _, event := range []string{"await_user", "complete"} {
		for _, code := range []int{202, 400, 503, 307} {
			t.Run(event+http.StatusText(code), func(t *testing.T) {
				dir := t.TempDir()
				var calls, redirects atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); w.WriteHeader(202) }))
				defer target.Close()
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					// Before a request reaches the receiver, its intent is already durable.
					h, err := LoadDeliveries(dir)
					if err != nil || len(h.Records) != 1 || h.Records[0].Outcome != "attempt_started" {
						t.Errorf("before response: %+v %v", h, err)
					}
					w.Header().Set("Location", target.URL)
					w.WriteHeader(code)
				}))
				defer srv.Close()
				r := recordedWebhook(t, dir, srv.URL)
				err := r.Report(context.Background(), Event{Type: event, Text: "fictional-private-text", Loop: "private-loop", Data: map[string]any{"note": "private-note"}})
				accepted := code == 202 || (code == 307 && event != "complete")
				if (err == nil) != accepted {
					t.Fatalf("err=%v", err)
				}
				attempts := 1
				if code == 503 && event != "complete" {
					attempts = 2
				}
				d := history(t, dir).Records[0]
				if int(calls.Load()) != attempts || len(d.Attempts) != attempts || d.Receipt != "unverified" {
					t.Fatalf("%+v calls=%d", d, calls.Load())
				}
				want := "http_rejected"
				if accepted {
					want = "http_accepted"
				}
				if d.Outcome != want {
					t.Fatal(d)
				}
				if event == "complete" && redirects.Load() != 0 {
					t.Fatal("completion redirected")
				}
				b, _ := os.ReadFile(filepath.Join(dir, DeliveryFile))
				for _, private := range []string{srv.URL, secret, "fictional-private-text", "private-loop", "private-note", "authorization", "webhook-id"} {
					if strings.Contains(string(b), private) {
						t.Fatalf("private content persisted: %s", private)
					}
				}
			})
		}
	}
}

func TestDeliveryTransportPreparationAndSetupFailures(t *testing.T) {
	for _, event := range []string{"await_user", "complete"} {
		t.Run(event, func(t *testing.T) {
			dir := t.TempDir()
			r := recordedWebhook(t, dir, "https://example.com/hook")
			tr := &failedTransport{}
			r.webhook.client = &http.Client{Transport: tr}
			if err := r.Report(context.Background(), Event{Type: event}); err == nil {
				t.Fatal("missing failure")
			}
			d := history(t, dir).Records[0]
			want := 2
			if event == "complete" {
				want = 1
			}
			if d.Outcome != "transport_failed" || len(d.Attempts) != want || int(tr.calls.Load()) != want {
				t.Fatal(d)
			}
		})
	}
	dir := t.TempDir()
	r := recordedWebhook(t, dir, "https://example.com/hook")
	if err := r.Report(context.Background(), Event{Type: "complete", Data: map[string]any{"bad": make(chan int)}}); err == nil {
		t.Fatal("bad body accepted")
	}
	if d := history(t, dir).Records[0]; d.Outcome != "body_failed" || len(d.Attempts) != 0 {
		t.Fatal(d)
	}
	t.Setenv("KG_TEST_SECRET", "")
	bad, err := NewRecorded(cfg(""), dir)
	if err == nil {
		t.Fatal("missing setup error")
	}
	if err = bad.Report(context.Background(), Event{Type: "await_user"}); err == nil {
		t.Fatal("missing report failure")
	}
	if d := history(t, dir).Records[1]; d.Outcome != "setup_failed" || len(d.Attempts) != 0 {
		t.Fatal(d)
	}
	// A filtered event must not be misrepresented as a successful delivery.
	if err = bad.Report(context.Background(), Event{Type: "wake"}); err != nil {
		t.Fatal(err)
	}
	if len(history(t, dir).Records) != 2 {
		t.Fatal("filtered event recorded")
	}
}

func TestDeliveryHistoryFailureNeverSuppressesOrRetriesSend(t *testing.T) {
	for _, failure := range []string{"corrupt", "locked", "finish_locked"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			var calls atomic.Int32
			var held *lock.Lock
			defer func() {
				if held != nil {
					held.Release()
				}
			}()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if failure == "finish_locked" {
					var err error
					held, err = lock.AcquireNamed(dir, "delivery")
					if err != nil {
						t.Error(err)
					}
				}
				w.WriteHeader(202)
			}))
			defer srv.Close()
			if failure == "corrupt" {
				os.WriteFile(filepath.Join(dir, DeliveryFile), []byte("bad"), 0600)
			}
			if failure == "locked" {
				var err error
				held, err = lock.AcquireNamed(dir, "delivery")
				if err != nil {
					t.Fatal(err)
				}
			}
			r := recordedWebhook(t, dir, srv.URL)
			if err := r.Report(context.Background(), Event{Type: "complete"}); !errors.Is(err, ErrDeliveryHistory) {
				t.Fatalf("error=%v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("changed single attempt policy")
			}
			if failure == "finish_locked" {
				// Simulates losing the final diagnostic write after HTTP acceptance.
				// A fresh reader sees unconfirmed intent, never a fabricated success.
				s := InspectDeliveries(dir)
				if len(s.Warnings) != 1 || s.Warnings[0].Outcome != "attempt_started" {
					t.Fatal(s)
				}
			}
		})
	}
}

func TestDeliveryHistoryRetentionValidationAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	if s := InspectDeliveries(dir); s.State != "not_recorded" {
		t.Fatal(s)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("read created files")
	}
	now := time.Now().UTC()
	d := Delivery{Event: "await_user", StartedAt: now, UpdatedAt: now, Outcome: "prepared", Receipt: "unverified", Attempts: []Attempt{}}
	for i := 0; i < MaxDeliveries+2; i++ {
		if _, err := saveDelivery(dir, d); err != nil {
			t.Fatal(err)
		}
	}
	h := history(t, dir)
	if h.Dropped != 2 || len(h.Records) != MaxDeliveries || h.Records[0].Sequence != 3 {
		t.Fatal(h)
	}
	s := InspectDeliveries(dir)
	if len(s.Warnings) != MaxDeliveries || s.Dropped != 2 {
		t.Fatal(s)
	}
	if !reflect.DeepEqual(h, history(t, dir)) {
		t.Fatal("read changed history")
	}
	if info, err := os.Stat(filepath.Join(dir, DeliveryFile)); err != nil || info.Size() > MaxDeliveryBytes || (info.Mode().Perm() != 0600 && os.PathSeparator == '/') {
		t.Fatalf("stat=%v err=%v", info, err)
	}
	// Finishing an evicted record cannot overwrite a newer attempt.
	d.Sequence = 1
	if _, err := saveDelivery(dir, d); err == nil {
		t.Fatal("updated evicted record")
	}
	good, _ := json.Marshal(h)
	for _, bad := range []string{"{}", string(good) + " {}", strings.Replace(string(good), `"schemaVersion":1`, `"schemaVersion":2`, 1), strings.Replace(string(good), `"await_user"`, `"private-event"`, 1), strings.Repeat(" ", MaxDeliveryBytes+1)} {
		os.WriteFile(filepath.Join(dir, DeliveryFile), []byte(bad), 0600)
		if s := InspectDeliveries(dir); s.State != "unavailable" {
			t.Fatal(s)
		}
		if _, err := saveDelivery(dir, d); err == nil {
			t.Fatal("corrupt history reset")
		}
	}
}

func TestDeliveryHistoryConcurrentReportsKeepSeparateRecords(t *testing.T) {
	dir := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(202) }))
	defer srv.Close()
	r := recordedWebhook(t, dir, srv.URL)
	done := make(chan error, 1)
	go func() { done <- r.Report(context.Background(), Event{Type: "complete"}) }()
	<-entered
	// The diagnostic lock must be released during HTTP. Another declaration can
	// write its own record without corrupting the first pending record.
	now := time.Now().UTC()
	_, err := saveDelivery(dir, Delivery{Event: "stop", StartedAt: now, UpdatedAt: now, Outcome: "setup_failed", Receipt: "unverified", Attempts: []Attempt{}})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	h := history(t, dir)
	if len(h.Records) != 2 || h.Records[0].Outcome != "http_accepted" || h.Records[1].Outcome != "setup_failed" {
		t.Fatal(h)
	}
}

func TestDeliveryHistoryRefusesAliasedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(target, []byte(`{"schemaVersion":1,"nextSequence":1,"dropped":0,"records":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, DeliveryFile)); err != nil {
		t.Skip("symlinks unavailable")
	}
	if s := InspectDeliveries(dir); s.State != "unavailable" {
		t.Fatal(s)
	}
}

// A report the caller decided not to send, for lack of time in a check, is
// kept beside the deliveries with no request and no attempt, and shows as a
// warning. Events the loop does not subscribe to leave no record.
func TestDroppedReportIsRecordedWithoutARequest(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	r := recordedWebhook(t, dir, srv.URL)
	d, ok := Reporter(r).(Dropper)
	if !ok {
		t.Fatal("the recording reporter cannot note a dropped report")
	}
	if err := d.Dropped(Event{Type: "await_user", Loop: "example-loop", Text: "private text"}); err != nil {
		t.Fatal(err)
	}
	h := history(t, dir)
	if len(h.Records) != 1 || h.Records[0].Outcome != "not_sent" || h.Records[0].Event != "await_user" || len(h.Records[0].Attempts) != 0 || calls.Load() != 0 {
		t.Fatalf("%+v calls=%d", h.Records, calls.Load())
	}
	if s := InspectDeliveries(dir); len(s.Warnings) != 1 || s.Warnings[0].Outcome != "not_sent" {
		t.Fatalf("%+v", s)
	}
	b, _ := os.ReadFile(filepath.Join(dir, DeliveryFile))
	if strings.Contains(string(b), "private text") {
		t.Fatal("delivery record kept report text")
	}
	if err := d.Dropped(Event{Type: "wake"}); err != nil || len(history(t, dir).Records) != 1 {
		t.Fatalf("an unsubscribed event was recorded: %v", err)
	}
}
