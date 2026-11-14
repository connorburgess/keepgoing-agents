package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Replacing the file that runs a loop's checks with this panel's own build
// (Versions phase 2a). Offered only by a panel started with
// --allow-changes, through a review and a confirmation, with an Undo. The
// source is always this panel's own file; nothing is downloaded. Nothing is
// restarted: the next scheduled check runs the new file. Refused whenever a
// daemon or service of the loop runs the file (a restart, which is 2b), on
// Windows, and for a file that is a symlink, not the user's, this panel's
// own, or inside a package manager's tree.

const (
	buildReplaceAction = "replace_build"
	buildUndoAction    = "undo_replace_build"
	// buildUpdateName is the private record of the last replacement, for
	// the page's "waiting for the next check" and for Undo.
	buildUpdateName    = "build-update.json"
	buildBackupsDir    = "upgrade-backups"
	buildBackupState   = "state"
	buildBackupFile    = "executable"
	buildKeepBackups   = 3
	buildChangeTimeout = 25 * time.Second
)

// The state directory's cap for a backup, and how long a running check is
// waited for. The wait fits in one request to the panel, whose replies are
// cut off after 30 seconds. Tests shorten both.
var (
	buildBackupLimit int64 = 64 << 20
	buildLockWait          = 10 * time.Second
)

// panelBuildActions are the build changes. They are taken only by the
// panel's own change endpoint, for one loop: never by client review or apply,
// and never for every loop at once.
var panelBuildActions = map[string]string{buildReplaceAction: "Replace the build", buildUndoAction: "Undo the replacement"}

// Stand-ins for tests: this panel's own file, and the file system's owner check.
var (
	buildSource = func() (string, error) {
		if checkExecutable() == "" {
			return "", errors.New("startup image unavailable")
		}
		return checkExecutable(), nil
	}
	buildOS          = runtime.GOOS
	buildOwnedByUser = fileOwnedByUser
)

// dockBuildChange is what the page offers for a loop's build. It never
// carries a path; the review names paths, only under --allow-changes.
type dockBuildChange struct {
	// Action is replace_build or undo_replace_build when one is offered.
	Action string `json:"action,omitempty"`
	Label  string `json:"label,omitempty"` // the button
	// Kind says how the builds compare: newer (this panel's release is
	// newer), unordered (which is newer is not known), or empty.
	Kind string `json:"kind,omitempty"`
	// Text is one sentence: why it is offered, why not, or where a
	// replacement stands.
	Text string `json:"text,omitempty"`
	// Pending: replaced, and no check has run the new file yet.
	Pending bool `json:"pending,omitempty"`

	updatedAt time.Time // when this panel replaced the file; zero if it has not
}

// buildUpdate is the private record of the last replacement.
type buildUpdate struct {
	Version   int                `json:"version"`
	At        time.Time          `json:"at"`
	Backup    string             `json:"backup"` // the backup directory's name
	Target    string             `json:"target"`
	Mode      uint32             `json:"mode"`
	FromLabel string             `json:"fromLabel"`
	ToLabel   string             `json:"toLabel"`
	FromSHA   string             `json:"fromSha"`
	ToSHA     string             `json:"toSha"`
	Identity  state.FileIdentity `json:"identity"`
}

