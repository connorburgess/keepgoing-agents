// Package daemoninfo exposes bounded, loopback-only diagnostics for a daemon.
// The private endpoint record contains a random key, not process identity.
// A fresh challenge proves that a responder owns that key; stale records alone
// never establish liveness. No task, account, or provider data is exchanged.
package daemoninfo

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

const FileName = "daemon-endpoint.json"

type endpoint struct {
	Address string `json:"address"`
	Key     string `json:"key"`
}

// Snapshot is a response from a running daemon, not a persisted heartbeat.
type Snapshot struct {
	ProcessID    int        `json:"processID,omitempty"`
	InvocationID string     `json:"invocationID,omitempty"`
	StateID      string     `json:"stateID"` // binds the response to its state directory without exposing the path
	Version      string     `json:"version"`
	StartedAt    time.Time  `json:"startedAt"`
	ObservedAt   time.Time  `json:"observedAt"`
	Phase        string     `json:"phase"`
	NextCheckAt  *time.Time `json:"nextCheckAt,omitempty"`
	// LastPassAt is when the daemon's latest completed pass ended, and
	// LastPassStartedAt when it began, whether or not it recorded a check.
	// A pass that began after a check finished is not that check's pass.
	LastPassAt             *time.Time `json:"lastPassAt,omitempty"`
	LastPassStartedAt      *time.Time `json:"lastPassStartedAt,omitempty"`
	DryRun                 bool       `json:"dryRun"`
	ConfigUnavailableSince *time.Time `json:"configUnavailableSince,omitempty"`
}

type response struct {
	Snapshot json.RawMessage `json:"snapshot"`
	MAC      string          `json:"mac"`
}

// Server must be started while the caller holds the daemon lock and closed
// before that lock is released. Diagnostics do not acquire the daemon lock.
type Server struct {
	listener net.Listener
	path     string
	endpoint endpoint
	done     chan struct{}
	mu       sync.Mutex
	passAt   time.Time // when the pass under way began
	snapshot Snapshot
}

func Start(stateDir, version string, dryRun bool) (*Server, error) {
	identity, err := stateIdentity(stateDir)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{listener: l, path: filepath.Join(stateDir, FileName), endpoint: endpoint{l.Addr().String(), hex.EncodeToString(key)}, done: make(chan struct{}), snapshot: Snapshot{ProcessID: os.Getpid(), InvocationID: localInvocationID(), StateID: identity, Version: version, StartedAt: time.Now().UTC(), Phase: "checking", DryRun: dryRun}}
	b, _ := json.Marshal(s.endpoint)
	f, err := os.CreateTemp(stateDir, ".daemon-endpoint-*")
	if err != nil {
		l.Close()
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Chmod(0o600)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = snapshot.Replace(f.Name(), s.path)
	}
	if err != nil {
		l.Close()
		return nil, err
	}
	go s.serve()
	return s, nil
}

// StartedAt is when this daemon started; zero for a nil server.
func (s *Server) StartedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.StartedAt
}

// LastPassAt is when this daemon's latest pass ended; zero before the first
// or for a nil server.
func (s *Server) LastPassAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.LastPassAt == nil {
		return time.Time{}
	}
	return *s.snapshot.LastPassAt
}

// LastPassStartedAt is when this daemon's latest completed pass began; zero
// before the first or for a nil server.
func (s *Server) LastPassStartedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.LastPassStartedAt == nil {
		return time.Time{}
	}
	return *s.snapshot.LastPassStartedAt
}

func (s *Server) Update(phase string, next *time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	switch {
	case phase == "checking":
		s.passAt = now // a pass begins
	case s.snapshot.Phase == "checking" && phase == "waiting":
		ended := now
		s.snapshot.LastPassAt = &ended // a pass has just ended
		if !s.passAt.IsZero() {
			began := s.passAt
			s.snapshot.LastPassStartedAt = &began
		}
	}
	s.snapshot.Phase, s.snapshot.NextCheckAt = phase, nil
	if next != nil {
		v := next.UTC()
		s.snapshot.NextCheckAt = &v
	}
}

func (s *Server) Close() {
	if s == nil {
		return
	}
	s.listener.Close()
	<-s.done
	// Preserve a record replaced by another writer instead of deleting it.
	if ep, err := readEndpoint(s.path); err == nil && ep == s.endpoint {
		_ = snapshot.Remove(s.path)
	}
}

