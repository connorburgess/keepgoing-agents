package report

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/lock"
)

// Fictional receiver uses the published HTTP.md discovery/202 examples. No
// external service, real prompts, accounts or Sessionpipe installation is used.
func pipeReceiver(t *testing.T, ack string, receive func(*http.Request, pipeBatch)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/sessionpipe" {
			if r.Header.Get("Authorization") != "" {
				t.Error("discovery leaked token")
			}
			w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"},"batch":{"max_events":2,"max_bytes":262144}}`))
			return
		}
		if r.Method != "POST" || r.URL.Path != "/events" || r.Header.Get("Authorization") != "Bearer fictional-token" || r.Header.Get("sessionpipe-protocol") != "1" {
			t.Error("bad transport contract")
		}
		var batch pipeBatch
		if json.NewDecoder(r.Body).Decode(&batch) != nil {
			t.Error("bad batch")
		}
		if receive != nil {
			receive(r, batch)
		}
		w.WriteHeader(202)
		w.Write([]byte(ack))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const pipeAck = `{"accepted":2,"duplicates":0,"rejected":[],"max_tier":0}`

func pipeConfig(t *testing.T, endpoint string) config.Report {
	t.Helper()
	t.Setenv("KG_TEST_URL", endpoint)
	t.Setenv("KG_TEST_SECRET", "fictional-token")
	c := cfg("")
	c.Format = "sessionpipe"
	c.Scheme = "bearer"
	c.Events = []string{"all"}
	return c
}

func TestSessionpipeRestartPrivacyAndEvidence(t *testing.T) {
	var batches []pipeBatch
	srv := pipeReceiver(t, pipeAck, func(_ *http.Request, b pipeBatch) { batches = append(batches, b) })
	c := pipeConfig(t, srv.URL)
	dir := t.TempDir()
	for _, event := range []string{"await_user", "complete", "limit_wait"} {
		r, err := NewRecorded(c, dir)
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Report(context.Background(), Event{Type: event, Loop: "private-loop", Text: "private-text", Data: map[string]any{"path": "private-path"}}); err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for i, b := range batches {
		if len(b.Events) != 2 {
			t.Fatal(b)
		}
		if i == 0 {
			id = b.Events[0].Session["id"].(string)
		}
		for j, e := range b.Events {
			if e.Protocol != 1 || e.Tier != 0 || e.Harness["name"] != "looptender" || e.Session["id"] != id || e.Session["seq"] != float64(1+i*2+j) || len(e.ID) != 26 {
				t.Fatalf("bad event: %+v", e)
			}
		}
		raw, _ := json.Marshal(b)
		for _, s := range []string{"private-loop", "private-text", "private-path", "fictional-token", "message", "cwd", "title"} {
			if strings.Contains(string(raw), s) {
				t.Fatalf("leaked %s", s)
			}
		}
	}
	if batches[0].Events[1].Type != "attention.needed" || batches[1].Events[1].Type != "session.ended" {
		t.Fatal(batches)
	}
	h, err := LoadDeliveries(dir)
	if err != nil || len(h.Records) != 3 {
		t.Fatalf("history: %+v %v", h, err)
	}
	for _, d := range h.Records {
		if d.Outcome != "http_accepted" || d.Receipt != "unverified" || len(d.Attempts) != 1 {
			t.Fatal(d)
		}
	}
}

func TestSessionpipeAcknowledgementUncertainNeverRetries(t *testing.T) {
	for _, ack := range []string{`{}`, `{"accepted":1,"duplicates":0,"max_tier":0}`, `{"accepted":2,"duplicates":0,"max_tier":0,"rejected":[{"id":"fictional","reason":"tier_type"}]}`, `{"accepted":-1,"duplicates":3,"max_tier":0}`, `{"accepted":2,"duplicates":0,"max_tier":0} {}`, strings.Repeat("x", sessionpipeMaxResponse+1)} {
		t.Run(ack[:min(len(ack), 40)], func(t *testing.T) {
			calls := 0
			srv := pipeReceiver(t, ack, func(_ *http.Request, _ pipeBatch) { calls++ })
			c := pipeConfig(t, srv.URL)
			dir := t.TempDir()
			r, err := NewRecorded(c, dir)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Report(context.Background(), Event{Type: "await_user"}); !errors.Is(err, ErrNoRetry) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			h, err := LoadDeliveries(dir)
			if err != nil || h.Records[0].Outcome != "protocol_failed" {
				t.Fatalf("%+v %v", h, err)
			}
			s, err := reservePipe(dir)
			if err != nil || s.Next != 3 {
				t.Fatalf("uncertain sequence reused: %+v %v", s, err)
			}
		})
	}
}

func TestSessionpipeConcurrentReservationAndCorruptState(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	var mu sync.Mutex
	seqs := map[uint64]bool{}
	ids := map[string]bool{}
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := reservePipe(dir)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seqs[s.Next] {
				t.Error("duplicate sequence")
			}
			seqs[s.Next] = true
			ids[s.ID] = true
		}()
	}
	wg.Wait()
	if len(seqs) == 0 || len(ids) != 1 {
		t.Fatalf("seq=%v ids=%v", seqs, ids)
	}
	s, err := reservePipe(dir)
	if err != nil || s.Next != uint64(2*len(seqs)+1) {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range []string{`{}`, `{"version":1,"id":"bad","next":1}`, `{"version":1,"id":"00000000000000000000000000000000","next":9007199254740991}`, `{"version":1,"id":"00000000000000000000000000000000","next":1,"unknown":true}`} {
		if err := os.WriteFile(filepath.Join(dir, sessionpipeFile), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := reservePipe(dir); err == nil {
			t.Fatal("corrupt state accepted")
		}
	}
	if _, err := reservePipe("relative"); err == nil {
		t.Fatal("relative state accepted")
	}
}

func TestSessionpipeRefusesDiscoveryRedirectAndStateFailure(t *testing.T) {
	for _, mode := range []string{"redirect", "foreign", "protocol", "tiny", "state"} {
		t.Run(mode, func(t *testing.T) {
			posts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
					w.WriteHeader(202)
					w.Write([]byte(pipeAck))
					return
				}
				switch mode {
				case "redirect":
					w.Header().Set("Location", "/elsewhere")
					w.WriteHeader(307)
				case "foreign":
					w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"https://example.com/events"}}`))
				case "state":
					w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`))
				case "protocol":
					w.Write([]byte(`{"protocol":[2],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`))
				default:
					w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"},"batch":{"max_events":1}}`))
				}
			}))
			defer srv.Close()
			c := pipeConfig(t, srv.URL)
			dir := t.TempDir()
			if mode == "state" {
				os.WriteFile(filepath.Join(dir, sessionpipeFile), []byte("bad"), 0600)
			}
			r, err := NewRecorded(c, dir)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Report(context.Background(), Event{Type: "error"}); err == nil || posts != 0 {
				t.Fatalf("%v posts %d", err, posts)
			}
		})
	}
}

