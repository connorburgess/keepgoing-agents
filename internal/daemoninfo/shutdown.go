package daemoninfo

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"time"
)

// ShutdownRequest binds a restrictive operation to one challenged invocation.
// Caller is the unchanged CLI caller identity, under the existing local-user
// trust boundary. Possession of the private endpoint key is not person proof.
type ShutdownRequest struct {
	RunID     string    `json:"runID"`
	Nonce     string    `json:"nonce"`
	StateID   string    `json:"stateID"`
	StartedAt time.Time `json:"startedAt"`
	Version   string    `json:"version"`
	Binding   string    `json:"binding"`
	Caller    string    `json:"caller"`
}
type shutdownWire struct {
	Request ShutdownRequest `json:"request"`
	MAC     string          `json:"mac"`
}

// EnableShutdown installs a nonblocking validator/canceller. It must never call
// Close, wait for this endpoint, or change controls. A successful response means
// cancellation was accepted, not that the daemon has exited.
func (s *Server) EnableShutdown(fn func(ShutdownRequest) error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdown = fn
	s.snapshot.Shutdown = fn != nil
}

func (s *Server) serveShutdown(c net.Conn, line string) {
	var w shutdownWire
	if json.Unmarshal([]byte(line), &w) != nil || !validKey(w.Request.Nonce) {
		return
	}
	b, _ := json.Marshal(w.Request)
	if !hmac.Equal([]byte(w.MAC), []byte(sign(s.endpoint.Key, "shutdown-request\n", b))) {
		return
	}
	s.mu.Lock()
	snap, fn := s.snapshot, s.shutdown
	s.mu.Unlock()
	result := "refused"
	if fn != nil && w.Request.RunID == snap.ShutdownID && w.Request.StateID == snap.StateID && w.Request.StartedAt.Equal(snap.StartedAt) && w.Request.Version == snap.Version {
		if fn(w.Request) == nil {
			result = "accepted"
		}
	}
	b, _ = json.Marshal(result)
	_ = json.NewEncoder(c).Encode(response{b, sign(s.endpoint.Key, "shutdown-response\n"+w.Request.Nonce, b)})
}

// Shutdown sends exactly one signed request. Any transport failure after sending
// is unknown; callers must reconcile, never signal a PID or blindly restart.
func Shutdown(ctx context.Context, dir string, expected Snapshot, binding, caller string) error {
	if !expected.Shutdown || !validKey(expected.ShutdownID) {
		return errors.New("shutdown unavailable: legacy daemon requires its original owned lifecycle manager")
	}
	identity, err := stateIdentity(dir)
	if err != nil || identity != expected.StateID {
		return errors.New("shutdown state identity mismatch")
	}
	ep, err := readEndpoint(filepath.Join(dir, FileName))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp4", ep.Address)
	if err != nil {
		return errors.New("shutdown endpoint unavailable")
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	req := ShutdownRequest{RunID: expected.ShutdownID, Nonce: hex.EncodeToString(nonce), StateID: expected.StateID, StartedAt: expected.StartedAt, Version: expected.Version, Binding: binding, Caller: caller}
	b, _ := json.Marshal(req)
	if json.NewEncoder(c).Encode(shutdownWire{req, sign(ep.Key, "shutdown-request\n", b)}) != nil {
		return errors.New("shutdown outcome unknown; reconcile the same invocation")
	}
	var reply response
	if json.NewDecoder(io.LimitReader(c, 8192)).Decode(&reply) != nil || !hmac.Equal([]byte(reply.MAC), []byte(sign(ep.Key, "shutdown-response\n"+req.Nonce, reply.Snapshot))) {
		return errors.New("shutdown outcome unknown; reconcile the same invocation")
	}
	var result string
	if json.Unmarshal(reply.Snapshot, &result) != nil || result != "accepted" {
		return errors.New("shutdown refused: busy check or changed invocation, sources, controls or owner")
	}
	return nil
}