// buildPackageTree names the package manager whose tree holds path, or "".
func buildPackageTree(path string) string {
	p := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	p = strings.TrimPrefix(p, "/system/volumes/data")
	lower := p
	home, _ := os.UserHomeDir()
	home = strings.ToLower(filepath.ToSlash(home))
	if home != "" {
		for _, shared := range []string{home + "/go/bin/", home + "/.local/bin/", home + "/.asdf/", home + "/.local/share/mise/"} {
			if strings.HasPrefix(p, shared) {
				return "its installation tool"
			}
		}
	}
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		resolved, e := filepath.EvalSymlinks(gobin)
		if e == nil && strings.HasPrefix(p, strings.ToLower(filepath.ToSlash(resolved))+"/") {
			return "its installation tool"
		}
	}
	switch {
	case strings.Contains(p, "/cellar/") || strings.HasPrefix(p, "/opt/homebrew/") || strings.HasPrefix(p, "/usr/local/homebrew/") || strings.Contains(p, "/.linuxbrew/"):
		return "Homebrew"
	case strings.HasPrefix(p, "/nix/") || (home != "" && strings.HasPrefix(p, home+"/.nix-profile/")):
		return "Nix"
	case strings.HasPrefix(p, "/snap/"):
		return "snap"
	case strings.Contains(lower, "/scoop/"):
		return "Scoop"
	case strings.Contains(lower, "/winget/") || strings.Contains(lower, "/windowsapps/"):
		return "winget"
	case strings.Contains(lower, "/chocolatey/"):
		return "Chocolatey"
	case strings.HasPrefix(p, "/usr/") || strings.HasPrefix(p, "/opt/") || strings.Contains(lower, "/program files"):
		return "the system's package manager"
	}
	return ""
}

// buildTilde shows a path with the home directory as ~.
func buildTilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if resolved, e := filepath.EvalSymlinks(home); e == nil {
			home = resolved
		}
		if p == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
			return "~" + string(filepath.Separator) + rest
		}
	}
	return p
}

// buildOrder says how this panel's build compares with the file's, for a
// replacement: newer, unordered, or a reason none is offered.
func buildOrder(panel, file string) (kind, why string) {
	panel, file = dockBuildLabel(panel), dockBuildLabel(file)
	switch {
	case !dockBuildKnown(panel) || strings.Contains(panel, "dirty"):
		return "", "This panel's build has no version recorded or was built with uncommitted changes, so it does not replace another."
	case file == "" || !dockBuildKnown(file) || strings.Contains(file, "dirty") || file == "unreadable":
		return "", "The build that ran its last check has no usable version, so it is not replaced from here."
	case file == panel:
		return "", ""
	}
	if o, ok := dockReleaseOrder(panel, file); ok {
		if o > 0 {
			return "newer", ""
		}
		return "", "Its checks run a newer release (" + file + ") than this panel's; this panel never replaces a file with an older build."
	}
	return "unordered", ""
}

// buildAssess reads only private metadata. Read-only readers carry no action.
func (b *buildSession) assess(entry registry.Entry, daemon daemonStatus, now time.Time) (dockBuildChange, *state.CheckExecutable) {
	c := dockBuildChange{}
	rec, err := state.LoadCheckExecutable(entry.StateDir)
	if b == nil {
		c.Text = "A panel started with changes allowed can replace a loop's own copy of the build."
		return c, rec
	}
	if b.agent {
		c.Text = "Build changes are unavailable to a panel started by an agent."
		return c, rec
	}
	if _, ok := clientConfig(entry); !ok {
		c.Text = "The configuration no longer matches this registered loop and state directory."
		return c, rec
	}
	if b.err != nil {
		c.Text = "This panel's running executable could not be opened and hashed at startup; build changes are unavailable."
		return c, rec
	}
	if u, e := loadBuildUpdate(b.recordDir(entry)); e != nil {
		c.Text = "The private replacement record could not be read."
		return c, rec
	} else if u != nil && now.Before(u.At.Add(7*24*time.Hour)) && !now.Before(u.At) {
		c.updatedAt = u.At
		c.Text = "Replaced with " + productName + " " + u.ToLabel + "; waiting for the next check to run it."
		c.Pending = true
		if rec != nil && rec.At.After(u.At) && rec.Label == u.ToLabel {
			c.Pending = false
			c.Text = "Its checks now run " + productName + " " + u.ToLabel + "."
		}
		if why := b.undoRefusal(entry, u, daemon); why != "" {
			c.Text = why
			return c, rec
		}
		c.Action, c.Label = buildUndoAction, "Undo the replacement"
		return c, rec
	}
	switch {
	case err != nil:
		c.Text = "The record of which file runs its checks could not be read."
	case rec == nil:
		c.Text = "Older builds never record which file runs their checks. Replace the loop's file by hand once with a build that records it before using this feature."
	default:
		if why := b.fileRefusal(entry, rec.Path, daemon); why != "" {
			c.Text = why
			return c, rec
		}
		if !buildIdentityMatches(rec.Path, rec.Identity) {
			c.Text = "The file has changed since its last check; its build is not known until the next check."
			return c, rec
		}
		kind, why := buildOrder(b.label, rec.Label)
		c.Text = why
		if kind != "" {
			c.Action, c.Kind = buildReplaceAction, kind
			c.Label = "Replace with this panel's build"
			c.Text = "Its checks run " + productName + " " + rec.Label + "; this panel can put its own build in that file's place."
			if kind == "unordered" {
				c.Label += "; which is newer is not known"
				c.Text = "Which build is newer is not known."
			}
		}
	}
	return c, rec
}

