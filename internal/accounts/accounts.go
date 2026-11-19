// Package accounts stores selected execution contexts, never credentials.
// Its read-only projection performs no subprocesses and grants no loop authority.
package accounts

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/envvar"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

const MaxEntries = 64
const MaxBytes = 1 << 20
const FileName = "accounts.json"

var ErrInvalid = errors.New("accounts storage unavailable or invalid")
var ErrChanged = errors.New("accounts selection changed; review again")

type Observation struct {
	PlanReason string     `json:"planReason,omitempty"`
	PlanAt     *time.Time `json:"planObservedAt,omitempty"`
	Revision   string     `json:"revision"`
	Context    string     `json:"context"`
	At         time.Time  `json:"observedAt"`
	Auth       string     `json:"auth"`
	Reason     string     `json:"reason"`
	Version    string     `json:"cliVersion"`
	Plan       string     `json:"plan"`
	Caller     string     `json:"caller"` // private actual caller, never projected
}
type Entry struct {
	ID        string       `json:"id"`
	Label     string       `json:"label"`
	Provider  string       `json:"provider"`
	Mode      string       `json:"selection"`
	CLI       string       `json:"executable"`
	Directory string       `json:"accountDirectory"`
	Workdir   string       `json:"workdir"`
	Revision  string       `json:"revision"`
	Context   string       `json:"context"` // keyed fingerprint, not account identity
	UpdatedAt time.Time    `json:"updatedAt"`
	UpdatedBy string       `json:"updatedBy"` // actual BB caller, or empty for a shell without one
	Last      *Observation `json:"lastObservation"`
	Attempt   *Observation `json:"lastAttempt"`
}
type File struct {
	SchemaVersion int     `json:"schemaVersion"`
	Salt          string  `json:"salt"`
	Entries       []Entry `json:"entries"`
}

// Schema 1 remains writable for legacy-compatible catalogs. Schema 2 is a
// one-way write promotion for new plan metadata or tiers; strict old readers
// refuse the whole catalog. Reads never promote or rewrite a catalog.
func needsPlanSchema(f File) bool {
	for _, e := range f.Entries {
		for _, o := range []*Observation{e.Last, e.Attempt} {
			if o == nil {
				continue
			}
			if o.PlanAt != nil || o.PlanReason != "" {
				return true
			}
			switch o.Plan {
			case "", "max", "pro", "team", "enterprise", "free":
			default:
				return true
			}
		}
	}
	return false
}

func token() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func Text(s string, max int) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" || len(s) > max {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return false
		}
	}
	return true
}
func supported(p string) bool   { return p == "claude-code" || p == "codex" }
func validToken(s string) bool  { b, e := hex.DecodeString(s); return e == nil && len(b) == 16 }
func validDigest(s string) bool { b, e := hex.DecodeString(s); return e == nil && len(b) == 32 }
func validEntry(e Entry) bool {
	if !validToken(e.ID) || !validToken(e.Revision) || !validDigest(e.Context) || !Text(e.Label, 120) || e.UpdatedAt.IsZero() || len(e.UpdatedBy) > 160 {
		return false
	}
	switch e.Provider {
	case "claude-code", "codex", "cursor", "bb", "other":
	default:
		return false
	}
	switch e.Mode {
	case "inherited", "explicit_directory", "externally_managed":
	default:
		return false
	}
	if e.Mode == "explicit_directory" && (!supported(e.Provider) || !filepath.IsAbs(e.Directory) || !Text(e.Directory, 4096)) {
		return false
	}
	if e.Mode != "explicit_directory" && e.Directory != "" {
		return false
	}
	if supported(e.Provider) {
		if !filepath.IsAbs(e.CLI) || !Text(e.CLI, 4096) || !filepath.IsAbs(e.Workdir) || !Text(e.Workdir, 4096) || e.Mode == "externally_managed" {
			return false
		}
	} else if e.Mode != "externally_managed" || e.CLI != "" || e.Workdir != "" {
		return false
	}
	for _, o := range []*Observation{e.Last, e.Attempt} {
		if o == nil {
			continue
		}
		if o.Revision != e.Revision || o.Context != e.Context || o.At.IsZero() || len(o.Caller) > 160 || !validAuth(o.Auth) || !validReason(o.Reason) || (o.Version != "" && !versionPattern.MatchString(o.Version)) || !validPlan(o.Plan) || !validPlanReason(o.PlanReason) || (o.PlanAt != nil && (o.PlanAt.IsZero() || o.Plan == "" || o.PlanAt.After(o.At))) {
			return false
		}
	}
	if e.Last != nil && e.Last.Auth != "reported_present" && e.Last.Auth != "reported_absent" {
		return false
	}
	return true
}

