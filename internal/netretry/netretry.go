// Package netretry reruns a network command only when it failed because
// the network was down. A failure while the network is reachable is a real
// failure and is returned at once, so a rejected push or merge is never
// blindly repeated.
package netretry

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// ExitNetworkGone is returned when the network never came back in time.
const ExitNetworkGone = 75

// Options bound the retries.
type Options struct {
	MaxWait  time.Duration // total time to wait for the network
	Attempts int           // total runs, including the first
	// Online reports connectivity. Nil uses DefaultOnline.
	Online func(ctx context.Context) bool
	// Sleep waits; nil uses time.Sleep honouring ctx.
	Sleep func(ctx context.Context, d time.Duration)
	// Log receives progress lines; nil discards them.
	Log func(msg string)
}

func (o *Options) fill() {
	if o.MaxWait <= 0 {
		o.MaxWait = 2 * time.Hour
	}
	if o.Attempts <= 0 {
		o.Attempts = 6
	}
	if o.Online == nil {
		o.Online = DefaultOnline
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
}

// ErrNetworkGone wraps the last error when the network did not return.
var ErrNetworkGone = errors.New("network did not return in time")

// Do runs fn, retrying only while the network is down.
func Do(ctx context.Context, opts Options, fn func() error) error {
	opts.fill()
	deadline := time.Now().Add(opts.MaxWait)
	for run := 1; ; run++ {
		err := fn()
		if err == nil {
			return nil
		}
		if opts.Online(ctx) {
			return err
		}
		if run >= opts.Attempts {
			opts.Log("giving up after " + itoa(run) + " runs")
			return err
		}
		opts.Log("run " + itoa(run) + " failed and the network is down; waiting to reconnect")
		delay := 5 * time.Second
		for !opts.Online(ctx) {
			if time.Now().After(deadline) || ctx.Err() != nil {
				return errors.Join(ErrNetworkGone, err)
			}
			opts.Sleep(ctx, delay)
			if delay < time.Minute {
				delay *= 2
			}
		}
		opts.Log("network is back; retrying")
	}
}

// Command runs argv with inherited stdio under Do and returns its exit code.
func Command(ctx context.Context, opts Options, argv []string) int {
	code := 0
	err := Do(ctx, opts, func() error {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := cmd.Run()
		code = exitCode(err)
		return err
	})
	if errors.Is(err, ErrNetworkGone) {
		return ExitNetworkGone
	}
	return code
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 127
}

// DefaultOnline checks connectivity. KEEPGOING_NET_PROBE, when set, is a
// command line run by the platform's shell whose exit 0 means online; otherwise an HTTP GET to
// KEEPGOING_NET_PROBE_URL (default https://api.github.com/zen) must answer.
func DefaultOnline(ctx context.Context) bool {
	if probe := os.Getenv("KEEPGOING_NET_PROBE"); probe != "" {
		return shell(ctx, probe).Run() == nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return defaultOnline(ctx)
}

// DefaultURL is requested when nothing else is configured.
const DefaultURL = "https://api.github.com/zen"

// defaultOnline is DefaultOnline under the caller's own time limit.
func defaultOnline(ctx context.Context) bool {
	if probe := os.Getenv("KEEPGOING_NET_PROBE"); probe != "" {
		return shell(ctx, probe).Run() == nil
	}
	url := os.Getenv("KEEPGOING_NET_PROBE_URL")
	if url == "" {
		url = DefaultURL
	}
	return get(ctx, url)
}

// Probe is a connectivity check with explicit settings. Command, when set,
// is an argv whose exit 0 means online. Otherwise URL must answer an HTTP
// GET; any response counts, a transport error does not. With neither set it
// is DefaultOnline, which honours the KEEPGOING_NET_PROBE variables.
type Probe struct {
	URL     string
	Command []string
	Timeout time.Duration
}

// Online runs the check once.
func (p Probe) Online(ctx context.Context) bool {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch {
	case len(p.Command) > 0:
		cmd := exec.CommandContext(ctx, p.Command[0], p.Command[1:]...)
		cmd.WaitDelay = 2 * time.Second
		return cmd.Run() == nil
	case p.URL != "":
		return get(ctx, p.URL)
	}
	return defaultOnline(ctx)
}

// client never follows a redirect: the redirect itself is the answer.
var client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

// get reports whether url's target answered the request itself. Online means
// a response that is not a redirect and not 511 Network Authentication
// Required: whatever else the status, a server that answers shows the
// network works. Offline means a transport error, or an answer that sends
// the client elsewhere, which is what a captive portal gives, on the target's
// own name or another. A TLS handshake that a gateway intercepts with a
// certificate this machine does not trust is a transport error. A portal that
// answers a plain http request in place, with a page of its own and an
// ordinary status, cannot be told from the target; use https.
func get(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	redirect := resp.StatusCode >= 300 && resp.StatusCode < 400
	return !redirect && resp.StatusCode != http.StatusNetworkAuthenticationRequired
}

// shell runs a command line with the platform's shell.
func shell(ctx context.Context, line string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", line)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", line)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
