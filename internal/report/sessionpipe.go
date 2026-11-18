package report

// Sessionpipe reporting uses only the v1 tier-0 vocabulary. It is not a
// supervision adapter and never reads agent events or the control channel.
import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

const sessionpipeFile = "sessionpipe.json"
const sessionpipeMaxResponse = 64 << 10

var errSessionpipe = errors.New("report: Sessionpipe delivery unverified; inspect receiver configuration and private report state")

type pipeState struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Next    uint64 `json:"next"`
}

type pipeEvent struct {
	Protocol int               `json:"protocol"`
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Time     string            `json:"time"`
	Tier     int               `json:"tier"`
	Harness  map[string]string `json:"harness"`
	Session  map[string]any    `json:"session"`
	Privacy  map[string]any    `json:"privacy"`
	Data     map[string]any    `json:"data"`
}

type pipeBatch struct {
	Events []pipeEvent `json:"events"`
}

// Reserve before I/O. A failed or uncertain request consumes these sequences;
// nothing retries it automatically. The existing delivery lock also fences
// migration. No paths, loop names or report text enter the wire identity.
func reservePipe(dir string) (pipeState, error) {
	if !filepath.IsAbs(dir) {
		return pipeState{}, errSessionpipe
	}
	l, err := lock.AcquireNamed(dir, "delivery")
	if err != nil {
		return pipeState{}, errSessionpipe
	}
	defer l.Release()
	path := filepath.Join(dir, sessionpipeFile)
	var s pipeState
	info, err := snapshot.LstatRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		s.Version, s.Next = 1, 1
		b := make([]byte, 16)
		if _, err = rand.Read(b); err != nil {
			return pipeState{}, errSessionpipe
		}
		s.ID = hex.EncodeToString(b)
	} else {
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
			return pipeState{}, errSessionpipe
		}
		f, err := snapshot.OpenRecord(path)
		if err != nil {
			return pipeState{}, errSessionpipe
		}
		opened, statErr := f.Stat()
		if statErr != nil || !opened.Mode().IsRegular() || opened.Size() > 1024 || !os.SameFile(info, opened) {
			f.Close()
			return pipeState{}, errSessionpipe
		}
		b, err := io.ReadAll(io.LimitReader(f, 1025))
		f.Close()
		var stored struct {
			Version *int    `json:"version"`
			ID      *string `json:"id"`
			Next    *uint64 `json:"next"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err != nil || len(b) > 1024 || dec.Decode(&stored) != nil || dec.Decode(new(any)) != io.EOF || stored.Version == nil || stored.ID == nil || stored.Next == nil {
			return pipeState{}, errSessionpipe
		}
		s = pipeState{Version: *stored.Version, ID: *stored.ID, Next: *stored.Next}
	}
	id, err := hex.DecodeString(s.ID)
	// Keep JSON integers exactly representable in common receivers.
	if err != nil || len(id) != 16 || s.Version != 1 || s.Next == 0 || s.Next > (1<<53)-3 {
		return pipeState{}, errSessionpipe
	}
	reserved := s
	s.Next += 2
	b, _ := json.Marshal(s)
	f, err := os.CreateTemp(dir, ".sessionpipe-*")
	if err != nil {
		return pipeState{}, errSessionpipe
	}
	defer os.Remove(f.Name())
	n, err := f.Write(b)
	if err != nil || n != len(b) {
		f.Close()
		return pipeState{}, errSessionpipe
	}
	if snapshot.Sync(f) != nil {
		f.Close()
		return pipeState{}, errSessionpipe
	}
	if f.Close() != nil {
		return pipeState{}, errSessionpipe
	}
	if snapshot.Replace(f.Name(), path) != nil {
		return pipeState{}, errSessionpipe
	}
	return reserved, nil
}

// ulid emits the protocol's 26-character Crockford ULID, without dependencies.
func pipeULID(at time.Time) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[6:]); err != nil {
		return "", err
	}
	ms := uint64(at.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	out := make([]byte, 26)
	for i := 0; i < 26; i++ {
		var v byte
		for j := 0; j < 5; j++ {
			bit := i*5 + j - 2
			v <<= 1
			if bit >= 0 {
				v |= (b[bit/8] >> (7 - uint(bit%8))) & 1
			}
		}
		out[i] = alphabet[v]
	}
	return string(out), nil
}

func pipeBody(e Event, s pipeState, now time.Time) ([]byte, error) {
	at := e.At
	if at.IsZero() {
		at = now
	}
	makeEvent := func(kind string, seq uint64, data map[string]any) (pipeEvent, error) {
		id, err := pipeULID(now)
		return pipeEvent{Protocol: 1, ID: id, Type: kind, Time: at.UTC().Format("2006-01-02T15:04:05.000Z"), Tier: 0, Harness: map[string]string{"name": "looptender"}, Session: map[string]any{"id": s.ID, "seq": seq}, Privacy: map[string]any{"rulesets": []string{}, "pii": false}, Data: data}, err
	}
	start, err := makeEvent("session.started", s.Next, map[string]any{"source": "resume"})
	if err != nil {
		return nil, errSessionpipe
	}
	kind := "looptender." + eventName(e.Type)
	data := map[string]any{}
	switch e.Type {
	case "complete", "stop":
		kind = "session.ended"
		data["reason"] = "other"
	case "await_user", "error", "child_error", "hung_turn", "refusal_streak":
		kind = "attention.needed"
		data["attention_id"] = start.ID
		data["kind"] = "error"
	}
	event, err := makeEvent(kind, s.Next+1, data)
	if err != nil {
		return nil, errSessionpipe
	}
	return json.Marshal(pipeBatch{[]pipeEvent{start, event}})
}

// Discovery and send share one total timeout, never follow redirects, and
// never forward a bearer token to an origin selected by the receiver.
func (w *Webhook) reportPipe(ctx context.Context, e Event, observe func(Delivery)) error {
	d := Delivery{Event: eventName(e.Type), StartedAt: w.now().UTC(), Receipt: "unverified", Attempts: []Attempt{}}
	save := func(outcome string) {
		d.Outcome = outcome
		d.UpdatedAt = w.now().UTC()
		if observe != nil {
			observe(d)
		}
	}
	save("prepared")
	fail := func(outcome string) error {
		save(outcome)
		if len(d.Attempts) > 0 {
			return errors.Join(errSessionpipe, ErrNoRetry)
		}
		return errSessionpipe
	}
	if !filepath.IsAbs(w.stateDir) {
		return fail("body_failed")
	}
	ctx, cancel := context.WithTimeout(ctx, min(w.cfg.Timeout.Duration, 15*time.Second))
	defer cancel()
	client := *w.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base, _ := url.Parse(w.url)
	discovery := *base
	discovery.Path = "/.well-known/sessionpipe"
	discovery.RawPath = ""
	discovery.RawQuery = ""
	discovery.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery.String(), nil)
	if err != nil {
		return fail("body_failed")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fail("body_failed")
	}
	var meta struct {
		Protocol     []int    `json:"protocol"`
		MaxTier      *int     `json:"max_tier"`
		Capabilities []string `json:"capabilities"`
		Auth         []string `json:"auth"`
		Endpoints    struct {
			Events string `json:"events"`
		} `json:"endpoints"`
		Batch struct {
			MaxEvents *int `json:"max_events"`
			MaxBytes  *int `json:"max_bytes"`
		} `json:"batch"`
	}
	ok := resp.StatusCode == 200 && readPipeJSON(resp.Body, &meta) == nil
	resp.Body.Close()
	contains := func(xs []string, v string) bool {
		for _, x := range xs {
			if x == v {
				return true
			}
		}
		return false
	}
	protocol := false
	for _, p := range meta.Protocol {
		protocol = protocol || p == 1
	}
	if !ok || !protocol || meta.MaxTier == nil || *meta.MaxTier < 0 || *meta.MaxTier > 3 || !contains(meta.Capabilities, "events") || !contains(meta.Auth, "bearer") || meta.Endpoints.Events == "" {
		return fail("body_failed")
	}
	endpoint, err := url.Parse(meta.Endpoints.Events)
	if err != nil {
		return fail("body_failed")
	}
	endpoint = base.ResolveReference(endpoint)
	if endpoint.Scheme != base.Scheme || endpoint.Host != base.Host || endpoint.User != nil || endpoint.Fragment != "" {
		return fail("body_failed")
	}
	s, err := reservePipe(w.stateDir)
	if err != nil {
		return fail("body_failed")
	}
	body, err := pipeBody(e, s, w.now())
	if err != nil {
		return fail("body_failed")
	}
	maxEvents, maxBytes := 50, 262144
	if meta.Batch.MaxEvents != nil {
		maxEvents = *meta.Batch.MaxEvents
	}
	if meta.Batch.MaxBytes != nil {
		maxBytes = *meta.Batch.MaxBytes
	}
	if maxEvents < 2 || maxBytes < len(body) {
		return fail("body_failed")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fail("request_failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.secret)
	req.Header.Set("sessionpipe-protocol", "1")
	d.Attempts = append(d.Attempts, Attempt{StartedAt: w.now().UTC(), Outcome: "unconfirmed"})
	save("attempt_started")
	resp, err = client.Do(req)
	a := &d.Attempts[0]
	a.FinishedAt = w.now().UTC()
	if err != nil {
		a.Outcome = "transport_failed"
		return fail("transport_failed")
	}
	defer resp.Body.Close()
	a.HTTPStatus = resp.StatusCode
	if resp.StatusCode != 202 {
		a.Outcome = "protocol_failed"
		return fail("protocol_failed")
	}
	var ack struct {
		Accepted   *int               `json:"accepted"`
		Duplicates *int               `json:"duplicates"`
		Rejected   *[]json.RawMessage `json:"rejected"`
		MaxTier    *int               `json:"max_tier"`
	}
	if readPipeJSON(resp.Body, &ack) != nil || ack.Accepted == nil || ack.Duplicates == nil || ack.MaxTier == nil || *ack.MaxTier < 0 || *ack.MaxTier > 3 || *ack.Accepted < 0 || *ack.Duplicates < 0 || *ack.Accepted > 2 || *ack.Duplicates > 2 || *ack.Accepted+*ack.Duplicates != 2 || ack.Rejected == nil || len(*ack.Rejected) != 0 {
		a.Outcome = "protocol_failed"
		return fail("protocol_failed")
	}
	a.Outcome = "http_accepted"
	save("http_accepted")
	return nil
}

func readPipeJSON(r io.Reader, v any) error {
	b, err := io.ReadAll(io.LimitReader(r, sessionpipeMaxResponse+1))
	if err != nil || len(b) > sessionpipeMaxResponse {
		return errSessionpipe
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if dec.Decode(v) != nil || dec.Decode(new(any)) != io.EOF {
		return errSessionpipe
	}
	return nil
}