var versionPattern = regexp.MustCompile(`^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}$`)

func validAuth(s string) bool {
	return s == "reported_present" || s == "reported_absent" || s == "unavailable"
}
func validReason(s string) bool {
	switch s {
	case "", "not_checked", "unsupported", "context_changed", "version_unavailable", "command_unavailable", "timeout", "cancelled", "status_unavailable", "schema_unavailable":
		return true
	}
	return false
}
func validPlan(s string) bool {
	switch s {
	case "", "max", "free", "go", "plus", "pro", "prolite", "promax", "team", "self_serve_business_prolite", "self_serve_business_usage_based", "business", "ent26", "enterprise_cbp_automation", "enterprise_cbp_usage_based", "enterprise", "edu", "edu_plus", "edu_pro":
		return true
	}
	return false
}

func validPlanReason(s string) bool {
	switch s {
	case "", "timeout", "cancelled", "status_unavailable", "schema_unavailable", "unsupported", "no_account", "api_key", "account_type", "not_recognized":
		return true
	}
	return false
}
func planEvidence(provider string, o *Observation, now time.Time) Evidence {
	at := o.At
	source := observationSource(provider, o.Version)
	if provider == "codex" {
		source = "codex account/read"
		if o.PlanAt != nil {
			at = *o.PlanAt
		}
	}
	p := evidence("reported", o.Plan, source, at, now)
	p.Text = planText(o.Plan)
	if provider == "codex" && o.PlanReason != "" {
		p.Freshness = "stale"
		p.Reason = o.PlanReason
	}
	return p
}

// Context fingerprints relevant effective auth inputs without retaining values.
// It does not read credentials or attest provider identity. The variables of
// the session that ran the command (envvar.SupervisorSession) are not inputs:
// they change between agent sessions and tool calls, and a check does not
// pass them to the provider.
func Context(f File, e Entry, env []string) string {
	return contextForOS(f, e, env, runtime.GOOS == "windows")
}
func contextForOS(f File, e Entry, env []string, windows bool) string {
	m := map[string]string{}
	for _, pair := range env {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || envvar.IsSupervisorSession(k) {
			continue
		}
		k = environmentKey(k, windows)
		// Tool invocation identity and the execution sandbox marker do not
		// select authentication. Keep both in the executed environment.
		if k == environmentKey("CODEX_TOOL_CALL_ID", windows) || k == environmentKey("CODEX_SANDBOX", windows) {
			continue
		}
		relevant := false
		for _, name := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
			relevant = relevant || k == environmentKey(name, windows)
		}
		if !windows {
			for _, name := range []string{"http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
				relevant = relevant || k == name
			}
		}
		for _, prefix := range []string{"ANTHROPIC_", "CLAUDE_", "CODEX_", "OPENAI_", "AWS_", "GOOGLE_", "AZURE_"} {
			relevant = relevant || strings.HasPrefix(k, environmentKey(prefix, windows))
		}
		if relevant {
			m[k] = v
		}
	}
	if e.Mode == "explicit_directory" {
		k := "CLAUDE_CONFIG_DIR"
		if e.Provider == "codex" {
			k = "CODEX_HOME"
		}
		m[environmentKey(k, windows)] = e.Directory
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	key, _ := hex.DecodeString(f.Salt)
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(contextDefinition + "\n"))
	enc := json.NewEncoder(h)
	_ = enc.Encode([]string{e.Provider, e.Mode, e.CLI, e.Directory, e.Workdir})
	for _, k := range keys {
		_ = enc.Encode([]string{k, m[k]})
	}
	sum := h.Sum(nil)[:24]
	return hex.EncodeToString(append(sum, contextTag(key, sum)...))
}

// contextDefinition names the inputs Context covers. A digest is 24 bytes of
// keyed fingerprint followed by an 8-byte keyed guard tag, so a
// binding recorded by an earlier build, whose digest is a plain 32-byte HMAC
// that also covered the caller's session variables, is recognizable without a
// new stored field that earlier builds would refuse.
const contextDefinition = "looptender accounts context 3"

// Retain the keyed tag family so definition-2 readers also recognize new
// bindings as protected and refuse them rather than treating them as pre-tag.
const contextTagDefinition = "looptender accounts context 2"