func buildIdentityMatches(path string, want state.FileIdentity) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	got, ok := state.IdentityOf(info)
	return ok && got == want
}

func (b *buildSession) fileRefusal(entry registry.Entry, path string, daemon daemonStatus) string {
	if buildOS == "windows" {
		return "On Windows the panel does not start; replace the file by hand when no check is running."
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "The file that ran its last check is no longer there."
	case err != nil:
		return "The file that runs its checks could not be looked at."
	case info.Mode()&os.ModeSymlink != 0:
		return "Its checks run a symbolic link; replace it by hand."
	case !info.Mode().IsRegular():
		return "The file is not a regular file."
	case !buildOwnedByUser(info):
		return "The file is not yours."
	}
	id, ok := state.IdentityOf(info)
	if !ok || id.Nlink != 1 {
		return "The file has hard links or its identity is unavailable; replace it by hand."
	}
	if b.source != nil {
		if own, e := b.source.Stat(); e == nil && os.SameFile(own, info) {
			return "Its checks run this panel's own file."
		}
	}
	target, e1 := filepath.EvalSymlinks(path)
	cfg, e2 := filepath.EvalSymlinks(entry.ConfigPath)
	if e1 != nil || e2 != nil {
		return "The file or configuration path could not be resolved."
	}
	rel, e := filepath.Rel(filepath.Dir(cfg), target)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "Shared locations (~/go/bin, ~/.local/bin, GOBIN, asdf or mise installs, and package trees) are updated with the tool that installed them. Only a file inside this loop's own configuration directory can be replaced."
	}
	if pm := buildPackageTree(target); pm != "" {
		return "The file belongs to " + pm + "; update it with that tool."
	}
	dirs, e := buildRelevantRegistries(b.registry)
	if e != nil {
		return "The relevant registered loops could not be checked."
	}
	var names []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		all, e := registry.Load(dir)
		if e != nil {
			return "The relevant registered loops could not be checked."
		}
		for _, other := range all.Entries {
			key := other.ID + "\x00" + other.ConfigPath + "\x00" + other.StateDir
			if seen[key] {
				continue
			}
			seen[key] = true
			if other.ID == entry.ID && other.ConfigPath == entry.ConfigPath && other.StateDir == entry.StateDir {
				continue
			}
			rec, e := state.LoadCheckExecutable(other.StateDir)
			if e != nil {
				return "Another registered loop's executable record could not be checked."
			}
			if rec != nil && (rec.Path == path || buildSameFile(rec.Path, info)) {
				names = append(names, other.Loop)
			}
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		return "Other registered loops name the same file: " + strings.Join(names, ", ") + ". Replace their shared installation by hand."
	}

	held, e := false, error(nil)
	if !b.daemonHeld {
		held, e = lock.Held(entry.StateDir, lock.Daemon)
	}
	if e != nil || held || daemon.State == "responsive" {
		return "A daemon of this loop is running or its lock cannot be checked; replacing its file needs a restart."
	}
	if p, exists, e := readServicePlan(localServiceHost(), entry.StateDir); exists && (e != nil || p.Executable == path || buildSameFile(p.Executable, info)) {
		return "A service of this loop runs this file; replacing it needs a restart."
	}
	return ""
}
func buildSameFile(path string, info os.FileInfo) bool {
	s, e := os.Stat(path)
	return e == nil && os.SameFile(s, info)
}
func buildHash(ctx context.Context, path string) (string, error) {
	sum, st := serviceBinaryDigest(ctx, path)
	if st != "available" {
		return "", errors.New("file unreadable")
	}
	return hex.EncodeToString(sum[:]), nil
}

