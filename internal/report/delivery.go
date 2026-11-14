package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

const DeliveryFile = "deliveries.json"
const MaxDeliveries = 64
const MaxDeliveryBytes = 128 * 1024

// ErrDeliveryHistory never embeds paths, credentials, request data or errors.
var ErrDeliveryHistory = errors.New("notification delivery history unavailable; check private state storage")

// Attempt is one client.Do call, which can include redirects for ordinary events.
// StartedAt is saved before Do: it is intent, not proof bytes reached the network.
type Attempt struct {
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Outcome    string    `json:"outcome"`
	HTTPStatus int       `json:"httpStatus,omitempty"`
}

// Delivery contains no route, payload, note, provider identity or raw error.
// HTTP acceptance never establishes receipt at the destination application.
type Delivery struct {
	Sequence  uint64    `json:"sequence"`
	Event     string    `json:"event"`
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Outcome   string    `json:"outcome"`
	Receipt   string    `json:"receipt"`
	Attempts  []Attempt `json:"attempts"`
}

type DeliveryHistory struct {
	SchemaVersion int        `json:"schemaVersion"`
	NextSequence  uint64     `json:"nextSequence"`
	Dropped       uint64     `json:"dropped"`
	Records       []Delivery `json:"records"`
}

func eventName(s string) string {
	if slices.Contains(config.ReportEvents, s) {
		return s
	}
	return "unknown"
}

// LoadDeliveries is read-only, bounded, strict and never repairs corrupt history.
// Missing history means no recorded evidence, not successful delivery.
func LoadDeliveries(dir string) (DeliveryHistory, error) {
	path := filepath.Join(dir, DeliveryFile)
	info, err := snapshot.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return DeliveryHistory{SchemaVersion: 1, NextSequence: 1, Records: []Delivery{}}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxDeliveryBytes {
		return DeliveryHistory{}, ErrDeliveryHistory
	}
	f, err := snapshot.Open(path)
	if err != nil {
		return DeliveryHistory{}, ErrDeliveryHistory
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxDeliveryBytes+1))
	if err != nil || len(b) > MaxDeliveryBytes {
		return DeliveryHistory{}, ErrDeliveryHistory
	}
	var h DeliveryHistory
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&h) != nil || dec.Decode(new(any)) != io.EOF || h.SchemaVersion != 1 || h.NextSequence == 0 || len(h.Records) > MaxDeliveries || h.Dropped > h.NextSequence-1 || h.NextSequence-1-h.Dropped != uint64(len(h.Records)) {
		return DeliveryHistory{}, ErrDeliveryHistory
	}
	for i, d := range h.Records {
		if d.Sequence != h.Dropped+uint64(i)+1 || !validDelivery(d) {
			return DeliveryHistory{}, ErrDeliveryHistory
		}
	}
	return h, nil
}

func validDelivery(d Delivery) bool {
	if d.StartedAt.IsZero() || d.UpdatedAt.IsZero() || d.Event != eventName(d.Event) || d.Receipt != "unverified" || len(d.Attempts) > 2 {
		return false
	}
	switch d.Outcome {
	case "prepared", "setup_failed", "body_failed", "sign_failed", "request_failed", "attempt_started", "transport_failed", "http_rejected", "http_accepted", "not_sent":
	default:
		return false
	}
	for _, a := range d.Attempts {
		if a.StartedAt.IsZero() {
			return false
		}
		switch a.Outcome {
		case "unconfirmed":
			if !a.FinishedAt.IsZero() || a.HTTPStatus != 0 {
				return false
			}
		case "transport_failed":
			if a.FinishedAt.IsZero() || a.HTTPStatus != 0 {
				return false
			}
		case "http_accepted":
			if a.FinishedAt.IsZero() || a.HTTPStatus < 200 || a.HTTPStatus >= 300 {
				return false
			}
		case "http_rejected":
			if a.FinishedAt.IsZero() || a.HTTPStatus < 100 || a.HTTPStatus > 599 || (a.HTTPStatus >= 200 && a.HTTPStatus < 300) {
				return false
			}
		default:
			return false
		}
	}
	switch d.Outcome {
	case "prepared", "setup_failed", "body_failed", "sign_failed", "not_sent":
		return len(d.Attempts) == 0
	case "request_failed":
		return true // request construction can fail before a retry
	case "attempt_started":
		return len(d.Attempts) > 0 && d.Attempts[len(d.Attempts)-1].Outcome == "unconfirmed"
	default:
		return len(d.Attempts) > 0 && d.Attempts[len(d.Attempts)-1].Outcome == d.Outcome
	}
}