func contextTag(key, sum []byte) []byte {
	return contextTagForDefinition(key, sum, contextTagDefinition)
}

func contextTagForDefinition(key, sum []byte, definition string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(definition + " tag\n"))
	_, _ = h.Write(sum)
	return h.Sum(nil)[:8]
}

// protectedDefinition recognizes the keyed guard family. Exact comparison
// with Context distinguishes the current input definition from older ones.
func protectedDefinition(f File, digest string) bool {
	return taggedDefinition(f, digest, contextTagDefinition)
}

func taggedDefinition(f File, digest, definition string) bool {
	b, err := hex.DecodeString(digest)
	if err != nil || len(b) != 32 {
		return false
	}
	key, _ := hex.DecodeString(f.Salt)
	return hmac.Equal(b[24:], contextTagForDefinition(key, b[:24], definition))
}

// Definition-2 tagged bindings require an explicit selection update before
// use: silently treating them as pre-tag legacy would discard their drift guard.
//
// Bound returns the context a check records for e under env, and whether e's
// binding accepts it. A binding computed under the current definition must
// match exactly. A pre-tag binding recorded by an earlier build cannot be compared,
// because its digest covered the session variables of whichever session added
// the entry; it is accepted, and the check replaces it with the current
// digest, so every later check compares exactly. The entry's provider,
// executable, working directory and account directory are bound either way.
func Bound(f File, e Entry, env []string) (string, bool) {
	current := Context(f, e, env)
	return current, current == e.Context || !protectedDefinition(f, e.Context)
}
func checkDir(dir string) error {
	st, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !st.IsDir() || !config.OwnedByUser(st) || (runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0) {
		return ErrInvalid
	}
	return nil
}
func checkFile(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !config.PrivateFile(st) {
		return ErrInvalid
	}
	return nil
}
func Load(dir string) (File, error) {
	if checkDir(dir) != nil {
		return File{}, ErrInvalid
	}
	p := filepath.Join(dir, FileName)
	st, err := snapshot.LstatRecord(p)
	if errors.Is(err, os.ErrNotExist) {
		return File{SchemaVersion: 1, Entries: []Entry{}}, nil
	}
	if err != nil || !config.PrivateFile(st) || st.Size() > MaxBytes {
		return File{}, ErrInvalid
	}
	opened, err := snapshot.OpenRecord(p)
	if err != nil {
		return File{}, ErrInvalid
	}
	defer opened.Close()
	// Validate the held snapshot, not just a pathname inspected before opening.
	// Record handles allow replacement on Windows without resetting missing data.
	info, err := opened.Stat()
	if err != nil || !config.PrivateFile(info) || info.Mode()&os.ModeSymlink != 0 || info.Size() > MaxBytes {
		return File{}, ErrInvalid
	}
	// Reject a pathname that became a link while it was opened. Do not compare
	// pathname file IDs: a concurrent atomic replacement may name another valid
	// generation while this checked handle retains its own complete snapshot.
	pathInfo, err := snapshot.LstatRecord(p)
	if err != nil || !config.PrivateFile(pathInfo) || pathInfo.Mode()&os.ModeSymlink != 0 {
		return File{}, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(opened, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return File{}, ErrInvalid
	}
	var f File
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil || d.Decode(new(any)) != io.EOF || (f.SchemaVersion != 1 && f.SchemaVersion != 2) || (f.SchemaVersion == 1 && needsPlanSchema(f)) || !validToken(f.Salt) || f.Entries == nil || len(f.Entries) > MaxEntries {
		return File{}, ErrInvalid
	}
	ids := map[string]bool{}
	for _, e := range f.Entries {
		if !validEntry(e) || ids[e.ID] {
			return File{}, ErrInvalid
		}
		ids[e.ID] = true
	}
	return f, nil
}

// Update serializes a small private record and refuses corruption or unsafe modes.
// The caller checks the destination's resolved ancestry first.
func Update(dir string, fn func(*File) error) error {
	if checkDir(dir) != nil {
		return ErrInvalid
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ErrInvalid
	}
	if checkFile(filepath.Join(dir, "accounts.lock")) != nil {
		return ErrInvalid
	}
	held, err := lock.AcquireNamed(dir, "accounts")
	if err != nil {
		return err
	}
	defer held.Release()
	f, err := Load(dir)
	if err != nil {
		return err
	}
	loadedSchema := f.SchemaVersion
	if f.Salt == "" {
		f.Salt = token()
	}
	if err = fn(&f); err != nil {
		return err
	}
	if (f.SchemaVersion != 1 && f.SchemaVersion != 2) || len(f.Entries) > MaxEntries {
		return ErrInvalid
	}
	ids := map[string]bool{}
	for _, e := range f.Entries {
		if !validEntry(e) || ids[e.ID] {
			return ErrInvalid
		}
		ids[e.ID] = true
	}
	if loadedSchema == 2 || needsPlanSchema(f) {
		f.SchemaVersion = 2
	}
	raw, err := json.Marshal(f)
	if err != nil || len(raw) > MaxBytes {
		return ErrInvalid
	}
	return control.WritePrivateFile(filepath.Join(dir, FileName), append(raw, '\n'))
}
func Find(f *File, id string) *Entry {
	for i := range f.Entries {
		if f.Entries[i].ID == id {
			return &f.Entries[i]
		}
	}
	return nil
}
func NewEntry(f File, e Entry, caller string, now time.Time, env []string) Entry {
	e.ID = token()
	e.Revision = token()
	e.UpdatedAt = now
	e.UpdatedBy = caller
	e.Last = nil
	e.Attempt = nil
	e.Context = Context(f, e, env)
	return e
}
func Revise(f File, old Entry, e Entry, caller string, now time.Time, env []string) Entry {
	e = NewEntry(f, e, caller, now, env)
	e.ID = old.ID
	return e
}

type Evidence struct {
	Text       string     `json:"text"`
	State      string     `json:"state"`
	Value      string     `json:"value,omitempty"`
	Source     string     `json:"source,omitempty"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	// Freshness is "stale" five minutes after the observation, and always for
	// a previous observation. It is kept for consumers; the page shows age as
	// AgeSeconds instead, since nothing re-checks on its own.
	Freshness string `json:"freshness"`
	// AgeSeconds is how long before the view was built the observation was
	// made; absent without an observation or when it is dated in the future.
	AgeSeconds *int64 `json:"ageSeconds,omitempty"`
	// CheckAgain is true when the observation is older than ConfirmAfter, so
	// a person who relies on it should check again to confirm it. It is a
	// hint, not a warning.
	CheckAgain bool `json:"checkAgain,omitempty"`
	// Missing says why there is no value; Text says it in words. Codex
	// plan-read failures also use their allowlisted reason here.
	Missing string `json:"missing,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Common reasons an Evidence holds no value.
const (
	// MissingNotReported: the provider's status command does not report it
	// (retained for older catalog consumers).
	MissingNotReported = "not_reported"
	// MissingNotRecognized: the status reported no plan among the broad
	// subscription types a check keeps; other values are discarded.
	MissingNotRecognized = "not_recognized"
	// MissingNoLogin: a plan is read only with a reported login.
	MissingNoLogin = "no_login"
	// MissingNotChecked: no check has run for the entry.
	MissingNotChecked = "not_checked"
	// MissingNotRecorded: the catalog does not record it. Models belong to
	// routes, and a check never calls a model.
	MissingNotRecorded = "not_recorded"
)

// Route association of a card, set by LinkRoutes.
const (
	RoutesNamed   = "named"   // at least one route names the entry
	RoutesNone    = "none"    // every route was read and none names it
	RoutesUnknown = "unknown" // not every route could be read, and none read names it
)

type Card struct {
	Quota           *QuotaView `json:"quota,omitempty"`
	ID              string     `json:"id"`
	Label           string     `json:"label"`
	Provider        string     `json:"provider"`
	Selection       string     `json:"selection"`
	Auth            Evidence   `json:"auth"`
	Plan            Evidence   `json:"plan"`
	ConfiguredModel Evidence   `json:"configuredModel"`
	ObservedModel   Evidence   `json:"observedModel"`
	Reason          string     `json:"reason,omitempty"`
	// FailedCheckAt is the time of the latest check when it did not produce a
	// login result and Auth and Plan show the observation before it.
	FailedCheckAt *time.Time `json:"failedCheckAt,omitempty"`
	Uses          string     `json:"uses"`
	Identity      string     `json:"identity"`
	// NeedsPerson is true only when the card shows something a person must
	// act on, named by Attention: no_login (the selected CLI reports no
	// login), check_failed (the latest check did not establish a result) or
	// context_changed (the effective context differs from the selection).
	// The age of an observation never sets it.
	NeedsPerson bool   `json:"needsPerson"`
	Attention   string `json:"attention,omitempty"`
	// Routes is one of the Routes* values when the caller compared the
	// card with the routes (LinkRoutes); empty when it did not.
	Routes string `json:"routes,omitempty"`
}
type View struct {
	State    string `json:"state"`
	Complete bool   `json:"complete"`
	Cards    []Card `json:"cards"`
}

// ConfirmAfter is the age past which a login or plan observation carries the
// hint to check again to confirm it.
const ConfirmAfter = 24 * time.Hour

func evidence(state, value, source string, at time.Time, now time.Time) Evidence {
	fresh := "fresh"
	if at.After(now) || now.Sub(at) >= 5*time.Minute {
		fresh = "stale"
	}
	e := Evidence{Text: authText(state), State: state, Value: value, Source: source, ObservedAt: &at, Freshness: fresh}
	if !at.After(now) {
		age := int64(now.Sub(at) / time.Second)
		e.AgeSeconds = &age
		e.CheckAgain = now.Sub(at) > ConfirmAfter
	}
	return e
}

// attention names what on a card needs a person, or "" when nothing does.
// A failed check outranks what an earlier check reported, as the earlier
// result is no longer current.
func attention(e Entry, c Card) string {
	switch {
	case c.Reason == "context_changed":
		return "context_changed"
	case e.Attempt != nil && e.Attempt.Auth == "unavailable" && c.Reason != "unsupported":
		return "check_failed"
	case c.Auth.State == "reported_absent":
		return "no_login"
	}
	return ""
}
func Project(f File, now time.Time) View {
	v := View{State: "available", Complete: true, Cards: []Card{}}
	unavailable := Evidence{Text: "Unavailable", State: "unavailable", Freshness: "unavailable"}
	notRecorded := Evidence{Text: "Not recorded", State: "unavailable", Freshness: "unavailable", Missing: MissingNotRecorded}
	for _, e := range f.Entries {
		c := Card{ID: e.ID, Label: e.Label, Provider: e.Provider, Selection: e.Mode, Auth: Evidence{Text: "Not checked", State: "unchecked", Freshness: "unavailable"}, Plan: unavailable, ConfiguredModel: notRecorded, ObservedModel: notRecorded, Uses: "unavailable", Identity: "selected_context_only"}
		if !supported(e.Provider) {
			c.Auth.State = "unavailable"
			c.Auth.Text = "Externally managed or unsupported"
			c.Reason = "unsupported"
			c.Identity = "unavailable"
		}
		if o := e.Attempt; o != nil {
			c.Auth = evidence(o.Auth, "", observationSource(e.Provider, o.Version), o.At, now)
			c.Reason = o.Reason
			if o.Auth == "unavailable" {
				c.Auth.Freshness = "unavailable"
			}
			if o.Plan != "" {
				c.Plan = planEvidence(e.Provider, o, now)
				c.Plan.Text = planText(o.Plan)
			}
		}
		// A check that did not produce a login result keeps the last recognized
		// observation, with its own time, and says when and why the latest
		// check failed. It is previous evidence, never a refreshed result.
		if e.Last != nil && (e.Attempt == nil || e.Attempt.Auth == "unavailable") {
			c.Auth = evidence(e.Last.Auth, "", observationSource(e.Provider, e.Last.Version), e.Last.At, now)
			c.Auth.Freshness = "stale"
			if e.Attempt != nil {
				at := e.Attempt.At
				c.FailedCheckAt = &at
			}
			if e.Last.Plan != "" {
				c.Plan = planEvidence(e.Provider, e.Last, now)
				c.Plan.Text = planText(e.Last.Plan)
				c.Plan.Freshness = "stale"
			}
		}
		c.Plan = missingPlan(e.Provider, c.Auth.State, c.Plan)
		if e.Provider == "codex" && c.Plan.Value == "" && e.Attempt != nil && e.Attempt.PlanReason != "" {
			c.Plan.Missing = e.Attempt.PlanReason
			c.Plan.Text = codexPlanMissing[e.Attempt.PlanReason]
		}
		c.Attention = attention(e, c)
		c.NeedsPerson = c.Attention != ""
		v.Cards = append(v.Cards, c)
	}
	return v
}

// missingPlan says why a card shows no plan, from what each provider's check
// reads (cmd/looptender/accounts.go, accountLogin). A check that did not
// establish a login leaves the plan unavailable.
func missingPlan(provider, auth string, p Evidence) Evidence {
	if p.Value != "" {
		return p
	}
	why := ""
	switch {
	case provider == "codex" && auth == "reported_present":
		return Evidence{Text: "Codex plan has not been read; check again.", State: "unavailable", Freshness: "unavailable", Missing: MissingNotChecked}
	case provider != "claude-code" && provider != "codex":
	case auth == "unchecked":
		why = MissingNotChecked
	case auth == "reported_present":
		why = MissingNotRecognized
	case auth == "reported_absent":
		why = MissingNoLogin
	}
	if why == "" {
		return p
	}
	return Evidence{Text: missingText[why], State: "unavailable", Freshness: "unavailable", Missing: why}
}

var codexPlanMissing = map[string]string{
	"timeout": "Codex plan read timed out.", "cancelled": "Codex plan read was cancelled.",
	"status_unavailable": "Codex plan read failed.", "schema_unavailable": "Codex plan response was not recognized.",
	"unsupported": "This Codex CLI does not support the plan read.", "no_account": "Codex reported no account for the plan read.",
	"api_key": "API key accounts have no ChatGPT subscription plan.", "account_type": "This Codex account type has no recognized ChatGPT plan.",
	"not_recognized": "Codex reported no recognized plan.",
}
var missingText = map[string]string{
	MissingNotReported:   "The status command does not report this value.",
	MissingNotRecognized: "Claude Code's status reported no recognized plan.",
	MissingNoLogin:       "No plan is read without a login.",
	MissingNotChecked:    "Not checked",
}

// LinkRoutes sets each card's Routes from the catalog IDs that routes name.
// complete is false when not every route could be read; a card that no
// route read names is then unknown rather than none.
func LinkRoutes(v *View, named map[string]bool, complete bool) {
	for i := range v.Cards {
		c := &v.Cards[i]
		switch {
		case named[c.ID]:
			c.Routes = RoutesNamed
		case complete:
			c.Routes = RoutesNone
		default:
			c.Routes = RoutesUnknown
		}
	}
}

func ReadView(dir string, now time.Time) View {
	f, err := Load(dir)
	if err != nil {
		return View{State: "unavailable", Complete: false, Cards: []Card{}}
	}
	v := Project(f, now)
	AttachQuota(&v, QuotaViews(dir, f, os.Environ(), now))
	return v
}

// CheckGuard prevents overlapping checks of one selection, without holding the
// catalog writer lock while an external command runs.
func CheckGuard(dir, id string) (*lock.Lock, error) {
	if !validToken(id) || checkDir(dir) != nil {
		return nil, ErrInvalid
	}
	if checkFile(filepath.Join(dir, "check-"+id+".lock")) != nil {
		return nil, ErrInvalid
	}
	return lock.AcquireNamed(dir, "check-"+id)
}

func authText(state string) string {
	switch state {
	case "reported_present":
		return "Selected CLI reports login"
	case "reported_absent":
		return "Selected CLI reports no login"
	case "unchecked":
		return "Not checked"
	default:
		return "Unavailable"
	}
}
func planText(plan string) string {
	if plan == "" {
		return "Unavailable"
	}
	return strings.ToUpper(plan[:1]) + plan[1:]
}

func observationSource(provider, version string) string {
	if version == "" {
		return provider + " status"
	}
	return provider + " CLI " + version + " status"
}

// ExecutionEnvironment drops the caller's session variables
// (envvar.SupervisorSession) and applies the one supported account-directory
// override using the same name equivalence as os/exec (case insensitive on
// Windows). Other environment entries remain ephemeral and pass through.
func ExecutionEnvironment(e Entry, env []string) []string {
	return executionEnvironmentForOS(e, env, runtime.GOOS == "windows")
}
func executionEnvironmentForOS(e Entry, env []string, windows bool) []string {
	// The provider's status command is not the session that ran the check.
	env = envvar.WithoutSupervisorSession(env)
	if e.Mode != "explicit_directory" {
		return env
	}
	key := "CLAUDE_CONFIG_DIR"
	if e.Provider == "codex" {
		key = "CODEX_HOME"
	}
	out := make([]string, 0, len(env)+1)
	for _, pair := range env {
		name, _, _ := strings.Cut(pair, "=")
		same := name == key
		if windows {
			same = environmentKey(name, true) == environmentKey(key, true)
		}
		if !same {
			out = append(out, pair)
		}
	}
	return append(out, key+"="+e.Directory)
}

// Go os/exec dedupEnvCase lowercases Windows names, then retains the last
// occurrence. Use that exact key equivalence for binding and overrides too.
func environmentKey(name string, windows bool) string {
	if windows {
		return strings.ToLower(name)
	}
	return name
}