func TestSessionpipeDeliveryLockAndPreview(t *testing.T) {
	dir := t.TempDir()
	l, err := lock.AcquireNamed(dir, "delivery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reservePipe(dir); err == nil {
		t.Fatal("concurrent reservation accepted")
	}
	l.Release()
	if _, err = os.Stat(filepath.Join(dir, sessionpipeFile)); !os.IsNotExist(err) {
		t.Fatal("failed reservation wrote state")
	}
	c := pipeConfig(t, "https://example.com")
	w, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.Body(Event{Type: "test", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || !strings.Contains(string(b), "00000000000000000000000000000000") {
		t.Fatalf("%s %v", b, err)
	}
	c.Scheme = "standard-webhooks"
	if _, err = New(c); err == nil {
		t.Fatal("wrong auth accepted")
	}
}

func TestSessionpipeConcurrentReportersAndDuplicateAck(t *testing.T) {
	var mu sync.Mutex
	var batches []pipeBatch
	srv := pipeReceiver(t, `{"accepted":0,"duplicates":2,"rejected":[],"max_tier":0}`, func(_ *http.Request, b pipeBatch) { mu.Lock(); defer mu.Unlock(); batches = append(batches, b) })
	c := pipeConfig(t, srv.URL)
	dir := t.TempDir()
	seed, err := NewRecorded(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = seed.Report(context.Background(), Event{Type: "error"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := NewRecorded(c, dir)
			if err != nil {
				t.Error(err)
				return
			}
			_ = r.Report(context.Background(), Event{Type: "error"})
		}()
	}
	wg.Wait()
	if len(batches) == 0 {
		t.Fatal("no send succeeded")
	}
	seen := map[float64]bool{}
	id := batches[0].Events[0].Session["id"]
	for _, b := range batches {
		for _, e := range b.Events {
			seq := e.Session["seq"].(float64)
			if e.Session["id"] != id || seen[seq] {
				t.Fatalf("overlapping identity/sequence %+v", e)
			}
			seen[seq] = true
		}
	}
	// A later instance must reserve beyond every concurrent sender, even if
	// diagnostic history writes contended independently of the POST.
	s, err := reservePipe(dir)
	if err != nil {
		t.Fatal(err)
	}
	for seq := range seen {
		if uint64(seq) >= s.Next {
			t.Fatalf("reused %v at %d", seq, s.Next)
		}
	}
}

func TestSessionpipeTransportNoRedirectRetryOrSecretError(t *testing.T) {
	for _, status := range []int{200, 401, 403, 429, 503, 307} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			posts, redirects := 0, 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects++ }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`))
					return
				}
				posts++
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				w.Write([]byte("fictional-token private-response"))
			}))
			defer srv.Close()
			c := pipeConfig(t, srv.URL)
			r, err := NewRecorded(c, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			err = r.Report(context.Background(), Event{Type: "error"})
			if err == nil || posts != 1 || redirects != 0 || strings.Contains(err.Error(), "fictional-token") || strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "private-response") {
				t.Fatalf("err=%v posts=%d redirects=%d", err, posts, redirects)
			}
		})
	}
	c := pipeConfig(t, "https://example.com")
	w, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	w.stateDir = t.TempDir()
	tr := &failedTransport{}
	w.client = &http.Client{Transport: tr}
	if err = w.Report(context.Background(), Event{Type: "error"}); err == nil || errors.Is(err, ErrNoRetry) || tr.calls.Load() != 1 {
		t.Fatalf("discovery err=%v calls=%d", err, tr.calls.Load())
	}
	for _, bad := range []string{"https://fictional-token@example.com", "https://example.com/events", "https://example.com?token=fictional-token", "https://example.com/#fictional-token"} {
		t.Setenv("KG_TEST_URL", bad)
		if _, err := New(c); err == nil || strings.Contains(err.Error(), "fictional-token") {
			t.Fatalf("bad origin err %v", err)
		}
	}
}

func TestSessionpipeTotalTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c := pipeConfig(t, srv.URL)
	c.Timeout.Duration = 30 * time.Millisecond
	r, err := NewRecorded(c, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err = r.Report(context.Background(), Event{Type: "error"}); err == nil || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
}

// Once an identity has sent, missing/null required fields must never reset its
// sequence. A corrupt snapshot is evidence to inspect, not defaults to repair.
func TestSessionpipeMissingStateFieldsPreserveBytesAndPreventPOST(t *testing.T) {
	for _, fields := range []string{`"version":1`, `"version":1,"next":null`, `"next":9`, `"version":null,"next":9`, `"version":1,"next":9,"id":null`, `"version":1,"next":9`} {
		t.Run(fields, func(t *testing.T) {
			posts := 0
			srv := pipeReceiver(t, pipeAck, func(_ *http.Request, _ pipeBatch) { posts++ })
			c := pipeConfig(t, srv.URL)
			dir := t.TempDir()
			initial, err := reservePipe(dir)
			if err != nil {
				t.Fatal(err)
			}
			bad := []byte(`{"id":"` + initial.ID + `",` + fields + `}`)
			// The final case omits identity; the preceding case explicitly nulls it.
			if fields == `"version":1,"next":9` {
				bad = []byte(`{` + fields + `}`)
			}
			path := filepath.Join(dir, sessionpipeFile)
			if err = os.WriteFile(path, bad, 0600); err != nil {
				t.Fatal(err)
			}
			r, err := NewRecorded(c, dir)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Report(context.Background(), Event{Type: "error"}); err == nil || posts != 0 {
				t.Errorf("err=%v posts=%d", err, posts)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(bad) {
				t.Fatalf("corrupt bytes changed: %s err=%v", after, err)
			}
		})
	}
}

func TestSessionpipeAcceptedPOSTWithUnavailableHistoryForbidsRetry(t *testing.T) {
	posts := 0
	srv := pipeReceiver(t, pipeAck, func(_ *http.Request, _ pipeBatch) { posts++ })
	c := pipeConfig(t, srv.URL)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DeliveryFile), []byte("corrupt-history"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := NewRecorded(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	err = r.Report(context.Background(), Event{Type: "error"})
	if !errors.Is(err, ErrNoRetry) || !errors.Is(err, ErrDeliveryHistory) || posts != 1 {
		t.Fatalf("err=%v posts=%d", err, posts)
	}
}