type buildReviewed struct {
	preview   panelPreview
	digest    string
	targetSHA string
}

func (b *buildSession) review(ctx context.Context, entry registry.Entry, l panelLoop, ctl control.File, action string, daemon daemonStatus, now time.Time) (buildReviewed, *panelResult) {
	offer, rec := b.assess(entry, daemon, now)
	refuse := func(text string) (buildReviewed, *panelResult) {
		return buildReviewed{}, &panelResult{Outcome: "refused", Text: text + " Nothing was changed."}
	}
	if offer.Action != action {
		return refuse(offer.Text)
	}
	p := panelPreview{Action: action, Key: dockKey(entry.ID), ExpiresAt: now.Add(panelHoldFormTTL), Lines: []dockText{}, Lost: "The file change could not be confirmed. Check the loop before retrying.", Refused: "Nothing was changed."}
	add := func(a ...any) { p.Lines = append(p.Lines, txt(a...)) }
	var facts any
	var targetSHA string
	if action == buildUndoAction {
		u, e := loadBuildUpdate(b.recordDir(entry))
		if e != nil || u == nil {
			return refuse("The private replacement record is unavailable.")
		}
		p.Title = "Undo the replacement for " + l.Name + "?"
		p.Confirm = "Restore only the file"
		add("Restores ", buildTilde(u.Target), " from the file backed up at ", tm(u.At, "clock"), ".")
		add("Only the binary is restored. Controls, pause, stop, sends, goals, daemon and service records, and history are left as they are. Nothing is restarted.")
		add("The saved configuration and state are for manual recovery only; restoring them by hand discards changes made since the backup.")
		facts = *u
	} else {
		from, e := buildHash(ctx, rec.Path)
		targetSHA = from
		if e != nil {
			return refuse("The file could not be read.")
		}
		if _, e = b.sourceBytes(); e != nil {
			return refuse("This panel's retained executable changed or could not be read.")
		}
		if from == b.sha {
			return refuse("The file already matches this panel's build.")
		}
		p.Title = "Replace the build that runs " + l.Name + "'s checks?"
		p.Confirm = "Replace the file"
		add("Replaces ", buildTilde(rec.Path), " (", rec.Label, ") with this panel's running build, ", buildTilde(b.path), " (", b.label, ").")
		if offer.Kind == "unordered" {
			add("Which build is newer is not known. Continue only if this is the build you want.")
		}
		add("First the configuration is loaded; then the binary, configuration and state are backed up in the panel's private directory. The newest three backups and any backup needed for a live Undo are kept.")
		add("Nothing is restarted. Undo restores only the binary, is available for seven days, and refuses if a record format changed. Manual state recovery discards later changes.")
		facts = struct {
			Rec           state.CheckExecutable
			From, To, Ctl string
		}{*rec, from, b.sha, controlDigest(ctl)}
	}
	add("A running check is waited for, up to " + dockSpan(buildLockWait) + ". A running daemon or service refuses this change.")
	data, _ := json.Marshal(facts)
	sum := sha256.Sum256(data)
	return buildReviewed{preview: p, digest: hex.EncodeToString(sum[:]), targetSHA: targetSHA}, nil
}

