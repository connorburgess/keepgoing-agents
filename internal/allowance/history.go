// Package allowance keeps the usage readings of one account over time and
// projects, from those recorded readings alone, how much of a window will be
// used when it resets. It is a display value: nothing in the decision engine
// reads it. It adds no provider call and reads nothing but stored readings.
package allowance

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

// Bounds on a stored history.
const (
	// MaxSamples is how many readings of one window are kept: four days at a
	// probe every four hours, twelve at one every twelve hours. A projection
	// fits a rate through them, so older readings add little.
	MaxSamples = 24
	// MaxWindows matches the number of windows one reading may hold.
	MaxWindows = 8
	// SameReset is how far two reported reset times may differ and still
	// describe the same window. A window's next instance resets at least one
	// window length later, so a small allowance only absorbs rounding.
	SameReset = 5 * time.Minute
)

// History is the recorded readings of one account, per window instance. It
// belongs to the account identity it was read for; readings of another
// identity start a new history.
type History struct {
	Identity string   `json:"identity"`
	Windows  []Series `json:"windows"`
}

// Series is one instance of one window: its key and the reset time that
// identifies the instance, with the readings taken during it, oldest first.
// A window reported without a reset time has no instance and no series.
type Series struct {
	Key      string    `json:"key"`
	ResetsAt time.Time `json:"resetsAt"`
	Samples  []Sample  `json:"samples"`
}

// Sample is one reading of a window: when it was taken and the used fraction.
type Sample struct {
	At   time.Time `json:"at"`
	Used float64   `json:"used"`
}

// Find returns the series for key, or nil.
func (h *History) Find(key string) *Series {
	if h == nil {
		return nil
	}
	for i := range h.Windows {
		if h.Windows[i].Key == key {
			return &h.Windows[i]
		}
	}
	return nil
}

// For reports whether the history was recorded for the given identity.
func (h *History) For(identity string) bool {
	return h != nil && identity != "" && h.Identity == identity && len(h.Windows) > 0
}

// Add returns the history with the reading u added, leaving h unchanged.
//
//   - No reading leaves the history as it is.
//   - A reading of another identity starts a new history.
//   - Readings stamped after this one are dropped first. Only a clock that
//     was ahead and has been set back leaves them, so the current clock wins
//     and new readings are never refused because of old ones.
//   - A series whose reset time is at or before the reading's time has reset
//     and is dropped, unless the reading itself reports a window whose reset
//     is at or before its own time: then this machine's clock may be ahead,
//     and nothing is dropped for it, so a clock far ahead cannot wipe the
//     history. Such a window is not recorded.
//   - The same reading recorded twice adds nothing.
//   - A window whose reset time differs from its series' by more than
//     SameReset starts a new instance.
//   - Each series keeps its newest MaxSamples readings.
func Add(h *History, u *probe.Usage) *History {
	if !u.Available() {
		return h
	}
	clockAhead := false
	for _, w := range u.Windows {
		if w.ResetsAt != nil && !w.ResetsAt.After(u.At) {
			clockAhead = true
		}
	}
	out := &History{Identity: u.Identity}
	if h != nil && h.Identity == u.Identity {
		for _, s := range h.Windows {
			if !clockAhead && !s.ResetsAt.After(u.At) {
				continue
			}
			c := s.clone()
			kept := c.Samples[:0]
			for _, x := range c.Samples {
				if !x.At.After(u.At) {
					kept = append(kept, x)
				}
			}
			if c.Samples = kept; len(c.Samples) > 0 {
				out.Windows = append(out.Windows, c)
			}
		}
	}
	for _, w := range u.Windows {
		if w.ResetsAt == nil || !w.ResetsAt.After(u.At) {
			continue
		}
		sample := Sample{At: u.At.UTC(), Used: w.Used}
		s := out.Find(w.Key)
		switch {
		case s == nil:
			out.Windows = append(out.Windows, Series{Key: w.Key, ResetsAt: w.ResetsAt.UTC(), Samples: []Sample{sample}})
			continue
		case !sample.At.After(s.Samples[len(s.Samples)-1].At):
			continue
		case absDuration(s.ResetsAt.Sub(*w.ResetsAt)) > SameReset:
			*s = Series{Key: w.Key, ResetsAt: w.ResetsAt.UTC(), Samples: []Sample{sample}}
			continue
		}
		s.ResetsAt = w.ResetsAt.UTC()
		s.Samples = append(s.Samples, sample)
		if len(s.Samples) > MaxSamples {
			s.Samples = s.Samples[len(s.Samples)-MaxSamples:]
		}
	}
	out.bound()
	if len(out.Windows) == 0 {
		return nil
	}
	return out
}

// bound sorts the series by key and keeps the MaxWindows read most recently.
func (h *History) bound() {
	if len(h.Windows) > MaxWindows {
		sort.SliceStable(h.Windows, func(i, j int) bool { return h.Windows[i].newest().After(h.Windows[j].newest()) })
		h.Windows = h.Windows[:MaxWindows]
	}
	sort.Slice(h.Windows, func(i, j int) bool { return h.Windows[i].Key < h.Windows[j].Key })
}

func (s Series) clone() Series {
	s.Samples = append([]Sample(nil), s.Samples...)
	return s
}

func (s Series) newest() time.Time {
	if len(s.Samples) == 0 {
		return time.Time{}
	}
	return s.Samples[len(s.Samples)-1].At
}

// UnmarshalJSON never fails: a stored history that is malformed loads as
// absent, and a series that is not a plain record of readings is dropped,
// so the health registry stays readable.
func (h *History) UnmarshalJSON(b []byte) error {
	type plain History
	var p plain
	*h = History{}
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	identity, ok := probe.UsageToken(p.Identity)
	if !ok {
		return nil
	}
	out := History{Identity: identity}
	seen := map[string]bool{}
	for _, s := range p.Windows {
		key, ok := probe.UsageToken(s.Key)
		if !ok || seen[key] || s.ResetsAt.IsZero() || len(s.Samples) == 0 {
			continue
		}
		clean := Series{Key: key, ResetsAt: s.ResetsAt.UTC()}
		valid := true
		for _, x := range s.Samples {
			if x.At.IsZero() || math.IsNaN(x.Used) || x.Used < 0 || x.Used > 1 ||
				(len(clean.Samples) > 0 && !x.At.After(clean.Samples[len(clean.Samples)-1].At)) {
				valid = false
				break
			}
			clean.Samples = append(clean.Samples, Sample{At: x.At.UTC(), Used: x.Used})
		}
		if !valid {
			continue
		}
		if len(clean.Samples) > MaxSamples {
			clean.Samples = clean.Samples[len(clean.Samples)-MaxSamples:]
		}
		seen[key] = true
		out.Windows = append(out.Windows, clean)
	}
	out.bound()
	*h = out
	return nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
