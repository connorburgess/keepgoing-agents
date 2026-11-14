// Package health keeps the evidence that each fallback works and answers
// whether the loop has enough confirmed fallbacks to hand off safely.
package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/allowance"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// FileName of the registry inside the state directory.
const FileName = "health.json"

const historyLen = 5

// Entry is one candidate's record.
type Entry struct {
	Last        *probe.Result  `json:"last,omitempty"`
	LastSched   time.Time      `json:"lastScheduledAt,omitempty"`
	ProbeDay    string         `json:"probeDay,omitempty"` // UTC date of ProbesToday
	ProbesToday int            `json:"probesToday,omitempty"`
	History     []probe.Result `json:"history,omitempty"`
	// Usage is the latest usage reading any probe of this candidate carried,
	// with its own observation time. A later probe without a reading leaves
	// it in place, so it can be older than Last. No decision reads it.
	Usage *probe.Usage `json:"usage,omitempty"`
	// UsageHistory is every recorded reading of this candidate's account per
	// window instance, bounded, for projecting use at a window's reset. Like
	// Usage it belongs to one account identity, and no decision reads it.
	UsageHistory *allowance.History `json:"usageHistory,omitempty"`
	// NearReset records the window instances a timing probe was made for as
	// their reset came near, so each gets one. No decision reads it but
	// TimingDue.
	NearReset []NearResetMark `json:"nearReset,omitempty"`
}

// NearResetMark is one window instance probed as its reset came near.
type NearResetMark struct {
	Key      string    `json:"key"`
	ResetsAt time.Time `json:"resetsAt"`
}

// maxNearResetMarks bounds the marks kept: one per named window instance.
const maxNearResetMarks = 16

// Registry is health.json.
type Registry struct {
	Candidates map[string]*Entry `json:"candidates"`
}