func (b *buildSession) apply(ctx context.Context, entry registry.Entry, action, digest string, daemon daemonStatus, now func() time.Time) panelResult {
	ctl, why := panelReadControl(entry.StateDir)
	if why != "" {
		return panelResult{Outcome: "refused", Text: why}
	}
	check := func() bool {
		r, ref := b.review(ctx, entry, panelLoop{}, ctl, action, daemon, now())
		return ref == nil && r.digest == digest
	}
	if !check() {
		return panelResult{Outcome: "review_again", Text: "The loop or file is no longer as reviewed. Nothing was changed. Review again.", Retry: true}
	}
	{
		if _, ok := clientConfig(entry); !ok {
			return panelResult{Outcome: "refused", Text: "Its configuration does not match this registered loop and state directory. Nothing was changed."}
		}
	}
	lk, e := buildWaitForChecks(ctx, entry.StateDir)
	if e != nil {
		return panelResult{Outcome: "refused", Text: "A check is running; try again when it finishes. Nothing was changed.", Retry: true}
	}
	defer lk.Release()
	// Hold the daemon lock through the swap, preventing a daemon from starting
	// between the fail-closed probe and rename, including with --offline.
	dl, e := lock.AcquireNamed(entry.StateDir, lock.Daemon)
	if e != nil {
		return panelResult{Outcome: "refused", Text: "A daemon is running or its lock cannot be held. Nothing was changed."}
	}
	defer dl.Release()
	// Our own daemon lock must not trip the review's external-holder probe.
	b2 := *b
	b2.daemonHeld = true
	ctl, why = panelReadControl(entry.StateDir)
	if why != "" {
		return panelResult{Outcome: "refused", Text: why}
	}
	{
		if _, ok := clientConfig(entry); !ok {
			return panelResult{Outcome: "refused", Text: "Its configuration changed or no longer matches the registered loop and state directory. Nothing was changed."}
		}
	}

	r, ref := b2.review(ctx, entry, panelLoop{}, ctl, action, daemon, now())
	if ref != nil || r.digest != digest {
		return panelResult{Outcome: "review_again", Text: "The loop changed while this waited. Nothing was changed. Review again.", Retry: true}
	}
	if action == buildUndoAction {
		return b2.undo(ctx, entry, now())
	}
	return b2.replace(ctx, entry, now(), r.targetSHA)
}

