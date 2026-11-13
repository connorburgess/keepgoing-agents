package report

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type failedTransport struct{ calls atomic.Int32 }

func (f *failedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls.Add(1)
	return nil, errors.New("uncertain transport failure")
}

func TestCompletionSingleAttemptForNetworkFailure(t *testing.T) {
	t.Setenv("KG_TEST_URL", "https://example.com/hook")
	t.Setenv("KG_TEST_SECRET", secret)
	w, err := New(cfg(""))
	if err != nil {
		t.Fatal(err)
	}
	tr := &failedTransport{}
	w.client = &http.Client{Transport: tr}
	if err := w.Report(context.Background(), Event{Type: "complete"}); err == nil || tr.calls.Load() != 1 {
		t.Fatalf("err=%v attempts=%d", err, tr.calls.Load())
	}
}
func TestCompletionSingleAttemptForFailureAndRedirect(t *testing.T) {
	for _, format := range []string{"standard", "text"} {
		for _, status := range []int{200, 400, 503, 307, 308} {
			var calls, redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			t.Setenv("KG_TEST_URL", srv.URL)
			t.Setenv("KG_TEST_SECRET", secret)
			c := cfg("")
			c.Format = format
			if format == "text" {
				c.Scheme = "bearer"
			}
			w, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			err = w.Report(context.Background(), Event{Type: "complete", Text: "completion declared; success not independently verified"})
			srv.Close()
			target.Close()
			if (err == nil) != (status == 200) || calls.Load() != 1 || redirected.Load() != 0 {
				t.Fatalf("%s/%d err=%v calls=%d redirected=%d", format, status, err, calls.Load(), redirected.Load())
			}
		}
	}
}

// A limit_wait report is requested once per wait, so like a completion it
// makes one attempt and follows no redirect: an uncertain delivery must not
// reach a person twice.
func TestLimitWaitSingleAttemptForFailureAndRedirect(t *testing.T) {
	t.Setenv("KG_TEST_URL", "https://example.com/hook")
	t.Setenv("KG_TEST_SECRET", secret)
	c := cfg("")
	c.Events = []string{"limit_wait", "await_user"}
	w, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	tr := &failedTransport{}
	w.client = &http.Client{Transport: tr}
	if err := w.Report(context.Background(), Event{Type: "limit_wait"}); err == nil || tr.calls.Load() != 1 {
		t.Fatalf("err=%v attempts=%d", err, tr.calls.Load())
	}
	// The ordinary policy is unchanged for other events.
	tr.calls.Store(0)
	if err := w.Report(context.Background(), Event{Type: "await_user"}); err == nil || tr.calls.Load() != 2 {
		t.Fatalf("await_user: err=%v attempts=%d", err, tr.calls.Load())
	}
	for _, status := range []int{202, 400, 503, 307} {
		var calls, redirected atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
		}))
		t.Setenv("KG_TEST_URL", srv.URL)
		w, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Report(context.Background(), Event{Type: "limit_wait", Text: "fictional loop is waiting on a provider limit"})
		srv.Close()
		target.Close()
		if (err == nil) != (status == 202) || calls.Load() != 1 || redirected.Load() != 0 {
			t.Fatalf("%d: err=%v calls=%d redirected=%d", status, err, calls.Load(), redirected.Load())
		}
	}
	// An unsubscribed limit_wait sends nothing.
	c.Events = []string{"await_user"}
	if w, err = New(c); err != nil {
		t.Fatal(err)
	}
	tr.calls.Store(0)
	w.client = &http.Client{Transport: tr}
	if err := w.Report(context.Background(), Event{Type: "limit_wait"}); err != nil || tr.calls.Load() != 0 {
		t.Fatalf("unsubscribed: err=%v attempts=%d", err, tr.calls.Load())
	}
}

// A child_error report is requested once per newly failed child, so like a
// completion it makes one attempt and follows no redirect.
func TestChildErrorSingleAttemptForFailureAndRedirect(t *testing.T) {
	t.Setenv("KG_TEST_URL", "https://example.com/hook")
	t.Setenv("KG_TEST_SECRET", secret)
	c := cfg("")
	c.Events = []string{"child_error", "await_user"}
	w, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	tr := &failedTransport{}
	w.client = &http.Client{Transport: tr}
	if err := w.Report(context.Background(), Event{Type: "child_error"}); err == nil || tr.calls.Load() != 1 {
		t.Fatalf("err=%v attempts=%d", err, tr.calls.Load())
	}
	for _, status := range []int{202, 503, 307} {
		var calls, redirected atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
		}))
		t.Setenv("KG_TEST_URL", srv.URL)
		w, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Report(context.Background(), Event{Type: "child_error", Text: "fictional loop has child threads in error"})
		srv.Close()
		target.Close()
		if calls.Load() != 1 || redirected.Load() != 0 || (err == nil) != (status == 202) {
			t.Fatalf("status %d: requests=%d redirected=%d err=%v", status, calls.Load(), redirected.Load(), err)
		}
	}
	t.Setenv("KG_TEST_URL", "https://example.com/hook")
	// An unsubscribed child_error sends nothing.
	c.Events = []string{"await_user"}
	w, _ = New(c)
	tr.calls.Store(0)
	w.client = &http.Client{Transport: tr}
	if err := w.Report(context.Background(), Event{Type: "child_error"}); err != nil || tr.calls.Load() != 0 {
		t.Fatalf("err=%v attempts=%d", err, tr.calls.Load())
	}
}
