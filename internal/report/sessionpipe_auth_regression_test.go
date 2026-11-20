package report

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSessionpipe401RestartRegression(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == "GET" {
			w.Write([]byte(`{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`))
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := pipeConfig(t, srv.URL)
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		r, err := NewRecorded(c, dir)
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Report(context.Background(), Event{Type: "await_user"}); err == nil {
			t.Fatal("401 unexpectedly accepted")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("401 pause lost across reporter restart: got %d network calls, want 2", calls.Load())
	}
}
