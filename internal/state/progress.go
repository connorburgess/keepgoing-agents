package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

const ProgressName = "progress.json"

var ErrProgressLedger = errors.New("progress allowance unavailable; inspect private progress.json, runtime access and host clock")

// Progress is independent of diagnostic history and per-turn counters. Sources
// is a hash of the repository list, never a list of private paths.
type Progress struct {
	Version     int       `json:"version"`
	Sources     string    `json:"sources"`
	Fingerprint string    `json:"fingerprint"`
	ChangedAt   time.Time `json:"changedAt"`
	ObservedAt  time.Time `json:"observedAt"`
}

// ProgressRepositories normalizes ordering for both the source identity and the
// fingerprint. Config loading has already resolved repository paths.
func ProgressRepositories(repositories []string) []string {
	set := map[string]bool{}
	for _, repo := range repositories {
		set[filepath.Clean(repo)] = true
	}
	result := make([]string, 0, len(set))
	for repo := range set {
		result = append(result, repo)
	}
	sort.Strings(result)
	return result
}

func ProgressSources(repositories []string) string {
	b, _ := json.Marshal(ProgressRepositories(repositories))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// LoadProgress returns nil for a new allowance. Corruption is not a reset.
func LoadProgress(dir string) (*Progress, error) {
	b, err := snapshot.ReadRecord(filepath.Join(dir, ProgressName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var p Progress
	if err != nil || json.Unmarshal(b, &p) != nil || p.Version != 1 || p.Sources == "" || p.Fingerprint == "" || p.ChangedAt.IsZero() || p.ObservedAt.IsZero() || p.ObservedAt.Before(p.ChangedAt) {
		return nil, ErrProgressLedger
	}
	return &p, nil
}

// ObserveProgress persists a verified observation before provider work under the
// caller's tick lock. A changed source list rebases the fingerprint but does not
// replenish time; only a change observed against the same source list does.
func ObserveProgress(dir string, previous *Progress, sources, fingerprint string, now time.Time, dryRun bool) (Progress, error) {
	if sources == "" || fingerprint == "" || now.IsZero() || (previous != nil && now.Before(previous.ObservedAt)) {
		return Progress{}, ErrProgressLedger
	}
	p := Progress{Version: 1, Sources: sources, Fingerprint: fingerprint, ChangedAt: now, ObservedAt: now}
	if previous != nil && sources == previous.Sources {
		p.Fingerprint = Remember(previous.Fingerprint, fingerprint)
	}
	if previous != nil && (sources != previous.Sources || SameProgress(previous.Fingerprint, fingerprint)) {
		p.ChangedAt = previous.ChangedAt
	}
	if !dryRun {
		b, err := json.Marshal(p)
		if err != nil {
			return Progress{}, ErrProgressLedger
		}
		if err := saveAllowance(dir, ProgressName, b); err != nil {
			return Progress{}, ErrProgressLedger
		}
	}
	return p, nil
}

func (p Progress) Reached(now time.Time, timeout time.Duration) bool {
	return timeout > 0 && !now.Before(p.ChangedAt.Add(timeout))
}

// CreditProgress moves the no-progress baseline forward by d, for time in
// which nothing could have made progress: no check ran, or the network was
// down. It never passes the latest observation.
func CreditProgress(dir string, p Progress, d time.Duration, dryRun bool) (Progress, error) {
	if d <= 0 {
		return p, nil
	}
	p.ChangedAt = p.ChangedAt.Add(d)
	if p.ChangedAt.After(p.ObservedAt) {
		p.ChangedAt = p.ObservedAt
	}
	if !dryRun {
		b, err := json.Marshal(p)
		if err != nil {
			return Progress{}, ErrProgressLedger
		}
		if err := saveAllowance(dir, ProgressName, b); err != nil {
			return Progress{}, ErrProgressLedger
		}
	}
	return p, nil
}
