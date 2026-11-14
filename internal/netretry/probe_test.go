package netretry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
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
	// The default target honors the probe's own timeout, shorter or longer
	// than net-retry's eight seconds.
	t.Setenv("LOOPTENDER_NET_PROBE", "")
	t.Setenv("LOOPTENDER_NET_PROBE_URL", slow.URL)
	start = time.Now()
	if (Probe{Timeout: 200 * time.Millisecond}).Online(ctx) || time.Since(start) > 5*time.Second {
		t.Fatal("the default target ignored the probe timeout")
	}
	late := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(600 * time.Millisecond) }))
	defer late.Close()
	t.Setenv("LOOPTENDER_NET_PROBE_URL", late.URL)
	if (Probe{Timeout: 100 * time.Millisecond}).Online(ctx) || !(Probe{Timeout: 5 * time.Second}).Online(ctx) {
		t.Fatal("the default target did not follow the configured timeout")
	}
	// With neither set the environment decides, as for net-retry.
	t.Setenv("LOOPTENDER_NET_PROBE_URL", up.URL)
	t.Setenv("LOOPTENDER_NET_PROBE", "")
	if !(Probe{}).Online(ctx) {
		t.Fatal("the default probe ignored LOOPTENDER_NET_PROBE_URL")
	}
}

// Behind a captive portal the target does not answer the request itself. A
// redirect, to another host or within the same name, a 511, and a TLS
// handshake presented with a certificate this machine does not trust all read
// as offline. Any other status from the target is online.
func TestCaptivePortalReadsAsOffline(t *testing.T) {
	ctx := context.Background()
	serve := func(h http.HandlerFunc) string {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv.URL
	}
	login := serve(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("sign in")) })
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		online  bool
	}{
		"redirect to another host": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, strings.Replace(login, "127.0.0.1", "localhost", 1)+"/login", http.StatusFound)
		}, false},
		"redirect on the same name": {func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/portal", http.StatusFound) }, false},
		"temporary redirect": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/portal", http.StatusTemporaryRedirect)
		}, false},
		"511":              {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNetworkAuthenticationRequired) }, false},
		"200":              {func(http.ResponseWriter, *http.Request) {}, true},
		"204":              {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, true},
		"403 rate limited": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }, true},
		"503":              {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, true},
	} {
		if got := (Probe{URL: serve(tc.handler) + "/zen"}).Online(ctx); got != tc.online {
			t.Errorf("%s: online=%t, want %t", name, got, tc.online)
		}
	}
	intercepted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer intercepted.Close()
	if (Probe{URL: intercepted.URL}).Online(ctx) {
		t.Fatal("a TLS handshake with an untrusted certificate counted as online")
	}
}
