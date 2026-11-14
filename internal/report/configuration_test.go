package report

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Only this cause suppresses retries/redirects; ordinary errors retain their
// established two-attempt delivery policy.
func TestConfigurationErrorHasOneAttemptWithoutChangingOtherErrors(t *testing.T) {
	for _, special := range []bool{true, false} {
		t.Run(map[bool]string{true: "configuration", false: "ordinary"}[special], func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); w.WriteHeader(503) }))
			defer srv.Close()
			t.Setenv("KG_TEST_URL", srv.URL)
			t.Setenv("KG_TEST_SECRET", secret)
			w, err := New(cfg(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			event := Event{Type: "error", Loop: "example-loop", Text: "fixture failure"}
			want := int32(2)
			if special {
				event.Data = map[string]any{"cause": "configuration_unavailable"}
				want = 1
			}
			if w.Report(context.Background(), event) == nil {
				t.Fatal("503 accepted")
			}
			if n.Load() != want {
				t.Fatalf("%d attempts, want %d", n.Load(), want)
			}
		})
	}
}