// Load reads the registry; a missing file is empty.
func Load(stateDir string) (*Registry, error) {
	r := &Registry{Candidates: map[string]*Entry{}}
	b, err := snapshot.ReadFile(filepath.Join(stateDir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if r.Candidates == nil {
		r.Candidates = map[string]*Entry{}
	}
	for _, e := range r.Candidates {
		// A stored reading that did not load cleanly is simply absent.
		if e != nil && !e.Usage.Available() {
			e.Usage = nil
		}
		if e != nil && e.UsageHistory != nil && len(e.UsageHistory.Windows) == 0 {
			e.UsageHistory = nil
		}
	}
	return r, nil
}

// Save writes the registry atomically.
func (r *Registry) Save(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	tmp.Chmod(0o600)
	if err := tmp.Close(); err != nil {
		return err
	}
	return snapshot.Replace(name, filepath.Join(stateDir, FileName))
}

func (r *Registry) entry(id string) *Entry {
	e, ok := r.Candidates[id]
	if !ok {
		e = &Entry{}
		r.Candidates[id] = e
	}
	return e
}

// Record stores a probe result.
func (r *Registry) Record(id string, res probe.Result) {
	e := r.entry(id)
	rc := res
	e.Last = &rc
	// A reading belongs to the account it was read from. Once the candidate
	// is probed as a different account, an earlier reading no longer applies.
	if e.Usage != nil && res.UsageIdentity != "" && e.Usage.Identity != res.UsageIdentity {
		e.Usage = nil
	}
	if e.UsageHistory != nil && res.UsageIdentity != "" && e.UsageHistory.Identity != res.UsageIdentity {
		e.UsageHistory = nil
	}
	if res.Usage.Available() {
		u := *res.Usage
		e.Usage = &u
		e.UsageHistory = allowance.Add(e.UsageHistory, &u)
	}
	e.History = append([]probe.Result{res}, e.History...)
	if len(e.History) > historyLen {
		e.History = e.History[:historyLen]
	}
	if res.Tier == probe.Scheduled {
		day := res.At.UTC().Format("2006-01-02")
		if e.ProbeDay != day {
			e.ProbeDay, e.ProbesToday = day, 0
		}
		e.ProbesToday++
		e.LastSched = res.At
	}
}

// Healthy reports whether the candidate's last probe passed within ttl.
func (r *Registry) Healthy(id string, now time.Time, ttl time.Duration) bool {
	e, ok := r.Candidates[id]
	return ok && e.Last != nil && e.Last.OK && now.Sub(e.Last.At) <= ttl
}

// Due reports whether a scheduled probe should run for the candidate now.
func (r *Registry) Due(c config.Candidate, p config.ProbePolicy, now time.Time) (bool, string) {
	if c.Disabled {
		return false, "disabled"
	}
	if p.Disabled {
		return false, "scheduled probes are off"
	}
	e := r.Candidates[c.ID]
	if e != nil && e.ProbeDay == now.UTC().Format("2006-01-02") && e.ProbesToday >= p.DailyCap {
		return false, "daily cap reached"
	}
	// A recent passing probe of any tier is fresh evidence. A failed manual or
	// exact probe is not, so it does not hold off the scheduled re-check.
	if e != nil && e.Last != nil && e.Last.OK && now.Sub(e.Last.At) < p.Every.Duration {
		return false, "probed recently"
	}
	if e != nil && !e.LastSched.IsZero() && now.Sub(e.LastSched) < p.Every.Duration {
		return false, "probed recently"
	}
	return true, "due"
}

// Summary is the redundancy picture relative to the active target.
type Summary struct {
	Healthy  []string `json:"healthy"`
	Eligible []string `json:"eligible"`
	Distinct int      `json:"distinct"`
	Min      int      `json:"min"`
	Span     string   `json:"span"`
	Meets    bool     `json:"meets"`
}

// Evaluate lists healthy candidates in file order among those eligible to
// take over from the active target: not disabled, not the active target,
// and not sharing its group (the exhausted account or quota pool).
func Evaluate(fb *config.Fallbacks, r *Registry, now time.Time, activeID, activeGroup string) Summary {
	s := Summary{Healthy: []string{}, Eligible: []string{}, Min: fb.Redundancy.MinHealthy, Span: fb.Redundancy.Span}
	keys := map[string]bool{}
	for _, c := range fb.Candidates {
		if c.Disabled || c.ID == activeID || (activeGroup != "" && c.Group == activeGroup) {
			continue
		}
		s.Eligible = append(s.Eligible, c.ID)
		if !r.Healthy(c.ID, now, fb.Probe.TTL.Duration) {
			continue
		}
		s.Healthy = append(s.Healthy, c.ID)
		switch fb.Redundancy.Span {
		case "provider":
			keys[c.Provider] = true
		case "group":
			keys[c.Group] = true
		default:
			keys[c.ID] = true
		}
	}
	s.Distinct = len(keys)
	s.Meets = s.Distinct >= s.Min && len(s.Healthy) > 0
	return s
}

// IDs returns the registry's candidate ids, sorted.
func (r *Registry) IDs() []string {
	var ids []string
	for id := range r.Candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Timing probe reasons, as TimingDue returns them.
const (
	TimingDue          = "due"
	TimingNearReset    = "near_reset"
	TimingRecent       = "probed recently"
	TimingCapReached   = "daily cap reached"
	TimingScheduledOff = "scheduled probes are off"
)

// TimingProbe reports whether the first route's timing probe should run now,
// and for a probe as a named window's reset comes near, which window instance
// it is for. Timing probes count against the route's own daily cap like any
// scheduled probe. The order is:
//
//   - scheduled probes switched off in the fallbacks file, or the day's cap
//     reached: none;
//   - a named window whose reset, from the latest reading of this account,
//     is within NearReset and has not been probed for: one probe, unless the
//     latest reading was already taken that close to the reset;
//   - otherwise every ProbeEvery since the last scheduled probe or the last
//     passing probe of any tier.
func (r *Registry) TimingProbe(c config.Candidate, t config.Timing, p config.ProbePolicy, identity string, now time.Time) (bool, string, *NearResetMark) {
	if p.Disabled {
		return false, TimingScheduledOff, nil
	}
	e := r.Candidates[c.ID]
	if e != nil && e.ProbeDay == now.UTC().Format("2006-01-02") && e.ProbesToday >= p.DailyCap {
		return false, TimingCapReached, nil
	}
	if e != nil && e.Usage.For(identity) {
		for _, key := range t.Windows {
			for _, w := range e.Usage.Windows {
				if w.Key != key || w.ResetsAt == nil {
					continue
				}
				at := *w.ResetsAt
				if !now.Before(at) || at.Sub(now) > t.NearReset.Duration || !e.Usage.At.Before(at.Add(-t.NearReset.Duration)) {
					continue
				}
				if e.markedNearReset(key, at) {
					continue
				}
				return true, TimingNearReset, &NearResetMark{Key: key, ResetsAt: at.UTC()}
			}
		}
	}
	if e != nil && e.Last != nil && e.Last.OK && now.Sub(e.Last.At) < t.ProbeEvery.Duration {
		return false, TimingRecent, nil
	}
	if e != nil && !e.LastSched.IsZero() && now.Sub(e.LastSched) < t.ProbeEvery.Duration {
		return false, TimingRecent, nil
	}
	return true, TimingDue, nil
}

func (e *Entry) markedNearReset(key string, at time.Time) bool {
	for _, m := range e.NearReset {
		if m.Key == key && m.ResetsAt.Equal(at) {
			return true
		}
	}
	return false
}

// MarkNearReset records that a timing probe was made for the window instance,
// dropping marks whose reset has passed, newest kept.
func (r *Registry) MarkNearReset(id string, m NearResetMark, now time.Time) {
	e := r.entry(id)
	kept := []NearResetMark{}
	for _, x := range e.NearReset {
		if x.ResetsAt.After(now) && !(x.Key == m.Key && x.ResetsAt.Equal(m.ResetsAt)) {
			kept = append(kept, x)
		}
	}
	kept = append(kept, m)
	if len(kept) > maxNearResetMarks {
		kept = kept[len(kept)-maxNearResetMarks:]
	}
	e.NearReset = kept
}
