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
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/report"
)

// Timeline is an offline diagnostic projection. It never runs an adapter,
// acquires a writer lock, or uses history to authorize supervision.
type timelineReport struct {
	SchemaVersion int              `json:"schemaVersion"`
	ReadAt        time.Time        `json:"readAt"`
	StaleAfter    string           `json:"staleAfter"`
	Control       timelineControl  `json:"control"`
	Sources       []timelineSource `json:"sources"`
	Summary       timelineSummary  `json:"summary"`
	Layers        []timelineLayer  `json:"layers"`
	Events        []timelineEntry  `json:"events"`
	Omitted       int              `json:"omitted"`
	Limitations   []string         `json:"limitations"`
	NextSteps     []string         `json:"nextSteps"`
}

type timelineControl struct {
	Availability string             `json:"availability"`
	State        string             `json:"state"`
	Flags        *incident.Controls `json:"flags,omitempty"`
	UpdatedAt    *time.Time         `json:"updatedAt,omitempty"`
}

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
	RetainedChecks       int          `json:"retainedChecks"`
	HeldChecks           int          `json:"heldChecks"`
	NotificationWarnings int          `json:"notificationWarnings"`
}

func timelineCommand(e env, args []string) int {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Never echo parser/config values in this redacted view.
	path := fs.String("c", "keepgoing.yaml", "configuration used only to locate state_dir")
	dir := fs.String("state-dir", "", "read this directory without loading configuration")
	asJSON := fs.Bool("json", false, "print the versioned diagnostic report")
	limit := fs.Int("limit", 40, "newest events to display (1-320); summaries use all retained evidence")
	stale := fs.Duration("stale-after", defaultStaleAfter, "age at which historical evidence is stale")
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
		cfg, err := config.Load(*path)
		if err != nil {
			e.errf("timeline: configuration unavailable; repair it or select --state-dir to inspect private records directly")
			return exitUsage
		}
		*dir = cfg.Loop.StateDir
	}
	r := readTimeline(*dir, time.Now().UTC(), *stale, *limit)
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

