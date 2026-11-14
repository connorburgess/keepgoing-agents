package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Timeline is an offline diagnostic projection. It never runs an adapter,
// acquires a writer lock, or uses history to authorize supervision.
type timelineReport struct {
	SchemaVersion int               `json:"schemaVersion"`
	ReadAt        time.Time         `json:"readAt"`
	StaleAfter    string            `json:"staleAfter"`
	Control       timelineControl   `json:"control"`
	Filter        timelineFilter    `json:"filter"`
	Sources       []timelineSource  `json:"sources"`
	Summary       timelineSummary   `json:"summary"`
	Retention     timelineRetention `json:"retention"`
	Layers        []timelineLayer   `json:"layers"`
	Events        []timelineEntry   `json:"events"`
	Omitted       int               `json:"omitted"`
	Limitations   []string          `json:"limitations"`
	NextSteps     []string          `json:"nextSteps"`
}

type timelineControl struct {
	Availability string             `json:"availability"`
	State        string             `json:"state"`
	Flags        *incident.Controls `json:"flags,omitempty"`
	UpdatedAt    *time.Time         `json:"updatedAt,omitempty"`
}

// A filter narrows displayed events only. Summaries, sources and layers always
// describe all retained evidence. The target is reported as its key, never the
// alias typed on the command line.
type timelineFilter struct {
	Kinds     []string   `json:"kinds"`
	TargetKey string     `json:"targetKey,omitempty"`
	Since     *time.Time `json:"since,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
	Matched   int        `json:"matched"`
	Excluded  int        `json:"excluded"`
	// ReachesPrunedHistory is true when the selected time range extends to or
	// before records that retention removed: an empty result there is unknown.
	ReachesPrunedHistory bool `json:"reachesPrunedHistory"`
}

// Retention reports what incidents.json no longer holds in full, so a gap is
// never read as "nothing happened". Collapsed checks are summarized on a
// retained record; pruned records are gone.
type timelineRetention struct {
	MaxRecords int            `json:"maxRecords"`
	MaxBytes   int            `json:"maxBytes"`
	Tiers      []timelineTier `json:"tiers"`
	// CollapsedChecks are summarized on retained records. CollapsedPruned were
	// summarized on records that were later pruned.
	CollapsedChecks uint64 `json:"collapsedChecks"`
	CollapsedPruned uint64 `json:"collapsedPruned"`
	// UntieredPruned were dropped by an older writer, with no tier or time.
	UntieredPruned uint64 `json:"untieredPruned"`
}

// Reserved is room other tiers cannot take; a tier may hold more while the
// journal has room to spare.
type timelineTier struct {
	Tier          string     `json:"tier"`
	Reserved      int        `json:"reserved"`
	Retained      int        `json:"retained"`
	Pruned        uint64     `json:"pruned"`
	PrunedThrough *time.Time `json:"prunedThrough,omitempty"`
}

// Event kinds are a fixed vocabulary derived from typed records. An event can
// carry several: a handoff check is also a check and usually a probe.
var timelineKindNames = []string{"check", "dry_run", "wake", "nudge", "reprompt", "handoff", "switch_back", "return", "retarget", "ownership", "probe", "hold", "stop", "limit_wait", "limit_wait_report", "turn_retry", "child_retry", "route_list", "route_change", "refusal", "collapsed", "control", "notification"}

// moves selects every recorded change of who has the loop.
var timelineMoveKinds = []string{"handoff", "switch_back", "return", "retarget", "ownership"}

type timelineSource struct {
	Source        string `json:"source"`
	Availability  string `json:"availability"`
	SchemaVersion int    `json:"schemaVersion,omitempty"`
	Retained      int    `json:"retained"`
	Dropped       uint64 `json:"dropped"`
}

type timelineLayer struct {
	Layer        string `json:"layer"`
	Availability string `json:"availability"`
	Explanation  string `json:"explanation"`
}

type timelineEntry struct {
	At          time.Time        `json:"at"`
	TimeBasis   string           `json:"timeBasis"`
	Source      string           `json:"source"`
	Sequence    uint64           `json:"sequence"`
	Freshness   string           `json:"freshness"`
	Description string           `json:"description"`
	Kinds       []string         `json:"kinds"`
	Incident    *incident.Record `json:"incident,omitempty"`
	Delivery    *report.Delivery `json:"delivery,omitempty"`
}

// References point into retained sources, even when --limit hides an event.
// Latest means append/start sequence order within a source, not maximum time.
type timelineRef struct {
	Source      string    `json:"source"`
	Sequence    uint64    `json:"sequence"`
	At          time.Time `json:"at"`
	Freshness   string    `json:"freshness"`
	Description string    `json:"description"`
}

type timelineSummary struct {
	LatestCheck          *timelineRef `json:"latestCheck,omitempty"`
	LastObservedActive   *timelineRef `json:"lastObservedActive,omitempty"`
	LastSendAction       *timelineRef `json:"lastSendAction,omitempty"`
	LastHoldEntry        *timelineRef `json:"lastHoldEntry,omitempty"`
	LastTargetMove       *timelineRef `json:"lastTargetMove,omitempty"`
	LastLimitWait        *timelineRef `json:"lastLimitWait,omitempty"`
	LastLimitWaitReport  *timelineRef `json:"lastLimitWaitReport,omitempty"`
	LastTurnRetry        *timelineRef `json:"lastTurnRetry,omitempty"`
	LastChildRetry       *timelineRef `json:"lastChildRetry,omitempty"`
	LimitWaitChecks      int          `json:"limitWaitChecks"`
	RetainedChecks       int          `json:"retainedChecks"`
	HeldChecks           int          `json:"heldChecks"`
	NotificationWarnings int          `json:"notificationWarnings"`
}

func timelineCommand(e env, args []string) int {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Never echo parser/config values in this redacted view.
	path := fs.String("c", "", configFlagHelp("configuration used only to locate state_dir"))
	dir := fs.String("state-dir", "", "read this directory without loading configuration")
	asJSON := fs.Bool("json", false, "print the versioned diagnostic report")
	limit := fs.Int("limit", 40, "newest events to display (1-320); summaries use all retained evidence")
	stale := fs.Duration("stale-after", defaultStaleAfter, "age at which historical evidence is stale")
	kind := fs.String("kind", "", "show only these comma-separated event kinds: "+strings.Join(timelineKindNames, ", ")+"; moves selects "+strings.Join(timelineMoveKinds, ", "))
	target := fs.String("target", "", "show only events for this target: primary, a fallback id, or a recorded target key")
	since := fs.String("since", "", "show only events at or after this RFC 3339 time, or this long ago (for example 72h)")
	until := fs.String("until", "", "show only events at or before this RFC 3339 time, or this long ago")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("timeline: invalid arguments; see timeline --help")
		return exitUsage
	}
	explicitConfig, explicitDir := false, false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "c" {
			explicitConfig = true
		}
		if f.Name == "state-dir" {
			explicitDir = true
		}
	})
	if fs.NArg() != 0 || *limit < 1 || *limit > incident.MaxRecords+report.MaxDeliveries || *stale <= 0 || (explicitDir && (explicitConfig || *dir == "")) {
		e.errf("timeline: select -c or --state-dir, a limit of 1-320, and a positive stale-after; no positional arguments")
		return exitUsage
	}
	if *dir == "" {
		if !configDefault(e, "timeline", fs, path) {
			return exitUsage
		}
		cfg, err := config.Load(*path)
		if err != nil {
			e.errf("timeline: configuration unavailable; repair it or select --state-dir to inspect private records directly")
			return exitUsage
		}
		*dir = cfg.Loop.StateDir
	}
	now := time.Now().UTC()
	filter, ok := parseTimelineFilter(*kind, *target, *since, *until, now)
	if !ok {
		e.errf("timeline: --kind takes %s or moves; --since and --until take an RFC 3339 time or a positive duration, with since not after until", strings.Join(timelineKindNames, ", "))
		return exitUsage
	}
	r := readTimeline(*dir, now, *stale, *limit, filter)
	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if enc.Encode(r) != nil {
			return exitFailed
		}
	} else {
		printTimeline(e.stdout, r)
	}
	// Historical failures and holds are valid diagnostic results. Unreadable
	// evidence is partial success, printed before returning a nonzero status.
	if r.Control.Availability == "unavailable" {
		return exitFailed
	}
	for _, s := range r.Sources {
		if s.Availability == "unavailable" {
			return exitFailed
		}
	}
	return exitOK
}

// parseTimelineFilter never echoes its arguments: a target alias is hashed and
// rejected values are reported only by the fixed usage text.
func parseTimelineFilter(kind, target, since, until string, now time.Time) (timelineFilter, bool) {
	f := timelineFilter{Kinds: []string{}}
	seen := map[string]bool{}
	for _, k := range strings.Split(kind, ",") {
		k = strings.TrimSpace(k)
		names := []string{k}
		if k == "moves" {
			names = timelineMoveKinds
		} else if k == "" {
			if kind == "" {
				continue
			}
			return f, false
		} else if !containsString(timelineKindNames, k) {
			return f, false
		}
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				f.Kinds = append(f.Kinds, n)
			}
		}
	}
	if target != "" {
		f.TargetKey = incident.ResolveTargetKey(target)
	}
	for _, b := range []struct {
		text string
		at   **time.Time
	}{{since, &f.Since}, {until, &f.Until}} {
		if b.text == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, b.text)
		if err != nil {
			d, derr := time.ParseDuration(b.text)
			if derr != nil || d <= 0 {
				return f, false
			}
			at = now.Add(-d)
		}
		at = at.UTC()
		*b.at = &at
	}
	if f.Since != nil && f.Until != nil && f.Since.After(*f.Until) {
		return f, false
	}
	return f, true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (f timelineFilter) matches(e timelineEntry) bool {
	// A record that summarizes collapsed checks stands for their whole span,
	// whichever way the clock moved between them.
	first, last := e.At, e.At
	if e.Incident != nil && e.Incident.Collapsed != nil {
		if c := e.Incident.Collapsed; c.FirstAt.Before(first) {
			first = c.FirstAt
		}
		if c := e.Incident.Collapsed; c.LastAt.After(last) {
			last = c.LastAt
		}
	}
	if f.Since != nil && last.Before(*f.Since) || f.Until != nil && first.After(*f.Until) {
		return false
	}
	if len(f.Kinds) > 0 {
		found := false
		for _, k := range e.Kinds {
			found = found || containsString(f.Kinds, k)
		}
		if !found {
			return false
		}
	}
	if f.TargetKey == "" {
		return true
	}
	// Control saves and deliveries record no target, so a target filter hides them.
	if e.Incident == nil || e.Incident.Kind == "control" {
		return false
	}
	if e.Incident.TargetKey == f.TargetKey || e.Incident.TargetKeyAfter == f.TargetKey {
		return true
	}
	if ret := e.Incident.Returned; ret != nil && ret.FromKey == f.TargetKey {
		return true
	}
	for _, p := range e.Incident.Probes.Results {
		if p.TargetKey == f.TargetKey {
			return true
		}
	}
	if c := e.Incident.Collapsed; c != nil && containsString(c.ProbeTargetKeys, f.TargetKey) {
		return true
	}
	return false
}

// timelineReturned reports a check that returned the loop to the primary. A
// return is recorded only once it is saved, so the check's outcome does not
// matter, and a dry run, which saves its state, counts too.
func timelineReturned(r incident.Record) bool {
	return r.Kind == "tick" && r.Returned != nil
}

// timelineReturnReasons words why a check returned the loop to the primary.
var timelineReturnReasons = map[string]string{
	incident.ReturnRemoved:         "is no longer configured",
	incident.ReturnDisabled:        "is disabled",
	incident.ReturnNoCreatedThread: "has no created thread on record",
}

func timelineLimitWait(cause string) bool {
	return cause == "limit_wait" || cause == "limit_no_fallback" || cause == "limit_failover_disabled" || cause == "limit_refusal_unrecovered"
}

// timelineKinds labels a record from its typed fields. A dry run decided an
// action without executing it, so it is never labelled as a send, and as a
// move only for the return to the primary it saves.
func timelineKinds(r incident.Record) []string {
	var kinds []string
	entered := !r.DryRun && r.Outcome != "dry_run" && enteredTimelineHold(r.ControlBefore, r.ControlAfter)
	switch r.Kind {
	case "tick":
		kinds = append(kinds, "check")
		if r.DryRun || r.Outcome == "dry_run" {
			// The one move a dry run makes: it saves a return to the primary.
			if timelineReturned(r) {
				kinds = append(kinds, "return")
			}
			return append(kinds, "dry_run")
		}
		switch r.Action {
		case "wake", "nudge", "reprompt", "stop":
			kinds = append(kinds, r.Action)
		case "handoff", "switch_back":
			// A move needs evidence that the target changed. Schema-5 records carry
			// both targets; older ones can only offer a succeeded outcome.
			moved := r.TargetKeyAfter != r.TargetKey
			if r.TargetKeyAfter == "unavailable" {
				moved = r.Outcome == "succeeded"
			}
			if moved {
				kinds = append(kinds, r.Action)
			}
		}
		if timelineReturned(r) {
			kinds = append(kinds, "return")
		}
		// A summary stands for the probes folded into it as well as its own.
		if len(r.Probes.Results) > 0 || (r.Collapsed != nil && r.Collapsed.PassedProbes+r.Collapsed.FailedProbes > 0) {
			kinds = append(kinds, "probe")
		}
		if timelineLimitWait(r.Cause) {
			kinds = append(kinds, "limit_wait")
		}
		if r.LimitWaitReport != "none" {
			kinds = append(kinds, "limit_wait_report")
		}
		if r.TurnRetry != "none" {
			kinds = append(kinds, "turn_retry")
		}
		if r.Lanes != (incident.LaneWork{}) {
			kinds = append(kinds, "child_retry")
		}
		if r.RouteListChanged {
			kinds = append(kinds, "route_list")
		}
		if r.Refusal != "" || r.ModelFallback {
			kinds = append(kinds, "refusal")
		}
	case "control":
		kinds = append(kinds, "control")
		if c := r.ControlChange; c != nil && (c.OwnershipChanged || c.Operation == "daemon_takeover" || c.Operation == "transfer_begin" || c.Operation == "transfer_finish") {
			kinds = append(kinds, "ownership")
		}
	case "retarget":
		kinds = append(kinds, "retarget")
	case "probe":
		kinds = append(kinds, "probe")
	case "route_change":
		kinds = append(kinds, "route_change")
	}
	if entered {
		kinds = append(kinds, "hold")
	}
	if r.Collapsed != nil {
		kinds = append(kinds, "collapsed")
	}
	if kinds == nil {
		kinds = []string{}
	}
	return kinds
}

// timelineTarget names the fixed primary alias and shortens any other key. It
// cannot recover a fallback's configured alias from its hash.
func timelineTarget(key string) string {
	switch {
	case len(key) != 64:
		return "unavailable"
	case key == incident.TargetKey(state.Primary):
		return "primary"
	default:
		return "fallback " + key[:8]
	}
}

func readTimeline(dir string, now time.Time, stale time.Duration, limit int, filter timelineFilter) timelineReport {
	r := timelineReport{SchemaVersion: 9, ReadAt: now, Filter: filter, StaleAfter: stale.String(), Control: readTimelineControl(dir), Sources: []timelineSource{}, Events: []timelineEntry{}, NextSteps: []string{}}
	j, jerr := incident.Load(dir)
	deliveries, derr := report.LoadDeliveries(dir)
	r.Sources = append(r.Sources, timelineSource{Source: incident.FileName, Availability: historyAvailability(jerr, len(j.Records)), SchemaVersion: j.SchemaVersion, Retained: len(j.Records), Dropped: j.Dropped}, timelineSource{Source: report.DeliveryFile, Availability: historyAvailability(derr, len(deliveries.Records)), SchemaVersion: deliveries.SchemaVersion, Retained: len(deliveries.Records), Dropped: deliveries.Dropped})
	r.Retention = readTimelineRetention(j, jerr)
	observations, targetRecords, latestCheckWaits := 0, 0, false
	for _, rec := range j.Records {
		at, basis := rec.CheckStartedAt, "check_started_at"
		if rec.Kind == "control" && rec.ControlChange != nil {
			at, basis = rec.ControlChange.SaveObservedAt, "control_save_observed_at"
		}
		if rec.Kind == "retarget" {
			basis = "retarget_save_observed_at"
		}
		entry := timelineEntry{At: at, TimeBasis: basis, Source: incident.FileName, Sequence: rec.Sequence, Freshness: timelineFreshness(at, now, stale), Description: describeIncident(rec), Kinds: timelineKinds(rec), Incident: &rec}
		r.Events = append(r.Events, entry)
		if rec.TargetKeyAfter != "unavailable" {
			targetRecords++
		}
		if rec.Kind == "tick" {
			// Matches the limit_wait kind and count: a dry run is not a wait.
			latestCheckWaits = containsString(entry.Kinds, "limit_wait")
		}
		for _, k := range entry.Kinds {
			switch k {
			case "handoff", "switch_back", "return", "retarget":
				r.Summary.LastTargetMove = timelineReference(entry)
			case "limit_wait_report":
				r.Summary.LastLimitWaitReport = timelineReference(entry)
			case "turn_retry":
				r.Summary.LastTurnRetry = timelineReference(entry)
			case "child_retry":
				r.Summary.LastChildRetry = timelineReference(entry)
			case "limit_wait":
				r.Summary.LastLimitWait = timelineReference(entry)
				r.Summary.LimitWaitChecks++
				if rec.Collapsed != nil {
					r.Summary.LimitWaitChecks += int(rec.Collapsed.Count)
				}
			}
		}
		if rec.Collapsed != nil {
			r.Retention.CollapsedChecks += rec.Collapsed.Count
		}
		if rec.Kind == "tick" {
			r.Summary.LatestCheck = timelineReference(entry)
			r.Summary.RetainedChecks++
			if !rec.DryRun && rec.Outcome != "dry_run" {
				if rec.Observation.Availability == "observed" {
					observations++
				}
				if state := timelineGate(rec.ControlBefore); state != "enabled" && state != "unavailable" {
					r.Summary.HeldChecks++
					if rec.Collapsed != nil {
						// Collapsed checks are identical to the record that summarizes them.
						r.Summary.HeldChecks += int(rec.Collapsed.Count)
					}
				}
				if rec.Observation.Availability == "observed" && rec.Observation.State == "active" {
					r.Summary.LastObservedActive = timelineReference(entry)
				}
				switch rec.Action {
				case "wake", "nudge", "reprompt", "handoff", "switch_back":
					r.Summary.LastSendAction = timelineReference(entry)
				}
			}
		}
		if (rec.Kind == "tick" || rec.Kind == "control") && !rec.DryRun && rec.Outcome != "dry_run" && enteredTimelineHold(rec.ControlBefore, rec.ControlAfter) {
			r.Summary.LastHoldEntry = timelineReference(entry)
		}
	}
	for _, d := range deliveries.Records {
		entry := timelineEntry{At: d.UpdatedAt, TimeBasis: "delivery_last_updated_at", Source: report.DeliveryFile, Sequence: d.Sequence, Freshness: timelineFreshness(d.UpdatedAt, now, stale), Description: describeDelivery(d), Kinds: []string{"notification"}, Delivery: &d}
		r.Events = append(r.Events, entry)
		if d.Outcome != "http_accepted" {
			r.Summary.NotificationWarnings++
		}
	}
	// Cross-file order is a presentation aid, not a causal link. Equal timestamps
	// sort by source then sequence for reproducible output.
	sort.SliceStable(r.Events, func(i, j int) bool {
		a, b := r.Events[i], r.Events[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Sequence < b.Sequence
	})
	matched := []timelineEntry{}
	for _, e := range r.Events {
		if filter.matches(e) {
			matched = append(matched, e)
		}
	}
	r.Filter.Matched, r.Filter.Excluded = len(matched), len(r.Events)-len(matched)
	if j.Collapsed > r.Retention.CollapsedChecks {
		r.Retention.CollapsedPruned = j.Collapsed - r.Retention.CollapsedChecks
	}
	// Legacy drops carry no time, so any range may reach them.
	r.Filter.ReachesPrunedHistory = r.Retention.UntieredPruned > 0
	for _, tier := range r.Retention.Tiers {
		if tier.Pruned > 0 && (filter.Since == nil || tier.PrunedThrough == nil || !filter.Since.After(*tier.PrunedThrough)) {
			r.Filter.ReachesPrunedHistory = true
		}
	}
	r.Omitted = max(0, len(matched)-limit)
	r.Events = matched[r.Omitted:]
	r.Layers = []timelineLayer{
		{"supervisor_checks", historyAvailability(jerr, r.Summary.RetainedChecks), "Retained checks show past supervisor execution, including held checks; they do not prove present liveness or continuous scheduling."},
		{"agent_connectivity", historyAvailability(jerr, observations), "Use each observed adapter/source/category/code and retry field. Active can include provider retries; stream errors do not establish an internet root cause."},
		{"notifications", historyAvailability(derr, len(deliveries.Records)), "Attempts and HTTP responses are recorded separately. Application/human receipt is unverified; later acceptance does not erase earlier retained failures."},
		{"target_moves", historyAvailability(jerr, targetRecords), "Handoffs, switch-backs and manual retargets are recorded with hashed target aliases from history schema 5, and a return to the primary after a fallback is removed or disabled, or has no created thread on record, from schema 13. Older records and work moved to another conversation outside keepgoing are not recorded; run and chat identity is planned separately as run discovery."},
		{"scheduler", "unavailable", "No scheduler queried; registration, last scheduler result and next run are unavailable in this offline view."},
		{"supervisor_liveness", "unavailable", "No daemon endpoint or process queried; recent records are not a live heartbeat."},
		{"ci_waits", "unavailable", "No CI query or persisted CI-wait evidence read; provider errors and held checks are not evidence of a CI wait."},
		{"task_progress", "unavailable", "Active observations and send outcomes do not prove useful progress, task completion or acceptance."},
	}
	r.Limitations = []string{
		"Bounded sampled evidence, not a complete audit. Missing records do not prove nothing happened; dropped counts are per source and omitted counts are display-only.",
		"incidents.json reserves room for key events, ongoing conditions, activity and routine checks separately, so one cannot evict another. A run of identical checks that executed and attempted nothing keeps its first and latest check; the rest are collapsed into a count and time span on the latest, without their decision inputs or repeated probe results. Pruned records are gone: see retention for each tier's count and the time through which it is incomplete.",
		"Files are read independently without writer locks; concurrent changes can produce different snapshots. Current flags are not attributed to an older hold entry.",
		"Events sort by local observation timestamps, not provider occurrence or causal order. Clock changes, --now test ticks, concurrent writes and late delivery updates can reorder them; preserve source sequence numbers.",
		"Latest summary references use retained source sequence order and can refer to events hidden by --limit. Dry-run checks are labelled and excluded from active/send/hold summaries.",
		"Provider event time, network root cause, installed/running binary identity and delivery receipt remain unavailable. Build labels describe recorded commands only.",
		"Target keys correlate configured aliases only; they identify no run, chat or provider session. Config changes and reused directories can mix sessions under one key. Delivery records deliberately contain no endpoint or target identity.",
		"Filters select displayed events only; summaries, sources and layers describe all retained evidence. Control saves and deliveries record no target, so --target hides them. Probe rows cover probes run by a check and, from history schema 13, manual fallback checks.",
	}
	if r.Control.State != "enabled" && r.Control.State != "unavailable" {
		r.NextSteps = append(r.NextSteps, "Review current control flags and historical hold evidence. Resolve the cause before explicitly enabling/resuming; this view never clears a hold.")
	}
	if r.Control.Availability == "unavailable" || jerr != nil || derr != nil {
		r.NextSteps = append(r.NextSteps, "Inspect private state storage and permissions; evidence is incomplete. Do not discard holds or overwrite corrupt history to obtain an all-clear.")
	}
	if r.Summary.LatestCheck == nil || r.Summary.LatestCheck.Freshness != "recent" {
		r.NextSteps = append(r.NextSteps, "Check host clocks and the supervisor/scheduler execution context. Use doctor with an explicitly selected scheduler if needed; missing or stale records alone do not identify the cause.")
	}
	if latestCheckWaits {
		r.NextSteps = append(r.NextSteps, "The latest recorded check was waiting for a provider limit to reset. keepgoing sends no prompt while it waits. It requests one limit_wait report per wait when no handoff is possible and the reset is further away than errors.report_wait_after, and only if report.events includes limit_wait; see the last long limit-wait report above, or its absence. Decide whether to wait, retarget or review failover settings.")
	}
	if r.Summary.NotificationWarnings > 0 {
		r.NextSteps = append(r.NextSteps, "Inspect notification setup and the receiver before any manual resend. Held checks do not retry notifications; do not resume/re-complete just to resend an alert.")
	}
	r.NextSteps = append(r.NextSteps, "For current liveness use status; for setup or an explicitly selected BB scheduler use doctor. Those separate checks may query local services; timeline never does.")
	return r
}

func readTimelineRetention(j incident.Journal, err error) timelineRetention {
	ret := timelineRetention{MaxRecords: incident.MaxRecords, MaxBytes: incident.MaxBytes, Tiers: []timelineTier{}}
	if err != nil {
		return ret
	}
	tiered := uint64(0)
	for _, t := range []struct {
		name   string
		pruned incident.TierPruned
	}{{incident.TierKey, j.Pruned.Key}, {incident.TierCondition, j.Pruned.Condition}, {incident.TierActivity, j.Pruned.Activity}, {incident.TierRoutine, j.Pruned.Routine}} {
		row := timelineTier{Tier: t.name, Reserved: incident.Reserved(t.name), Pruned: t.pruned.Count, PrunedThrough: t.pruned.NewestAt}
		for _, rec := range j.Records {
			if rec.Tier == t.name {
				row.Retained++
			}
		}
		tiered += t.pruned.Count
		ret.Tiers = append(ret.Tiers, row)
	}
	if j.Dropped > tiered {
		ret.UntieredPruned = j.Dropped - tiered
	}
	return ret
}

func timelineReference(e timelineEntry) *timelineRef {
	return &timelineRef{Source: e.Source, Sequence: e.Sequence, At: e.At, Freshness: e.Freshness, Description: e.Description}
}
func historyAvailability(err error, count int) string {
	if err != nil {
		return "unavailable"
	}
	if count == 0 {
		return "not_recorded"
	}
	return "recorded"
}
func timelineFreshness(at, now time.Time, stale time.Duration) string {
	if at.After(now) {
		return "future"
	}
	if now.Sub(at) > stale {
		return "stale"
	}
	return "recent"
}
func timelineGate(c *incident.Controls) string {
	if c == nil {
		return "unavailable"
	}
	switch {
	case c.Stop:
		return "stop_requested"
	case !c.Enabled:
		return "disabled"
	case c.Paused:
		return "paused"
	case c.AwaitingUser:
		return "awaiting_user"
	case c.Completed:
		return "completed"
	default:
		return "enabled"
	}
}

// The normal control file contains private notes/ownership. Bound the read and
// project only flags/time; never emit raw errors or forward its other fields.
func readTimelineControl(dir string) timelineControl {
	unavailable := timelineControl{Availability: "unavailable", State: "unavailable"}
	p := filepath.Join(dir, control.FileName)
	info, err := snapshot.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return timelineControl{Availability: "not_recorded", State: "disabled", Flags: &incident.Controls{}}
	}
	const maxControlBytes = 64 * 1024
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxControlBytes {
		return unavailable
	}
	// Never block a concurrent control or state save (Windows).
	f, err := snapshot.Open(p)
	if err != nil {
		return unavailable
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxControlBytes+1))
	if err != nil || len(b) > maxControlBytes {
		return unavailable
	}
	var ctl *control.File
	if json.Unmarshal(b, &ctl) != nil || ctl == nil {
		return unavailable
	}
	c := timelineControl{Availability: "recorded", Flags: incident.Gate(ctl.Gate())}
	c.State = timelineGate(c.Flags)
	if !ctl.UpdatedAt.IsZero() {
		c.UpdatedAt = &ctl.UpdatedAt
	}
	return c
}

func describeIncident(r incident.Record) string {
	if r.Kind == "control" && r.ControlChange != nil {
		text := fmt.Sprintf("control %s saved: %s -> %s; recorded build %s; this is not provider or delivery evidence", r.ControlChange.Operation, timelineGate(r.ControlBefore), timelineGate(r.ControlAfter), r.Build)
		if r.ControlChange.OwnershipChanged {
			text += "; loop owner changed (identity withheld)"
		}
		if caller := r.ControlChange.Caller; caller != "" && caller != "unavailable" {
			text += "; run by " + map[string]string{"owner_thread": "the owning BB thread", "other_thread": "another BB thread", "terminal": "a terminal with no BB thread identity", "client": "a registered local client"}[caller]
		}
		if r.ControlChange.NewEpisode {
			text += "; a hold for a spent per-episode allowance was cleared, so a new error episode started: the re-prompt and nudge counts started over"
		}
		if r.ControlChange.Operation == "complete" && r.ControlBefore != nil && r.ControlAfter != nil && r.ControlBefore.AwaitingUser && !r.ControlAfter.AwaitingUser {
			text += "; an awaiting-user hold was outstanding and ended with the completion (status keeps its note until a resume)"
		}
		return text
	}
	if r.Kind == "retarget" {
		return fmt.Sprintf("manual retarget saved: target %s -> %s; recorded build %s; no probe, send or provider check is implied", timelineTarget(r.TargetKey), timelineTarget(r.TargetKeyAfter), r.Build)
	}
	if r.Kind == "route_change" && r.RouteChange != nil {
		c := r.RouteChange
		by := map[string]string{"owner_thread": "the owning BB thread", "terminal": "a person (no BB thread identity)"}[c.Caller]
		if by == "" {
			by = "an unrecorded caller"
		}
		what := map[string]string{
			"handoff_on":      "automatic handoff turned on",
			"handoff_off":     "automatic handoff turned off",
			"follow_standard": "the loop now follows the standard route list",
			"own":             "the loop now has its own list, copied from the standard route list",
		}[c.Operation]
		if what == "" {
			what = fmt.Sprintf("%s route %s in the loop's own list", c.Operation, timelineTarget(c.TargetKey))
		}
		return fmt.Sprintf("route list change saved: %s, by %s; recorded build %s; the loop uses it after its next configuration reload", what, by, r.Build)
	}
	if r.Kind == "probe" {
		return "manual fallbacks check (recorded build " + r.Build + ")" + describeProbes(r.Probes) + "; health saved; no session was observed and nothing was sent"
	}
	if r.Kind != "tick" {
		return "incident kind unavailable; no check or control save inferred"
	}
	text := fmt.Sprintf("%s check (recorded build %s): observation %s", r.Adapter, r.Build, r.Observation.Availability)
	o := r.Observation
	if o.Availability == "observed" {
		retry := o.Retry
		if retry == "expired" {
			retry = "expired (stale provider retry notice no longer honored; ordinary handling applied)"
		}
		text += fmt.Sprintf(" %s; error %s; retry %s; source %s; code %s", o.State, o.ErrorClass, retry, o.Source, o.ProviderCode)
		if o.HTTPStatus != nil {
			text += fmt.Sprintf("; provider HTTP %d", *o.HTTPStatus)
		}
		if o.ExitCode != nil {
			text += fmt.Sprintf("; provider exit %d", *o.ExitCode)
		}
	}
	if o.ErroredChildren > 0 {
		count := fmt.Sprintf("%d", o.ErroredChildren)
		if o.ChildrenUnverified {
			count = "at least " + count + " (child discovery was incomplete)"
		}
		text += fmt.Sprintf("; %s direct child thread(s) in error (keepgoing does not message children; it retries one only under bb.children.retry_errored, and stops one only for stop --lanes)", count)
	}
	text += fmt.Sprintf("; action %s; outcome %s; cause %s; controls %s -> %s", r.Action, r.Outcome, r.Cause, timelineGate(r.ControlBefore), timelineGate(r.ControlAfter))
	if r.TargetKeyAfter != "unavailable" {
		text += "; target " + timelineTarget(r.TargetKey)
		if r.TargetKeyAfter != r.TargetKey {
			text += " -> " + timelineTarget(r.TargetKeyAfter)
		}
	}
	if ret := r.Returned; ret != nil {
		why := timelineReturnReasons[ret.Reason]
		if why == "" {
			why = "could not be used (reason unavailable)"
		}
		text += fmt.Sprintf("; RETURNED TO THE PRIMARY: the loop was on %s, which %s, so this check ran on the primary", timelineTarget(ret.FromKey), why)
	}
	if r.RouteListChanged {
		text += "; ROUTE LIST: the standard route list changed since this loop's previous check, and this check used the new list (keepgoing routes list shows it)"
	}
	text += describeProbes(r.Probes)
	if r.NewThread {
		text += "; NEW THREAD: keepgoing created a BB thread for this handoff and left the earlier conversation as it was (`keepgoing runs` shows both references)"
	}
	if r.Action == "handoff" || r.Action == "switch_back" {
		if r.TargetKeyAfter != "unavailable" && r.TargetKeyAfter == r.TargetKey && !r.DryRun && r.Outcome != "dry_run" {
			text += "; the target did not change, so this is not a recorded move"
		}
	}
	if timelineLimitWait(r.Cause) && len(r.Decisions.Evaluations) > 0 {
		in := r.Decisions.Evaluations[len(r.Decisions.Evaluations)-1].Input
		text += fmt.Sprintf("; waiting for a provider limit: recorded reset in %s, failover eligible %t, fallback available %t; no prompt is sent while waiting", in.ResetIn.Round(time.Minute), in.FailoverEligible, in.FailoverAvailable)
	}
	switch r.Refusal {
	case "refused":
		text += "; REFUSAL: the model or provider declined the turn (counted once; a plain continue follows in the same session, never another route)"
	case "recovered":
		text += "; REFUSAL RECOVERED: a turn ended without a refusal after a continue"
	}
	if r.ModelFallback {
		text += "; MODEL FALLBACK: the provider fell back to another model after a refusal (a note only; keepgoing took no action)"
	}
	switch r.TurnRetry {
	case "retried":
		text += "; TURN RETRY: the failed turn was re-run by reference and no prompt was sent"
	case "queued":
		text += "; TURN RETRY: a retry of the failed turn was queued by reference and no prompt was sent"
	case "nothing_to_retry":
		text += "; TURN RETRY: the provider reported no failed turn, so the prompt was sent instead"
	case "not_in_error":
		text += "; TURN RETRY: the provider reported no failed turn and the session was no longer in error, so nothing was sent and no attempt was counted"
	case "queue_unconfirmed":
		text += "; TURN RETRY: the provider kept answering that a retry was already queued while reporting nothing queued, so the prompt was sent instead"
	case "unsupported":
		text += "; TURN RETRY: this provider version cannot retry a turn, so the prompt was sent instead"
	case "already_queued":
		text += "; TURN RETRY: a retry of the failed turn was already waiting, so nothing was sent and no attempt was counted"
	case "refused":
		text += "; TURN RETRY: the provider refused the retry or its result could not be read, so nothing was sent"
	}
	if l := r.Lanes; l != (incident.LaneWork{}) {
		text += fmt.Sprintf("; LANES: asked BB to retry the failed turn of %d lane(s) by reference, gave up on %d, put off %d for lack of time; lane recovery messaged, stopped and created no lane", l.Retried, l.GivenUp, l.Deferred)
	}
	switch r.LimitWaitReport {
	case "requested":
		text += "; LONG LIMIT WAIT: the reset was further away than the configured threshold and no handoff was possible, so one report was requested for this wait (delivery is in the notification records; receipt unverified). The loop was not held"
	case "delivery_failed":
		text += "; LONG LIMIT WAIT: the reset was further away than the configured threshold and no handoff was possible; the one report for this wait failed and is not retried. The loop was not held"
	case "not_subscribed":
		text += "; LONG LIMIT WAIT: the reset was further away than the configured threshold and no handoff was possible; nothing was sent because report.events does not include limit_wait. The loop was not held"
	}
	if r.DryRun || r.Outcome == "dry_run" {
		text += "; DRY RUN (no executed action)"
	}
	if c := r.Collapsed; c != nil {
		text += fmt.Sprintf("; COLLAPSED: %d identical earlier checks from %s to %s are summarized here", c.Count, c.FirstAt.Format(time.RFC3339), c.LastAt.Format(time.RFC3339))
		if c.PassedProbes > 0 || c.FailedProbes > 0 {
			text += fmt.Sprintf(", including %d passing and %d failed probe results, each repeating that candidate's previous retained result", c.PassedProbes, c.FailedProbes)
		}
		text += "; their decision inputs are not retained"
	}
	if r.Action == "await_user" && len(r.Decisions.Evaluations) > 0 {
		in := r.Decisions.Evaluations[len(r.Decisions.Evaluations)-1].Input
		text += fmt.Sprintf("; recorded decision inputs: re-prompts %d/%d, nudges %d/%d, deadline reached %t, send limit reached %t, no-progress limit reached %t", in.Reprompts, in.MaxReprompts, in.Nudges, in.MaxNudges, in.DeadlineReached, in.SendLimitReached, in.NoProgressReached)
	}
	return text
}
func describeProbes(p incident.Probes) string {
	text := ""
	for i, p := range p.Results {
		if i == 0 {
			text += "; probes:"
		} else {
			text += ","
		}
		result := "passed"
		if !p.OK {
			result = "failed (" + p.ErrorClass + ")"
		}
		text += fmt.Sprintf(" %s %s %s", timelineTarget(p.TargetKey), p.Tier, result)
	}
	if p.Dropped > 0 {
		text += fmt.Sprintf("; %d earlier probe result(s) not kept", p.Dropped)
	}
	return text
}

func describeDelivery(d report.Delivery) string {
	s := fmt.Sprintf("notification %s: %s; %d request attempts begun", d.Event, d.Outcome, len(d.Attempts))
	if len(d.Attempts) > 0 && d.Attempts[len(d.Attempts)-1].HTTPStatus != 0 {
		s += fmt.Sprintf("; webhook HTTP %d", d.Attempts[len(d.Attempts)-1].HTTPStatus)
	}
	return s + "; receipt unverified"
}

func printTimeline(w io.Writer, r timelineReport) {
	fmt.Fprintf(w, "keepgoing incident timeline (offline, read-only)\nread at: %s; stale after %s\ncurrent control: %s (%s; source control.json)\n", r.ReadAt.Format(time.RFC3339), r.StaleAfter, r.Control.State, r.Control.Availability)
	if r.Control.UpdatedAt != nil {
		fmt.Fprintf(w, "control saved at: %s (local timestamp)\n", r.Control.UpdatedAt.Format(time.RFC3339))
	}
	if r.Control.Flags != nil {
		c := r.Control.Flags
		fmt.Fprintf(w, "flags: enabled=%t stop=%t paused=%t awaitingUser=%t completed=%t\n", c.Enabled, c.Stop, c.Paused, c.AwaitingUser, c.Completed)
	}
	for _, s := range r.Sources {
		fmt.Fprintf(w, "source %s: %s; schema %d; retained %d; dropped %d\n", s.Source, s.Availability, s.SchemaVersion, s.Retained, s.Dropped)
	}
	printTimelineRetention(w, r.Retention)
	for _, item := range []struct {
		name string
		ref  *timelineRef
	}{{"latest recorded check", r.Summary.LatestCheck}, {"last recorded active observation", r.Summary.LastObservedActive}, {"last recorded send action (not proof of a sent message or task success)", r.Summary.LastSendAction}, {"last recorded hold entry (not attributed to current flags)", r.Summary.LastHoldEntry}, {"last recorded target move (handoff, switch-back, return to the primary or retarget)", r.Summary.LastTargetMove}, {"last recorded limit wait", r.Summary.LastLimitWait}, {"last recorded long limit-wait report (one per wait; not proof of receipt)", r.Summary.LastLimitWaitReport}, {"last recorded turn retry (a failed turn re-run by reference; not proof it succeeded)", r.Summary.LastTurnRetry}, {"last recorded lane work (failed child turns re-run by reference; not proof they succeeded)", r.Summary.LastChildRetry}} {
		if item.ref == nil {
			fmt.Fprintf(w, "%s: unavailable\n", item.name)
			continue
		}
		v := item.ref
		fmt.Fprintf(w, "%s: %s #%d at %s (%s)\n  %s\n", item.name, v.Source, v.Sequence, v.At.Format(time.RFC3339), v.Freshness, v.Description)
	}
	fmt.Fprintf(w, "retained checks: %d (plus %d collapsed into them); checks with a pre-existing hold: %d; checks waiting for a provider limit: %d; failed/unconfirmed notifications: %d\n", r.Summary.RetainedChecks, r.Retention.CollapsedChecks, r.Summary.HeldChecks, r.Summary.LimitWaitChecks, r.Summary.NotificationWarnings)
	for _, l := range r.Layers {
		fmt.Fprintf(w, "%s: %s; %s\n", l.Layer, l.Availability, l.Explanation)
	}
	if f := r.Filter; len(f.Kinds) > 0 || f.TargetKey != "" || f.Since != nil || f.Until != nil {
		fmt.Fprint(w, "\nfilter:")
		if len(f.Kinds) > 0 {
			fmt.Fprintf(w, " kinds %s;", strings.Join(f.Kinds, ","))
		}
		if f.TargetKey != "" {
			fmt.Fprintf(w, " target %s;", timelineTarget(f.TargetKey))
		}
		if f.Since != nil {
			fmt.Fprintf(w, " since %s;", f.Since.Format(time.RFC3339))
		}
		if f.Until != nil {
			fmt.Fprintf(w, " until %s;", f.Until.Format(time.RFC3339))
		}
		fmt.Fprintf(w, " matched %d of %d retained events (summaries above use all of them)", f.Matched, f.Matched+f.Excluded)
	}
	if r.Filter.ReachesPrunedHistory {
		fmt.Fprint(w, "\nNOTE: the selected range reaches history that retention pruned (see retention above); no event there does not mean nothing happened")
	}
	fmt.Fprintf(w, "\nevents (oldest to newest by local timestamp; %d hidden by limit):\n", r.Omitted)
	for _, ev := range r.Events {
		fmt.Fprintf(w, "%s [%s #%d; %s; %s; %s]\n  %s\n", ev.At.Format(time.RFC3339Nano), ev.Source, ev.Sequence, ev.TimeBasis, ev.Freshness, strings.Join(ev.Kinds, ","), ev.Description)
	}
	fmt.Fprintln(w, "\nlimitations:")
	for _, s := range r.Limitations {
		fmt.Fprintln(w, "- "+s)
	}
	fmt.Fprintln(w, "next steps:")
	for _, s := range r.NextSteps {
		fmt.Fprintln(w, "- "+s)
	}
}

func printTimelineRetention(w io.Writer, ret timelineRetention) {
	if len(ret.Tiers) == 0 {
		fmt.Fprintln(w, "retention incidents.json: unavailable")
		return
	}
	fmt.Fprintf(w, "retention incidents.json (at most %d records and %d KiB):", ret.MaxRecords, ret.MaxBytes/1024)
	for _, t := range ret.Tiers {
		fmt.Fprintf(w, " %s %d (reserved %d), pruned %d", t.Tier, t.Retained, t.Reserved, t.Pruned)
		if t.PrunedThrough != nil {
			fmt.Fprintf(w, " through %s", t.PrunedThrough.Format(time.RFC3339))
		}
		fmt.Fprint(w, ";")
	}
	fmt.Fprintf(w, " %d identical checks collapsed into retained summaries", ret.CollapsedChecks)
	if ret.CollapsedPruned > 0 {
		fmt.Fprintf(w, "; %d more were collapsed into records since pruned", ret.CollapsedPruned)
	}
	if ret.UntieredPruned > 0 {
		fmt.Fprintf(w, "; %d records were dropped by an older build with no tier or time recorded", ret.UntieredPruned)
	}
	fmt.Fprintln(w)
}

func enteredTimelineHold(before, after *incident.Controls) bool {
	if before == nil || after == nil {
		return false
	}
	return (before.Enabled && !after.Enabled) || (!before.Stop && after.Stop) || (!before.Paused && after.Paused) || (!before.AwaitingUser && after.AwaitingUser) || (!before.Completed && after.Completed)
}
