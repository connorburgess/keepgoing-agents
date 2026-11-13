package netretry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestProbeOnline(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer up.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	gone := down.URL
	down.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	ctx := context.Background()
	// Any HTTP response means the network works, whatever its status.
	if !(Probe{URL: up.URL}).Online(ctx) {
		t.Fatal("an answering server counted as offline")
	}
	if (Probe{URL: gone}).Online(ctx) {
		t.Fatal("a refused connection counted as online")
	}
	start := time.Now()
	if (Probe{URL: slow.URL, Timeout: 200 * time.Millisecond}).Online(ctx) || time.Since(start) > 5*time.Second {
		t.Fatal("a server that never answers was not bounded by the timeout")
	}
	if (Probe{URL: "://not a url"}).Online(ctx) {
		t.Fatal("an unusable URL counted as online")
	}
	if runtime.GOOS != "windows" {
		if !(Probe{Command: []string{"true"}}).Online(ctx) || (Probe{Command: []string{"false"}}).Online(ctx) {
			t.Fatal("command exit status not used")
		}
		if (Probe{Command: []string{"keepgoing-no-such-command-example"}}).Online(ctx) {
			t.Fatal("a missing command counted as online")
		}
		// A command wins over a URL.
		if (Probe{URL: up.URL, Command: []string{"false"}}).Online(ctx) {
			t.Fatal("the URL was used although a command is set")
		}
	}
	// With neither set the environment decides, as for net-retry.
	t.Setenv("KEEPGOING_NET_PROBE_URL", up.URL)
	t.Setenv("KEEPGOING_NET_PROBE", "")
	if !(Probe{}).Online(ctx) {
		t.Fatal("the default probe ignored KEEPGOING_NET_PROBE_URL")
	}
}