func (s *Server) serve() {
	defer close(s.done)
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		// Serial and bounded: a local caller cannot create unbounded goroutines.
		_ = c.SetDeadline(time.Now().Add(200 * time.Millisecond))
		nonce, err := bufio.NewReader(io.LimitReader(c, 65)).ReadString('\n')
		if err == nil && validKey(strings.TrimSuffix(nonce, "\n")) {
			s.mu.Lock()
			snap := s.snapshot
			snap.ObservedAt = time.Now().UTC()
			s.mu.Unlock()
			b, _ := json.Marshal(snap)
			_ = json.NewEncoder(c).Encode(response{b, sign(s.endpoint.Key, nonce, b)})
		}
		_ = c.Close()
	}
}

// ValidInvocationID accepts the canonical nonzero systemd runtime-cycle ID.
func ValidInvocationID(s string) bool {
	if len(s) != 32 || s == strings.Repeat("0", 32) {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func localInvocationID() string {
	if runtime.GOOS == "linux" {
		if id := os.Getenv("INVOCATION_ID"); ValidInvocationID(id) {
			return id
		}
	}
	return ""
}

func validKey(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == 32 }

func stateIdentity(dir string) (string, error) {
	p, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(p))
	return hex.EncodeToString(h[:]), nil
}

func sign(key, nonce string, b []byte) string {
	k, _ := hex.DecodeString(key)
	m := hmac.New(sha256.New, k)
	m.Write([]byte(nonce))
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

func readEndpoint(path string) (endpoint, error) {
	var ep endpoint
	info, err := snapshot.Lstat(path)
	if err != nil {
		return ep, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return ep, errors.New("invalid endpoint record")
	}
	f, err := snapshot.Open(path)
	if err != nil {
		return ep, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(b) > 1024 {
		return ep, errors.New("unreadable endpoint record")
	}
	if json.Unmarshal(b, &ep) != nil || !validKey(ep.Key) {
		return ep, errors.New("invalid endpoint record")
	}
	host, port, err := net.SplitHostPort(ep.Address)
	n, numErr := strconv.Atoi(port)
	if err != nil || host != "127.0.0.1" || numErr != nil || n < 1 || n > 65535 {
		return endpoint{}, errors.New("invalid loopback endpoint")
	}
	return ep, nil
}

// Query never creates state. The caller supplies a deadline; all failures stay
// unverified. The key is not sent over the socket or exposed in errors.
func Query(ctx context.Context, stateDir string) (Snapshot, error) {
	var snap Snapshot
	ep, err := readEndpoint(filepath.Join(stateDir, FileName))
	if err != nil {
		return snap, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp4", ep.Address)
	if err != nil {
		return snap, errors.New("daemon endpoint did not respond")
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return snap, err
	}
	challenge := hex.EncodeToString(nonce) + "\n"
	if _, err := io.WriteString(c, challenge); err != nil {
		return snap, errors.New("daemon challenge failed")
	}
	var reply response
	if json.NewDecoder(io.LimitReader(c, 8192)).Decode(&reply) != nil {
		return snap, errors.New("invalid daemon response")
	}
	if !hmac.Equal([]byte(reply.MAC), []byte(sign(ep.Key, challenge, reply.Snapshot))) {
		return snap, errors.New("daemon response could not be verified")
	}
	if json.Unmarshal(reply.Snapshot, &snap) != nil || snap.StartedAt.IsZero() || snap.ObservedAt.IsZero() || (snap.Phase != "checking" && snap.Phase != "waiting") {
		return Snapshot{}, errors.New("invalid daemon snapshot")
	}
	// Older daemons omit identity. Invalid optional metadata must not create a
	// service binding, but need not discard otherwise valid responsiveness.
	if snap.ProcessID < 1 || snap.ProcessID > 2147483647 {
		snap.ProcessID = 0
	}
	if !ValidInvocationID(snap.InvocationID) {
		snap.InvocationID = ""
	}
	identity, err := stateIdentity(stateDir)
	if err != nil || snap.StateID != identity {
		return Snapshot{}, errors.New("daemon response belongs to another state directory")
	}
	return snap, nil
}

// SetConfigUnavailable publishes only a dated configuration diagnostic.
func (s *Server) SetConfigUnavailable(since *time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.ConfigUnavailableSince = nil
	if since != nil {
		at := since.UTC()
		s.snapshot.ConfigUnavailableSince = &at
	}
}
