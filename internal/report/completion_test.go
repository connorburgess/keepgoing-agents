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