// saveDelivery inserts (sequence zero) or updates only the assigned record.
// Never hold this lock during HTTP, or take a control/tick lock from here.
func saveDelivery(dir string, d Delivery) (uint64, error) {
	l, err := lock.AcquireNamed(dir, "delivery")
	if err != nil {
		return 0, ErrDeliveryHistory
	}
	defer l.Release()
	h, err := LoadDeliveries(dir)
	if err != nil || !validDelivery(d) {
		return 0, ErrDeliveryHistory
	}
	if d.Sequence == 0 {
		if h.NextSequence == ^uint64(0) {
			return 0, ErrDeliveryHistory
		}
		d.Sequence = h.NextSequence
		h.NextSequence++
		h.Records = append(h.Records, d)
		if len(h.Records) > MaxDeliveries {
			h.Records = h.Records[1:]
			h.Dropped++
		}
	} else {
		i := slices.IndexFunc(h.Records, func(r Delivery) bool { return r.Sequence == d.Sequence })
		if i < 0 {
			return 0, ErrDeliveryHistory
		}
		h.Records[i] = d
	}
	b, err := json.Marshal(h)
	if err != nil || len(b)+1 > MaxDeliveryBytes {
		return 0, ErrDeliveryHistory
	}
	f, err := os.CreateTemp(dir, ".deliveries-*")
	if err != nil {
		return 0, ErrDeliveryHistory
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return 0, ErrDeliveryHistory
	}
	if err = snapshot.Sync(f); err != nil {
		f.Close()
		return 0, ErrDeliveryHistory
	}
	if err = f.Close(); err != nil {
		return 0, ErrDeliveryHistory
	}
	if snapshot.Replace(name, filepath.Join(dir, DeliveryFile)) != nil {
		return 0, ErrDeliveryHistory
	}
	return d.Sequence, nil
}

type recorded struct {
	webhook *Webhook
	cfg     config.Report
	dir     string
}

// NewRecorded returns a reporter even when setup fails: subscribed events can
// then record setup_failed with no request. The error is for immediate diagnostics.
func NewRecorded(cfg config.Report, dir string) (Reporter, error) {
	w, err := New(cfg)
	return recorded{webhook: w, cfg: cfg, dir: dir}, err
}

func (r recorded) Report(ctx context.Context, e Event) error {
	if !r.cfg.ReportsEvent(e.Type) {
		return nil
	}
	var seq uint64
	var historyErr error
	observe := func(d Delivery) {
		d.Sequence = seq
		id, err := saveDelivery(r.dir, d)
		if err != nil {
			historyErr = ErrDeliveryHistory
		} else {
			seq = id
		}
	}
	var sendErr error
	if r.webhook == nil {
		now := time.Now().UTC()
		observe(Delivery{Event: eventName(e.Type), StartedAt: now, UpdatedAt: now, Outcome: "setup_failed", Receipt: "unverified", Attempts: []Attempt{}})
		sendErr = errors.New("report: setup failed; no request attempted")
	} else {
		sendErr = r.webhook.report(ctx, e, observe)
	}
	// Diagnostic failure must never suppress the original request, trigger another
	// attempt, or alter a hold. Callers report the error after the existing action.
	return errors.Join(sendErr, historyErr)
}

// Dropper is implemented by reporters that keep delivery records. Dropped
// records a subscribed event the caller decided not to send, with no request
// and no attempt, so the omission is visible beside the deliveries.
type Dropper interface {
	Dropped(e Event) error
}

func (r recorded) Dropped(e Event) error {
	if !r.cfg.ReportsEvent(e.Type) {
		return nil
	}
	now := time.Now().UTC()
	if _, err := saveDelivery(r.dir, Delivery{Event: eventName(e.Type), StartedAt: now, UpdatedAt: now, Outcome: "not_sent", Receipt: "unverified", Attempts: []Attempt{}}); err != nil {
		return ErrDeliveryHistory
	}
	return nil
}

// DeliveryStatus is historical evidence, never endpoint health or send authority.
type DeliveryStatus struct {
	State    string     `json:"state"`
	Dropped  uint64     `json:"dropped"`
	Latest   *Delivery  `json:"latest,omitempty"`
	Warnings []Delivery `json:"warnings"`
}

func InspectDeliveries(dir string) DeliveryStatus {
	s := DeliveryStatus{State: "not_recorded", Warnings: []Delivery{}}
	h, err := LoadDeliveries(dir)
	if err != nil {
		s.State = "unavailable"
		return s
	}
	s.Dropped = h.Dropped
	if len(h.Records) == 0 {
		return s
	}
	s.State = "recorded"
	s.Latest = &h.Records[len(h.Records)-1]
	for _, d := range h.Records {
		if d.Outcome != "http_accepted" {
			s.Warnings = append(s.Warnings, d)
		}
	}
	return s
}
