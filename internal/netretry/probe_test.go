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
	t.Setenv("KEEPGOING_NET_PROBE", "")
	t.Setenv("KEEPGOING_NET_PROBE_URL", slow.URL)
	start = time.Now()
	if (Probe{Timeout: 200 * time.Millisecond}).Online(ctx) || time.Since(start) > 5*time.Second {
		t.Fatal("the default target ignored the probe timeout")
	}
	late := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(600 * time.Millisecond) }))
	defer late.Close()
	t.Setenv("KEEPGOING_NET_PROBE_URL", late.URL)
	if (Probe{Timeout: 100 * time.Millisecond}).Online(ctx) || !(Probe{Timeout: 5 * time.Second}).Online(ctx) {
		t.Fatal("the default target did not follow the configured timeout")
	}
	// With neither set the environment decides, as for net-retry.
	t.Setenv("KEEPGOING_NET_PROBE_URL", up.URL)
	t.Setenv("KEEPGOING_NET_PROBE", "")
	if !(Probe{}).Online(ctx) {
		t.Fatal("the default probe ignored KEEPGOING_NET_PROBE_URL")
	}
}

// Behind a captive portal the target does not answer as itself. A redirect
// to another host (the portal's login page) and a TLS handshake presented by
// a certificate this machine does not trust both read as offline. A redirect
// within the same host is the target answering.
func TestCaptivePortalReadsAsOffline(t *testing.T) {
	ctx := context.Background()
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("sign in")) }))
	defer login.Close()
	// The login page is on another host name for the same listener.
	loginURL := strings.Replace(login.URL, "127.0.0.1", "localhost", 1)
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loginURL+"/login", http.StatusFound)
	}))
	defer portal.Close()
	if (Probe{URL: portal.URL + "/zen"}).Online(ctx) {
		t.Fatal("a redirect to a portal's login page counted as online")
	}
	sameHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zen" {
			http.Redirect(w, r, "/moved", http.StatusMovedPermanently)
		}
	}))
	defer sameHost.Close()
	if !(Probe{URL: sameHost.URL + "/zen"}).Online(ctx) {
		t.Fatal("a redirect within the target's own host counted as offline")
	}
	intercepted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer intercepted.Close()
	if (Probe{URL: intercepted.URL}).Online(ctx) {
		t.Fatal("a TLS handshake with an untrusted certificate counted as online")
	}
	loop := httptest.NewServer(nil)
	defer loop.Close()
	loop.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.URL.Path+"x", http.StatusFound) })
	if (Probe{URL: loop.URL + "/", Timeout: 5 * time.Second}).Online(ctx) {
		t.Fatal("an endless redirect counted as online")
	}
}
