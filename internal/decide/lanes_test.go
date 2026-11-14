package decide

import (
	"strings"
	"testing"
	"time"
)

func TestLaneVerdictFor(t *testing.T) {
	ok := LaneFacts{Verified: true, Errored: true, InputKnown: true, ClassKnown: true, Class: ErrNetwork}
	with := func(change func(*LaneFacts)) LaneFacts {
		f := ok
		change(&f)
		return f
	}
	for name, tc := range map[string]struct {
		f    LaneFacts
		want LaneVerdict
	}{
		"network error":                   {ok, LaneRetry},
		"overloaded":                      {with(func(f *LaneFacts) { f.Class = ErrOverloaded }), LaneRetry},
		"unknown error":                   {with(func(f *LaneFacts) { f.Class = ErrUnknown }), LaneRetry},
		"error with no class":             {with(func(f *LaneFacts) { f.Class = "" }), LaneRetry},
		"rate limited":                    {with(func(f *LaneFacts) { f.Class = ErrRateLimit }), LaneRateLimited},
		"auth":                            {with(func(f *LaneFacts) { f.Class = ErrAuth }), LaneAuth},
		"restricted":                      {with(func(f *LaneFacts) { f.Class = ErrRestricted }), LaneDeclined},
		"refused":                         {with(func(f *LaneFacts) { f.Class = ErrRefused }), LaneRetry},
		"refused and retrying":            {with(func(f *LaneFacts) { f.Class, f.Retrying = ErrRefused, true }), LaneRetrying},
		"restricted and retrying":         {with(func(f *LaneFacts) { f.Class, f.Retrying = ErrRestricted, true }), LaneDeclined},
		"an unrecognised class":           {with(func(f *LaneFacts) { f.Class = "something_new" }), LaneUnknown},
		"failure not read":                {with(func(f *LaneFacts) { f.ClassKnown = false }), LaneUnknown},
		"not verified as a child":         {with(func(f *LaneFacts) { f.Verified = false }), LaneUnverified},
		"working again":                   {with(func(f *LaneFacts) { f.Errored = false }), LaneNotInError},
		"something queued":                {with(func(f *LaneFacts) { f.Queued = 1 }), LaneQueued},
		"waiting on a person":             {with(func(f *LaneFacts) { f.WaitingOnPerson = true }), LaneWaitingPerson},
		"interactions not read":           {with(func(f *LaneFacts) { f.InputKnown = false }), LaneWaitingPerson},
		"provider retrying":               {with(func(f *LaneFacts) { f.Retrying = true }), LaneRetrying},
		"rate limited and retrying":       {with(func(f *LaneFacts) { f.Class, f.Retrying = ErrRateLimit, true }), LaneRateLimited},
		"waiting on a person, rate limit": {with(func(f *LaneFacts) { f.Class, f.WaitingOnPerson = ErrRateLimit, true }), LaneWaitingPerson},
		"nothing established":             {LaneFacts{}, LaneUnverified},
		"only the class":                  {LaneFacts{ClassKnown: true, Class: ErrNetwork}, LaneUnverified},
		"verified, nothing else":          {LaneFacts{Verified: true}, LaneNotInError},
		"errored, nothing else":           {LaneFacts{Verified: true, Errored: true}, LaneWaitingPerson},
	} {
		if got := LaneVerdictFor(tc.f); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
	// Exactly one verdict permits the retry, and it needs every fact.
	for i := 0; i < 1<<7; i++ {
		f := LaneFacts{Verified: i&1 != 0, Errored: i&2 != 0, InputKnown: i&4 != 0, WaitingOnPerson: i&8 != 0, ClassKnown: i&16 != 0, Retrying: i&32 != 0}
		if i&64 != 0 {
			f.Queued = 1
		}
		for _, class := range []ErrorClass{ErrNetwork, ErrOverloaded, ErrUnknown, ErrRateLimit, ErrAuth} {
			f.Class = class
			want := f.Verified && f.Errored && f.InputKnown && !f.WaitingOnPerson && f.ClassKnown && !f.Retrying && f.Queued == 0 && class != ErrRateLimit && class != ErrAuth
			if got := LaneVerdictFor(f) == LaneRetry; got != want {
				t.Fatalf("%+v: retry %t, want %t", f, got, want)
			}
		}
	}
}

func TestPlanLanes(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	lim := LaneLimits{MaxPerCheck: 2, MaxAttempts: 3, Backoff: 10 * time.Minute, EpisodeLeft: 20}
	lane := func(hash string, change func(*Lane)) Lane {
		l := Lane{Hash: hash, SeenWorking: true}
		if change != nil {
			change(&l)
		}
		return l
	}
	for name, tc := range map[string]struct {
		lanes   []Lane
		lim     LaneLimits
		try     string
		giveUp  string
		waiting int
	}{
		"nothing": {nil, lim, "", "", 0},
		"in hash order, up to the per-check limit":          {[]Lane{lane("c", nil), lane("a", nil), lane("b", nil)}, lim, "a,b", "", 1},
		"a lane put off last time goes first":               {[]Lane{lane("a", nil), lane("b", nil), lane("c", func(l *Lane) { l.Deferred = true })}, lim, "c,a", "", 1},
		"never seen working":                                {[]Lane{lane("a", func(l *Lane) { l.SeenWorking = false }), lane("b", nil)}, lim, "b", "", 0},
		"given up":                                          {[]Lane{lane("a", func(l *Lane) { l.GivenUp, l.Attempts = true, 3 })}, lim, "", "", 0},
		"inside its spacing":                                {[]Lane{lane("a", func(l *Lane) { l.Attempts, l.LastAt = 1, now.Add(-9*time.Minute) })}, lim, "", "", 1},
		"inspected and left alone nine minutes ago":         {[]Lane{lane("a", func(l *Lane) { l.LookedAt = now.Add(-9 * time.Minute) })}, lim, "", "", 1},
		"inspected and left alone ten minutes ago":          {[]Lane{lane("a", func(l *Lane) { l.LookedAt = now.Add(-10 * time.Minute) })}, lim, "a", "", 0},
		"spacing passed":                                    {[]Lane{lane("a", func(l *Lane) { l.Attempts, l.LastAt = 2, now.Add(-10*time.Minute) })}, lim, "a", "", 0},
		"attempts spent, last one still inside its spacing": {[]Lane{lane("a", func(l *Lane) { l.Attempts, l.LastAt = 3, now.Add(-time.Minute) })}, lim, "", "", 0},
		"attempts spent and the spacing passed":             {[]Lane{lane("a", func(l *Lane) { l.Attempts, l.LastAt = 3, now.Add(-11*time.Minute) })}, lim, "", "a", 0},
		"the episode allows one more":                       {[]Lane{lane("a", nil), lane("b", nil)}, LaneLimits{MaxPerCheck: 5, MaxAttempts: 3, Backoff: time.Minute, EpisodeLeft: 1}, "a", "", 1},
		"the episode is spent":                              {[]Lane{lane("a", nil)}, LaneLimits{MaxPerCheck: 5, MaxAttempts: 3, Backoff: time.Minute, EpisodeLeft: 0}, "", "a", 0},
		"the episode is overspent":                          {[]Lane{lane("a", nil)}, LaneLimits{MaxPerCheck: 5, MaxAttempts: 3, Backoff: time.Minute, EpisodeLeft: -4}, "", "a", 0},
		"a per-check limit of zero":                         {[]Lane{lane("a", nil)}, LaneLimits{MaxPerCheck: 0, MaxAttempts: 3, Backoff: time.Minute, EpisodeLeft: 9}, "", "", 1},
		"give-ups do not use the per-check limit":           {[]Lane{lane("a", func(l *Lane) { l.Attempts, l.LastAt = 3, now.Add(-time.Hour) }), lane("b", nil), lane("c", nil)}, lim, "b,c", "a", 0},
	} {
		p := PlanLanes(tc.lanes, now, tc.lim)
		if strings.Join(p.Try, ",") != tc.try || strings.Join(p.GiveUp, ",") != tc.giveUp || p.Waiting != tc.waiting {
			t.Errorf("%s: try %v give up %v waiting %d", name, p.Try, p.GiveUp, p.Waiting)
		}
	}
	// The plan never names a lane that was not seen working or was given up,
	// and does not reorder the caller's slice.
	in := []Lane{lane("z", nil), lane("y", func(l *Lane) { l.SeenWorking = false }), lane("x", func(l *Lane) { l.GivenUp = true })}
	p := PlanLanes(in, now, LaneLimits{MaxPerCheck: 9, MaxAttempts: 3, Backoff: time.Minute, EpisodeLeft: 9})
	if strings.Join(p.Try, ",") != "z" || len(p.GiveUp) != 0 || in[0].Hash != "z" || in[2].Hash != "x" {
		t.Fatalf("%+v %+v", p, in)
	}
}

func TestLanesAllowed(t *testing.T) {
	for a, want := range map[Action]bool{None: true, Wake: true, Nudge: true, Reprompt: true, AwaitUser: false, Stop: false, Handoff: false, SwitchBack: false, Action("something new"): false} {
		for _, session := range []State{Idle, Active, Error} {
			if got := LanesAllowed(a, session, true, false); got != want {
				t.Errorf("%s, session %s, in the window: %v", a, session, got)
			}
		}
		if LanesAllowed(a, Idle, false, false) {
			t.Errorf("%s outside the window is allowed", a)
		}
		if LanesAllowed(a, Idle, true, true) {
			t.Errorf("%s is allowed while the session waits on a person", a)
		}
		for _, session := range []State{Stopping, Unknown, State(""), State("something new")} {
			if LanesAllowed(a, session, true, false) {
				t.Errorf("%s is allowed while the session is %q", a, session)
			}
		}
	}
}

// With the episode's retries spent, a lane that is due is given up, so it is
// reported, instead of waiting for a retry that will never come.
func TestPlanLanesGivesUpWhenTheEpisodeIsSpent(t *testing.T) {
	now := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	lanes := []Lane{
		{Hash: "a", SeenWorking: true, Attempts: 1, LastAt: now.Add(-11 * time.Minute)},
		{Hash: "b", SeenWorking: true, Attempts: 2, LastAt: now.Add(-time.Minute)}, // inside its spacing
		{Hash: "c", SeenWorking: true},
		{Hash: "d"}, // never seen working
		{Hash: "e", SeenWorking: true, GivenUp: true},
	}
	lim := LaneLimits{MaxPerCheck: 5, MaxAttempts: 3, Backoff: 10 * time.Minute}
	for _, left := range []int{0, -3} {
		lim.EpisodeLeft = left
		p := PlanLanes(lanes, now, lim)
		if len(p.Try) != 0 || strings.Join(p.GiveUp, ",") != "a,c" || p.Waiting != 1 {
			t.Fatalf("%d left: %+v", left, p)
		}
	}
	lim.EpisodeLeft = 1
	if p := PlanLanes(lanes, now, lim); strings.Join(p.Try, ",") != "a" || len(p.GiveUp) != 0 || p.Waiting != 2 {
		t.Fatalf("%+v", p)
	}
}

// A lane the provider keeps answering "nothing to do" for is given up after
// three such answers, and a lane that keeps failing again after it recovered
// is bounded by its retries in a day.
func TestPlanLanesBoundsNoOpsAndRetriesPerDay(t *testing.T) {
	now := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	lim := LaneLimits{MaxPerCheck: 5, MaxAttempts: 3, Backoff: 10 * time.Minute, EpisodeLeft: 20, MaxNoOps: 3, MaxPerDay: 6}
	for name, tc := range map[string]struct {
		lane        Lane
		try, giveUp bool
	}{
		"two no-ops, spaced":           {Lane{NoOps: 2, LookedAt: now.Add(-11 * time.Minute)}, true, false},
		"two no-ops, inside spacing":   {Lane{NoOps: 2, LookedAt: now.Add(-time.Minute)}, false, false},
		"three no-ops":                 {Lane{NoOps: 3, LookedAt: now.Add(-time.Minute)}, false, true},
		"five retries today":           {Lane{DayRetries: 5}, true, false},
		"six retries today":            {Lane{DayRetries: 6}, false, true},
		"six today, last a minute ago": {Lane{DayRetries: 6, Attempts: 1, LastAt: now.Add(-time.Minute)}, false, false},
	} {
		tc.lane.Hash, tc.lane.SeenWorking = "a", true
		p := PlanLanes([]Lane{tc.lane}, now, lim)
		if (len(p.Try) == 1) != tc.try || (len(p.GiveUp) == 1) != tc.giveUp {
			t.Errorf("%s: %+v", name, p)
		}
	}
}