func readTimeline(dir string, now time.Time, stale time.Duration, limit int) timelineReport {
	r := timelineReport{SchemaVersion: 1, ReadAt: now, StaleAfter: stale.String(), Control: readTimelineControl(dir), Sources: []timelineSource{}, Events: []timelineEntry{}, NextSteps: []string{}}
	j, jerr := incident.Load(dir)
	deliveries, derr := report.LoadDeliveries(dir)
	r.Sources = append(r.Sources, timelineSource{Source: incident.FileName, Availability: historyAvailability(jerr, len(j.Records)), SchemaVersion: j.SchemaVersion, Retained: len(j.Records), Dropped: j.Dropped}, timelineSource{Source: report.DeliveryFile, Availability: historyAvailability(derr, len(deliveries.Records)), SchemaVersion: deliveries.SchemaVersion, Retained: len(deliveries.Records), Dropped: deliveries.Dropped})
	observations := 0
	for _, rec := range j.Records {
		at, basis := rec.CheckStartedAt, "check_started_at"
		if rec.Kind == "control" && rec.ControlChange != nil {
			at, basis = rec.ControlChange.SaveObservedAt, "control_save_observed_at"
		}
		entry := timelineEntry{At: at, TimeBasis: basis, Source: incident.FileName, Sequence: rec.Sequence, Freshness: timelineFreshness(at, now, stale), Description: describeIncident(rec), Incident: &rec}
		r.Events = append(r.Events, entry)
		if rec.Kind == "tick" {
			r.Summary.LatestCheck = timelineReference(entry)
			r.Summary.RetainedChecks++
			if !rec.DryRun && rec.Outcome != "dry_run" {
				if rec.Observation.Availability == "observed" {
					observations++
				}
				if state := timelineGate(rec.ControlBefore); state != "enabled" && state != "unavailable" {
					r.Summary.HeldChecks++
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
		entry := timelineEntry{At: d.UpdatedAt, TimeBasis: "delivery_last_updated_at", Source: report.DeliveryFile, Sequence: d.Sequence, Freshness: timelineFreshness(d.UpdatedAt, now, stale), Description: describeDelivery(d), Delivery: &d}
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
	r.Omitted = max(0, len(r.Events)-limit)
	r.Events = r.Events[r.Omitted:]
	r.Layers = []timelineLayer{
		{"supervisor_checks", historyAvailability(jerr, r.Summary.RetainedChecks), "Retained checks show past supervisor execution, including held checks; they do not prove present liveness or continuous scheduling."},
		{"agent_connectivity", historyAvailability(jerr, observations), "Use each observed adapter/source/category/code and retry field. Active can include provider retries; stream errors do not establish an internet root cause."},
		{"notifications", historyAvailability(derr, len(deliveries.Records)), "Attempts and HTTP responses are recorded separately. Application/human receipt is unverified; later acceptance does not erase earlier retained failures."},
		{"scheduler", "unavailable", "No scheduler queried; registration, last scheduler result and next run are unavailable in this offline view."},
		{"supervisor_liveness", "unavailable", "No daemon endpoint or process queried; recent records are not a live heartbeat."},
		{"ci_waits", "unavailable", "No CI query or persisted CI-wait evidence read; provider errors and held checks are not evidence of a CI wait."},
		{"task_progress", "unavailable", "Active observations and send outcomes do not prove useful progress, task completion or acceptance."},
	}
	r.Limitations = []string{
		"Bounded sampled evidence, not a complete audit. Missing records do not prove nothing happened; dropped counts are per source and omitted counts are display-only.",
		"Files are read independently without writer locks; concurrent changes can produce different snapshots. Current flags are not attributed to an older hold entry.",
		"Events sort by local observation timestamps, not provider occurrence or causal order. Clock changes, --now test ticks, concurrent writes and late delivery updates can reorder them; preserve source sequence numbers.",
		"Latest summary references use retained source sequence order and can refer to events hidden by --limit. Dry-run checks are labelled and excluded from active/send/hold summaries.",
		"Provider event time, network root cause, installed/running binary identity and delivery receipt remain unavailable. Build labels describe recorded commands only.",
		"Target keys correlate configured aliases only; retargeting, config changes and reused directories can mix sessions. Delivery records deliberately contain no endpoint or target identity.",
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
	if r.Summary.NotificationWarnings > 0 {
		r.NextSteps = append(r.NextSteps, "Inspect notification setup and the receiver before any manual resend. Held checks do not retry notifications; do not resume/re-complete just to resend an alert.")
	}
	r.NextSteps = append(r.NextSteps, "For current liveness use status; for setup or an explicitly selected BB scheduler use doctor. Those separate checks may query local services; timeline never does.")
	return r
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
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return timelineControl{Availability: "not_recorded", State: "disabled", Flags: &incident.Controls{}}
	}
	const maxControlBytes = 64 * 1024
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxControlBytes {
		return unavailable
	}
	f, err := os.Open(p)
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
		return fmt.Sprintf("control %s saved: %s -> %s; recorded build %s; this is not provider or delivery evidence", r.ControlChange.Operation, timelineGate(r.ControlBefore), timelineGate(r.ControlAfter), r.Build)
	}
	if r.Kind != "tick" {
		return "incident kind unavailable; no check or control save inferred"
	}
	text := fmt.Sprintf("%s check (recorded build %s): observation %s", r.Adapter, r.Build, r.Observation.Availability)
	o := r.Observation
	if o.Availability == "observed" {
		text += fmt.Sprintf(" %s; error %s; retry %s; source %s; code %s", o.State, o.ErrorClass, o.Retry, o.Source, o.ProviderCode)
		if o.HTTPStatus != nil {
			text += fmt.Sprintf("; provider HTTP %d", *o.HTTPStatus)
		}
		if o.ExitCode != nil {
			text += fmt.Sprintf("; provider exit %d", *o.ExitCode)
		}
	}
	text += fmt.Sprintf("; action %s; outcome %s; cause %s; controls %s -> %s", r.Action, r.Outcome, r.Cause, timelineGate(r.ControlBefore), timelineGate(r.ControlAfter))
	if r.DryRun || r.Outcome == "dry_run" {
		text += "; DRY RUN (no executed action)"
	}
	if r.Action == "await_user" && len(r.Decisions.Evaluations) > 0 {
		in := r.Decisions.Evaluations[len(r.Decisions.Evaluations)-1].Input
		text += fmt.Sprintf("; recorded decision inputs: re-prompts %d/%d, nudges %d/%d, deadline reached %t, send limit reached %t, no-progress limit reached %t", in.Reprompts, in.MaxReprompts, in.Nudges, in.MaxNudges, in.DeadlineReached, in.SendLimitReached, in.NoProgressReached)
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
	for _, item := range []struct {
		name string
		ref  *timelineRef
	}{{"latest recorded check", r.Summary.LatestCheck}, {"last recorded active observation", r.Summary.LastObservedActive}, {"last recorded send action (not proof of a sent message or task success)", r.Summary.LastSendAction}, {"last recorded hold entry (not attributed to current flags)", r.Summary.LastHoldEntry}} {
		if item.ref == nil {
			fmt.Fprintf(w, "%s: unavailable\n", item.name)
			continue
		}
		v := item.ref
		fmt.Fprintf(w, "%s: %s #%d at %s (%s)\n  %s\n", item.name, v.Source, v.Sequence, v.At.Format(time.RFC3339), v.Freshness, v.Description)
	}
	fmt.Fprintf(w, "retained checks: %d; checks with a pre-existing hold: %d; failed/unconfirmed notifications: %d\n", r.Summary.RetainedChecks, r.Summary.HeldChecks, r.Summary.NotificationWarnings)
	for _, l := range r.Layers {
		fmt.Fprintf(w, "%s: %s; %s\n", l.Layer, l.Availability, l.Explanation)
	}
	fmt.Fprintf(w, "\nevents (oldest to newest by local timestamp; %d hidden by limit):\n", r.Omitted)
	for _, ev := range r.Events {
		fmt.Fprintf(w, "%s [%s #%d; %s; %s]\n  %s\n", ev.At.Format(time.RFC3339Nano), ev.Source, ev.Sequence, ev.TimeBasis, ev.Freshness, ev.Description)
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

func enteredTimelineHold(before, after *incident.Controls) bool {
	if before == nil || after == nil {
		return false
	}
	return (before.Enabled && !after.Enabled) || (!before.Stop && after.Stop) || (!before.Paused && after.Paused) || (!before.AwaitingUser && after.AwaitingUser) || (!before.Completed && after.Completed)
}
