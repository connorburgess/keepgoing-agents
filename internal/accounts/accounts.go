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
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

const MaxEntries = 64
const MaxBytes = 1 << 20
const FileName = "accounts.json"

var ErrInvalid = errors.New("accounts storage unavailable or invalid")
var ErrChanged = errors.New("accounts selection changed; review again")

type Observation struct {
	Revision string    `json:"revision"`
	Context  string    `json:"context"`
	At       time.Time `json:"observedAt"`
	Auth     string    `json:"auth"`
	Reason   string    `json:"reason"`
	Version  string    `json:"cliVersion"`
	Plan     string    `json:"plan"`
	Caller   string    `json:"caller"` // private actual caller, never projected
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
		if o.Revision != e.Revision || o.Context != e.Context || o.At.IsZero() || len(o.Caller) > 160 || !validAuth(o.Auth) || !validReason(o.Reason) || (o.Version != "" && !versionPattern.MatchString(o.Version)) || !validPlan(o.Plan) {
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
	case "", "max", "pro", "team", "enterprise", "free":
		return true
	}
	return false
}

// Context fingerprints relevant effective auth inputs without retaining values.
// It does not read credentials or attest provider identity.
func Context(f File, e Entry, env []string) string {
	return contextForOS(f, e, env, runtime.GOOS == "windows")
}
func contextForOS(f File, e Entry, env []string, windows bool) string {
	m := map[string]string{}
	for _, pair := range env {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		k = environmentKey(k, windows)
		// This identifier changes for each agent tool invocation, not the
		// selected authentication context. Keep it in the executed environment.
		if k == environmentKey("CODEX_TOOL_CALL_ID", windows) {
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
	enc := json.NewEncoder(h)
	_ = enc.Encode([]string{e.Provider, e.Mode, e.CLI, e.Directory, e.Workdir})
	for _, k := range keys {
		_ = enc.Encode([]string{k, m[k]})
	}
	return hex.EncodeToString(h.Sum(nil))
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
	if d.Decode(&f) != nil || d.Decode(new(any)) != io.EOF || f.SchemaVersion != 1 || !validToken(f.Salt) || f.Entries == nil || len(f.Entries) > MaxEntries {
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
	if f.Salt == "" {
		f.Salt = token()
	}
	if err = fn(&f); err != nil {
		return err
	}
	if len(f.Entries) > MaxEntries {
		return ErrInvalid
	}
	ids := map[string]bool{}
	for _, e := range f.Entries {
		if !validEntry(e) || ids[e.ID] {
			return ErrInvalid
		}
		ids[e.ID] = true
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
	Freshness  string     `json:"freshness"`
}
type Card struct {
	ID              string    `json:"id"`
	Label           string    `json:"label"`
	Provider        string    `json:"provider"`
	Selection       string    `json:"selection"`
	Auth            Evidence  `json:"auth"`
	Plan            Evidence  `json:"plan"`
	ConfiguredModel Evidence  `json:"configuredModel"`
	ObservedModel   Evidence  `json:"observedModel"`
	PreviousAuth    *Evidence `json:"previousAuth,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Uses            string    `json:"uses"`
	Identity        string    `json:"identity"`
}
type View struct {
	State    string `json:"state"`
	Complete bool   `json:"complete"`
	Cards    []Card `json:"cards"`
}

func evidence(state, value, source string, at time.Time, now time.Time) Evidence {
	fresh := "fresh"
	if at.After(now) || now.Sub(at) >= 5*time.Minute {
		fresh = "stale"
	}
	return Evidence{Text: authText(state), State: state, Value: value, Source: source, ObservedAt: &at, Freshness: fresh}
}
func Project(f File, now time.Time) View {
	v := View{State: "available", Complete: true, Cards: []Card{}}
	unavailable := Evidence{Text: "Unavailable", State: "unavailable", Freshness: "unavailable"}
	for _, e := range f.Entries {
		c := Card{ID: e.ID, Label: e.Label, Provider: e.Provider, Selection: e.Mode, Auth: Evidence{Text: "Not checked", State: "unchecked", Freshness: "unavailable"}, Plan: unavailable, ConfiguredModel: unavailable, ObservedModel: unavailable, Uses: "unavailable", Identity: "selected_context_only"}
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
				c.Plan = evidence("reported", o.Plan, observationSource(e.Provider, o.Version), o.At, now)
				c.Plan.Text = planText(o.Plan)
			}
		}
		if e.Last != nil && (e.Attempt == nil || e.Attempt.Auth == "unavailable") {
			x := evidence(e.Last.Auth, "", observationSource(e.Provider, e.Last.Version), e.Last.At, now)
			x.Freshness = "stale"
			c.PreviousAuth = &x
			if e.Last.Plan != "" {
				c.Plan = evidence("reported", e.Last.Plan, e.Provider+" previous status", e.Last.At, now)
				c.Plan.Text = planText(e.Last.Plan)
				c.Plan.Freshness = "stale"
			}
		}
		v.Cards = append(v.Cards, c)
	}
	return v
}
func ReadView(dir string, now time.Time) View {
	f, err := Load(dir)
	if err != nil {
		return View{State: "unavailable", Complete: false, Cards: []Card{}}
	}
	return Project(f, now)
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
		return "Login reported"
	case "reported_absent":
		return "Not logged in"
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

// ExecutionEnvironment applies the one supported account-directory override
// using the same name equivalence as os/exec (case insensitive on Windows).
// Other environment entries remain ephemeral and are passed through as before.
func ExecutionEnvironment(e Entry, env []string) []string {
	return executionEnvironmentForOS(e, env, runtime.GOOS == "windows")
}
func executionEnvironmentForOS(e Entry, env []string, windows bool) []string {
	if e.Mode != "explicit_directory" {
		return append([]string(nil), env...)
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