func buildWaitForChecks(ctx context.Context, stateDir string) (*lock.Lock, error) {
	deadline := time.Now().Add(buildLockWait)
	for {
		lk, e := lock.Acquire(stateDir)
		if e == nil {
			return lk, nil
		}
		if !errors.Is(e, lock.ErrHeld) || time.Now().After(deadline) {
			return nil, e
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (b *buildSession) replace(ctx context.Context, entry registry.Entry, now time.Time, expected ...string) panelResult {
	rec, e := state.LoadCheckExecutable(entry.StateDir)
	if e != nil || rec == nil {
		return panelResult{Outcome: "refused", Text: "Executable record unavailable. Nothing was changed."}
	}
	data, e := b.sourceBytes()
	if e != nil {
		return panelResult{Outcome: "refused", Text: "The retained source is unavailable. Nothing was changed."}
	}
	// Open and verify the target directory and target before backing up. Both
	// backup and swap use this descriptor, never a later pathname resolution.
	target, e := openBuildTarget(rec.Path, rec.Identity, entry.ConfigPath)
	if e != nil {
		return panelResult{Outcome: "refused", Text: "The target changed. Nothing was changed."}
	}
	defer target.close()
	old, e := target.read()
	if e != nil {
		return panelResult{Outcome: "refused", Text: "The target could not be read. Nothing was changed."}
	}
	sum := sha256.Sum256(old)
	if len(expected) > 0 && expected[0] != hex.EncodeToString(sum[:]) {
		return panelResult{Outcome: "refused", Text: "The target changed after review. Nothing was changed."}
	}
	backup, why := b.backUp(entry, old, now)
	if why != "" {
		return panelResult{Outcome: "refused", Text: why + " Nothing was changed."}
	}
	u := buildUpdate{Version: 1, At: now.UTC(), Backup: filepath.Base(backup), Target: rec.Path, Mode: uint32(target.mode.Perm()), FromLabel: rec.Label, ToLabel: b.label, FromSHA: hex.EncodeToString(sum[:]), ToSHA: b.sha}
	if e = target.swap(ctx, data); e != nil {
		os.RemoveAll(backup)
		return panelResult{Outcome: "refused", Text: "The file could not be replaced. Nothing was changed."}
	}
	info, e := target.stat()
	if e != nil {
		return panelResult{Outcome: "uncertain", Text: "The file was replaced but its identity could not be read. Recover by hand from the panel's private backup."}
	}
	u.Identity, _ = state.IdentityOf(info)
	if e = saveBuildUpdate(b.recordDir(entry), u); e != nil {
		return panelResult{Outcome: "uncertain", Text: "The file was replaced but Undo could not be recorded. The private backup is kept for manual recovery."}
	}
	buildHistory(entry.StateDir, "replace_build")
	buildPrune(filepath.Join(b.recordDir(entry), buildBackupsDir), u.Backup)
	return panelResult{Outcome: "done", Text: "Replaced with " + productName + " " + u.ToLabel + ". Nothing was restarted; the next scheduled check runs it."}
}

func (b *buildSession) undo(ctx context.Context, entry registry.Entry, now time.Time) panelResult {
	u, e := loadBuildUpdate(b.recordDir(entry))
	if e != nil || u == nil {
		return panelResult{Outcome: "refused", Text: "Private replacement record unavailable. Nothing was changed."}
	}
	old, e := os.ReadFile(filepath.Join(b.recordDir(entry), buildBackupsDir, u.Backup, buildBackupFile))
	sum := sha256.Sum256(old)
	if e != nil || hex.EncodeToString(sum[:]) != u.FromSHA {
		return panelResult{Outcome: "refused", Text: "The backup changed. Nothing was changed."}
	}
	target, e := openBuildTarget(u.Target, u.Identity, entry.ConfigPath)
	if e != nil {
		return panelResult{Outcome: "refused", Text: "The file changed. Nothing was changed."}
	}
	defer target.close()
	current, e := target.read()
	sumCurrent := sha256.Sum256(current)
	if e != nil || hex.EncodeToString(sumCurrent[:]) != u.ToSHA {
		return panelResult{Outcome: "refused", Text: "The target changed after review. Nothing was changed."}
	}
	if e = target.swap(ctx, old); e != nil {
		return panelResult{Outcome: "refused", Text: "The file could not be restored. Nothing was changed."}
	}
	// No loop record is written, removed or restored by Undo. Invalidate only
	// the panel's private receipt. A failed removal is safe: identity mismatch
	// makes a later attempt refuse rather than overwriting another change.
	_ = snapshot.Remove(filepath.Join(b.recordDir(entry), buildUpdateName))
	return panelResult{Outcome: "done", Text: "Restored only the file backed up at " + u.At.UTC().Format(time.RFC3339) + ". Nothing was restarted."}
}
func buildHistory(stateDir, operation string) {
	_, _ = updateControlFromAnnotated(stateDir, operation, panelChangeOrigin, func(*control.File) error { return nil }, nil)
}

// buildBackUp copies the file, the configuration and the state directory into
// a new private backup directory. It returns the directory, or why not.
func (b *buildSession) backUp(entry registry.Entry, old []byte, now time.Time) (string, string) {
	root := filepath.Join(b.recordDir(entry), buildBackupsDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "The backup folder could not be made."
	}
	size := int64(0)
	if err := filepath.WalkDir(entry.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path == root {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && !buildSkipState(d.Name()) {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		if size > buildBackupLimit {
			return errBuildTooLarge
		}
		return nil
	}); err != nil {
		if errors.Is(err, errBuildTooLarge) {
			return "", fmt.Sprintf("Its state directory is over %d MiB, so it is not backed up and the file is not replaced. Replace it by hand: copy the state directory somewhere safe, then put this panel's file in place of the old one while no check is running.", buildBackupLimit>>20)
		}
		return "", "Its state directory could not be read for the backup."
	}
	dir := filepath.Join(root, now.UTC().Format("20060102T150405.000000000Z"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", "The backup folder could not be made."
	}
	fail := func(why string) (string, string) {
		os.RemoveAll(dir)
		return "", why
	}
	if err := buildWriteFile(filepath.Join(dir, buildBackupFile), old, 0o700); err != nil {
		return fail("The file could not be backed up.")
	}
	if err := buildCopyFile(entry.ConfigPath, filepath.Join(dir, "config.yaml"), 0o600); err != nil {
		return fail("Its configuration could not be backed up.")
	}
	if err := buildCopyTree(entry.StateDir, filepath.Join(dir, buildBackupState), root); err != nil {
		return fail("Its state directory could not be backed up.")
	}
	return dir, ""
}

var errBuildTooLarge = errors.New("state too large")

// buildSkipState excludes locks, legacy receipts and backup trees from manual
// recovery snapshots. Undo never restores this tree.
func buildSkipState(name string) bool {
	return strings.HasSuffix(name, ".lock") || name == buildUpdateName || name == buildUpdateName+".present" || name == buildBackupsDir
}

func buildCopyFile(src, dst string, mode os.FileMode) error {
	b, err := snapshot.ReadFile(src)
	if err != nil {
		return err
	}
	return buildWriteFile(dst, b, mode)
}

func buildWriteFile(dst string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// buildCopyTree copies a state directory's regular files and folders, skipping
// skip (the backups) and what buildSkipState names.
func buildCopyTree(src, dst, skip string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == skip || (path != src && buildSkipState(d.Name())) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o700)
		case d.Type().IsRegular():
			return buildCopyFile(path, to, 0o600)
		}
		return nil // sockets and links are not state
	})
}

// buildPrune keeps the newest backups.
func buildPrune(root string, protected ...string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // the names are times
	for len(names) > buildKeepBackups {
		if len(protected) == 0 || names[0] != protected[0] {
			os.RemoveAll(filepath.Join(root, names[0]))
		}
		names = names[1:]
	}
}

func saveBuildUpdate(stateDir string, u buildUpdate) error {
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return control.WritePrivateFile(filepath.Join(stateDir, buildUpdateName), b)
}

func loadBuildUpdate(stateDir string) (*buildUpdate, error) {
	path := filepath.Join(stateDir, buildUpdateName)
	info, err := snapshot.LstatRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<10 {
		return nil, errors.New("unreadable")
	}
	b, err := snapshot.ReadRecord(path)
	if err != nil {
		return nil, err
	}
	var u buildUpdate
	if state.StrictKeys(b, "version", "at", "backup", "target", "mode", "fromLabel", "toLabel", "fromSha", "toSha", "identity") != nil || state.StrictDecode(b, &u) != nil || state.StrictIdentity(b) != nil || u.Version != 1 || !validBuildBackup(u.Backup) || !state.CleanPath(u.Target) || u.At.IsZero() || !validBuildSHA(u.FromSHA) || !validBuildSHA(u.ToSHA) || u.Identity.Ino == 0 || u.Identity.Nlink != 1 || u.Mode & ^uint32(0777) != 0 {
		return nil, errors.New("unreadable")
	}
	return &u, nil
}
