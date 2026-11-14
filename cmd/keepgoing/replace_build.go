package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
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
	buildUpdateName   = "build-update.json"
	buildBackupsDir   = "upgrade-backups"
	buildBackupState  = "state"
	buildBackupFile   = "executable"
	buildKeepBackups  = 3
	buildChangeTimout = 25 * time.Second
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
	buildSource      = os.Executable
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
	Version   int       `json:"version"`
	At        time.Time `json:"at"`
	Backup    string    `json:"backup"` // the backup directory's name
	Target    string    `json:"target"`
	Mode      uint32    `json:"mode"`
	FromLabel string    `json:"fromLabel"`
	ToLabel   string    `json:"toLabel"`
	FromSHA   string    `json:"fromSha"`
	ToSHA     string    `json:"toSha"`
}

// buildPackageTree names the package manager whose tree holds path, or "".
func buildPackageTree(path string) string {
	p := filepath.ToSlash(path)
	lower := strings.ToLower(p)
	home, _ := os.UserHomeDir()
	home = filepath.ToSlash(home)
	switch {
	case strings.Contains(p, "/Cellar/") || strings.HasPrefix(p, "/opt/homebrew/") || strings.HasPrefix(p, "/usr/local/Homebrew/") || strings.Contains(p, "/.linuxbrew/"):
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
	case !dockBuildKnown(panel) || strings.HasSuffix(panel, "-dirty"):
		return "", "This panel's build has no version recorded or was built with uncommitted changes, so it does not replace another."
	case file == "" || !dockBuildKnown(file) || strings.HasSuffix(file, "-dirty") || file == "unreadable":
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

// buildAssess says what may be offered for a loop now. It reads only the
// loop's private records and the file's metadata; it hashes nothing.
func buildAssess(stateDir string, daemon daemonStatus) (dockBuildChange, *state.CheckExecutable) {
	var c dockBuildChange
	if u, err := loadBuildUpdate(stateDir); err == nil && u != nil {
		c.Action, c.Label, c.updatedAt = buildUndoAction, "Undo the replacement", u.At
		rec, _ := state.LoadCheckExecutable(stateDir)
		if rec != nil && rec.At.After(u.At) && rec.Label == u.ToLabel {
			c.Text = "Its checks now run " + productName + " " + u.ToLabel + ", which this panel put in place of " + u.FromLabel + "."
		} else {
			c.Pending = true
			c.Text = "Replaced with " + productName + " " + u.ToLabel + "; waiting for the next check to run it."
		}
		return c, rec
	}
	rec, err := state.LoadCheckExecutable(stateDir)
	switch {
	case err != nil:
		c.Text = "The record of which file runs its checks could not be read, so it is not replaced from here."
		return c, nil
	case rec == nil:
		c.Text = "Which file runs its checks is not recorded yet; the next check records it."
		return c, nil
	}
	kind, why := buildOrder(version, rec.Label)
	if kind == "" {
		c.Text = why
		return c, rec
	}
	if why := buildFileRefusal(stateDir, rec.Path, daemon); why != "" {
		c.Text = why
		return c, rec
	}
	c.Action, c.Kind = buildReplaceAction, kind
	if kind == "newer" {
		c.Label = "Replace with this panel's build"
		c.Text = "Its checks run " + productName + " " + dockBuildLabel(rec.Label) + "; this panel can put its newer " + dockBuildLabel(version) + " in that file's place."
	} else {
		c.Label = "Replace with this panel's build; which is newer is not known"
		c.Text = "Its checks run " + productName + " " + dockBuildLabel(rec.Label) + " and this panel is " + dockBuildLabel(version) + "; which of them is newer is not known."
	}
	return c, rec
}

// buildFileRefusal is why a file may not be replaced from here, or "".
func buildFileRefusal(stateDir, path string, daemon daemonStatus) string {
	if buildOS == "windows" {
		return "On Windows a program's file cannot be replaced while it may be running, and keepgoing cannot tell whether a check is using it. Replace it by hand when no check is running."
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "The file that ran its last check is no longer there."
	case err != nil:
		return "The file that ran its last check could not be looked at."
	case info.Mode()&os.ModeSymlink != 0:
		return "Its checks run a symbolic link; replace the file it points to with the tool that made the link."
	case !info.Mode().IsRegular():
		return "The file that runs its checks is not a regular file."
	case !buildOwnedByUser(info):
		return "The file that runs its checks is not yours, so this panel does not replace it."
	}
	if pm := buildPackageTree(path); pm != "" {
		return "The file that runs its checks belongs to " + pm + "; update it with " + pm + "."
	}
	if self, err := buildSource(); err == nil {
		if s, err := os.Stat(self); err == nil && os.SameFile(s, info) {
			return "Its checks run this panel's own file."
		}
	}
	if daemon.State == "responsive" {
		return "A daemon of this loop is running; replacing its file needs a restart, which this panel cannot do yet."
	}
	if p, exists, err := readServicePlan(localServiceHost(), stateDir); exists && (err != nil || p.Executable == path || buildSameFile(p.Executable, info)) {
		return "A service of this loop runs this file; replacing it needs a restart, which this panel cannot do yet."
	}
	return ""
}

func buildSameFile(path string, info os.FileInfo) bool {
	s, err := os.Stat(path)
	return err == nil && os.SameFile(s, info)
}

// buildHash is a file's SHA-256, read with the service comparison's bounds.
func buildHash(ctx context.Context, path string) (string, error) {
	sum, st := serviceBinaryDigest(ctx, path)
	if st != "available" {
		return "", fmt.Errorf("the file could not be read (%s)", strings.ReplaceAll(st, "_", " "))
	}
	return hex.EncodeToString(sum[:]), nil
}

// buildReviewed is a reviewed build change: its words, and the facts its
// confirmation is bound to.
type buildReviewed struct {
	preview panelPreview
	digest  string
}

// buildReview words the review of a build change for one loop.
func buildReview(ctx context.Context, entry registry.Entry, l panelLoop, ctl control.File, action string, daemon daemonStatus, now time.Time) (buildReviewed, *panelResult) {
	offer, rec := buildAssess(entry.StateDir, daemon)
	if offer.Action != action {
		text := "This change is not offered for this loop now. Nothing was changed."
		if offer.Text != "" {
			text = offer.Text + " Nothing was changed."
		}
		return buildReviewed{}, &panelResult{Outcome: "refused", Text: text}
	}
	expires := now.Add(panelHoldFormTTL)
	p := panelPreview{Action: action, Key: dockKey(entry.ID), ExpiresAt: expires, Lines: []dockText{},
		Lost:    "keepgoing could not confirm whether the file was changed. Check the loop's Versions row before doing anything else.",
		Refused: "The panel refused this request before doing anything. Nothing was changed."}
	add := func(a ...any) { p.Lines = append(p.Lines, txt(a...)) }
	if action == buildUndoAction {
		u, err := loadBuildUpdate(entry.StateDir)
		if err != nil || u == nil {
			return buildReviewed{}, &panelResult{Outcome: "refused", Text: "The record of the replacement could not be read. Nothing was changed."}
		}
		cur, err := buildHash(ctx, u.Target)
		if err != nil || cur != u.ToSHA {
			return buildReviewed{}, &panelResult{Outcome: "refused", Text: "The file has changed since this panel replaced it, so Undo would overwrite someone else's change. Restore it by hand from " + buildTilde(filepath.Join(entry.StateDir, buildBackupsDir, u.Backup)) + ". Nothing was changed."}
		}
		p.Title = "Undo the replacement for " + l.Name + "?"
		p.Confirm = "Restore the file and its state"
		add("Puts back ", buildTilde(u.Target), " as it was before ", tm(u.At, "clock"), ": ", productName, " ", u.FromLabel, ".")
		add("Restores the loop's state from the same backup, made just before the replacement.")
		if rec != nil && rec.At.After(u.At) {
			add(txt("Checks have run on ", productName, " ", u.ToLabel, " since; the last at ", tm(rec.At, "clock"), ". What they recorded is replaced by the backed-up state, so a build that cannot read newer records is not left with them."))
		} else {
			add("No check has run the new file yet.")
		}
		add("Nothing is restarted; the next scheduled check runs the restored file. A check that is running now is waited for, up to " + dockSpan(buildLockWait) + ".")
		lastAt := time.Time{}
		if rec != nil {
			lastAt = rec.At
		}
		b, _ := json.Marshal(struct {
			U    buildUpdate
			Cur  string
			Last time.Time
			Ctl  string
		}{*u, cur, lastAt, controlDigest(ctl)})
		sum := sha256.Sum256(b)
		return buildReviewed{preview: p, digest: hex.EncodeToString(sum[:])}, nil
	}
	self, err := buildSource()
	if err != nil {
		return buildReviewed{}, &panelResult{Outcome: "refused", Text: "This panel's own file could not be found. Nothing was changed."}
	}
	from, err := buildHash(ctx, rec.Path)
	if err != nil {
		return buildReviewed{}, &panelResult{Outcome: "refused", Text: "The file that runs its checks could not be read. Nothing was changed."}
	}
	to, err := buildHash(ctx, self)
	if err != nil {
		return buildReviewed{}, &panelResult{Outcome: "refused", Text: "This panel's own file could not be read. Nothing was changed."}
	}
	if from == to {
		return buildReviewed{}, &panelResult{Outcome: "refused", Text: "The file that runs its checks is already the same as this panel's. Nothing was changed."}
	}
	p.Title = "Replace the build that runs " + l.Name + "'s checks?"
	p.Confirm = "Replace the file"
	if offer.Kind == "unordered" {
		p.Title = "Replace " + l.Name + "'s build with this panel's? Which is newer is not known."
	}
	add("Replaces " + buildTilde(rec.Path) + " (" + productName + " " + dockBuildLabel(rec.Label) + ") with this panel's own file, " + buildTilde(self) + " (" + productName + " " + dockBuildLabel(version) + ").")
	if offer.Kind == "unordered" {
		add("One of these is a development build, so which of them is newer is not known. Replace it only if you know this panel's build is the one you want.")
	}
	add("First its configuration is loaded with this build; if that fails, nothing is changed.")
	add("Then the file, the loop's configuration and its state directory are backed up to " + buildTilde(filepath.Join(entry.StateDir, buildBackupsDir)) + " (the newest " + fmt.Sprint(buildKeepBackups) + " are kept). A state directory over " + fmt.Sprint(buildBackupLimit>>20) + " MiB is refused.")
	add("A check that is running is waited for, up to " + dockSpan(buildLockWait) + ", and no check can start while the file is replaced.")
	add("Nothing is restarted: the next scheduled check runs the new file. Until it does, the loop says it is waiting for the next check, and if that check fails or does not come when due, the loop asks you, with Undo.")
	b, _ := json.Marshal(struct {
		Path, Label, From, To, Ctl string
		At                         time.Time
	}{rec.Path, rec.Label, from, to, controlDigest(ctl), rec.At})
	sum := sha256.Sum256(b)
	return buildReviewed{preview: p, digest: hex.EncodeToString(sum[:])}, nil
}

// buildApply makes a reviewed build change. It reviews again from the
// records and refuses unless the facts are the ones confirmed.
func buildApply(ctx context.Context, entry registry.Entry, action, digest string, daemon daemonStatus, now func() time.Time) panelResult {
	ctl, why := panelReadControl(entry.StateDir)
	if why != "" {
		return panelResult{Outcome: "refused", Text: why, Retry: true}
	}
	r, refused := buildReview(ctx, entry, panelLoop{}, ctl, action, daemon, now())
	if refused != nil {
		return panelResult{Outcome: "review_again", Text: strings.TrimSuffix(refused.Text, " Nothing was changed.") + " Nothing was changed. Review it again.", Retry: true}
	}
	if r.digest != digest {
		return panelResult{Outcome: "review_again", Text: "The loop or one of the files changed since the review. Nothing was changed. Review it again.", Retry: true}
	}
	if action == buildReplaceAction {
		cfg, err := config.Load(entry.ConfigPath)
		if err != nil {
			return panelResult{Outcome: "refused", Text: "Its configuration does not load with this panel's build, so the file was not replaced. Nothing was changed."}
		}
		_ = cfg
	}
	lk, err := buildWaitForChecks(ctx, entry.StateDir)
	if err != nil {
		return panelResult{Outcome: "refused", Text: "A check is running on this loop; try again when it finishes. Nothing was changed.", Retry: true}
	}
	defer lk.Release()
	// Under the tick lock, the facts once more: a check that ran while the
	// lock was awaited has written a new record.
	r2, refused := buildReview(ctx, entry, panelLoop{}, ctl, action, daemon, now())
	if refused != nil || r2.digest != digest {
		return panelResult{Outcome: "review_again", Text: "A check ran while this waited, so the loop is not as reviewed. Nothing was changed. Review it again.", Retry: true}
	}
	if action == buildUndoAction {
		return buildUndo(ctx, entry, now())
	}
	return buildReplace(ctx, entry, now())
}

// buildWaitForChecks takes the loop's tick lock, waiting for a running check
// up to buildLockWait.
func buildWaitForChecks(ctx context.Context, stateDir string) (*lock.Lock, error) {
	deadline := time.Now().Add(buildLockWait)
	for {
		lk, err := lock.Acquire(stateDir)
		if err == nil {
			return lk, nil
		}
		if !errors.Is(err, lock.ErrHeld) || time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// buildReplace backs up and swaps, under the tick lock.
func buildReplace(ctx context.Context, entry registry.Entry, now time.Time) panelResult {
	rec, err := state.LoadCheckExecutable(entry.StateDir)
	if err != nil || rec == nil {
		return panelResult{Outcome: "refused", Text: "The record of which file runs its checks could not be read. Nothing was changed."}
	}
	self, err := buildSource()
	if err != nil {
		return panelResult{Outcome: "refused", Text: "This panel's own file could not be found. Nothing was changed."}
	}
	info, err := os.Lstat(rec.Path)
	if err != nil || !info.Mode().IsRegular() {
		return panelResult{Outcome: "refused", Text: "The file that runs its checks could not be looked at. Nothing was changed."}
	}
	from, err1 := buildHash(ctx, rec.Path)
	to, err2 := buildHash(ctx, self)
	if err1 != nil || err2 != nil {
		return panelResult{Outcome: "refused", Text: "A file could not be read. Nothing was changed."}
	}
	backup, why := buildBackUp(entry, rec.Path, now)
	if why != "" {
		return panelResult{Outcome: "refused", Text: why + " Nothing was changed."}
	}
	u := buildUpdate{Version: 1, At: now.UTC(), Backup: filepath.Base(backup), Target: rec.Path, Mode: uint32(info.Mode().Perm()), FromLabel: dockBuildLabel(rec.Label), ToLabel: dockBuildLabel(version), FromSHA: from, ToSHA: to}
	if err := buildSwap(ctx, self, rec.Path, info.Mode().Perm(), to); err != nil {
		return panelResult{Outcome: "refused", Text: "The file could not be replaced (" + err.Error() + "). It is as it was. The backup is kept in " + buildTilde(backup) + ". Nothing else was changed."}
	}
	if err := saveBuildUpdate(entry.StateDir, u); err != nil {
		return panelResult{Outcome: "uncertain", Text: "The file was replaced, but the record of it could not be saved, so this panel cannot offer Undo. The backup is in " + buildTilde(backup) + "."}
	}
	buildHistory(entry.StateDir, "replace_build")
	buildPrune(filepath.Join(entry.StateDir, buildBackupsDir))
	return panelResult{Outcome: "done", Text: "Replaced with " + productName + " " + u.ToLabel + ". Nothing was restarted; the next scheduled check runs it."}
}

// buildUndo restores the file and the state from the backup.
func buildUndo(ctx context.Context, entry registry.Entry, now time.Time) panelResult {
	u, err := loadBuildUpdate(entry.StateDir)
	if err != nil || u == nil {
		return panelResult{Outcome: "refused", Text: "The record of the replacement could not be read. Nothing was changed."}
	}
	dir := filepath.Join(entry.StateDir, buildBackupsDir, u.Backup)
	old := filepath.Join(dir, buildBackupFile)
	if sum, err := buildHash(ctx, old); err != nil || sum != u.FromSHA {
		return panelResult{Outcome: "refused", Text: "The backed-up file is missing or changed, so it is not restored. Nothing was changed."}
	}
	if err := buildSwap(ctx, old, u.Target, os.FileMode(u.Mode), u.FromSHA); err != nil {
		return panelResult{Outcome: "refused", Text: "The file could not be restored (" + err.Error() + "). Nothing was changed."}
	}
	if err := buildRestoreState(filepath.Join(dir, buildBackupState), entry.StateDir); err != nil {
		return panelResult{Outcome: "uncertain", Text: "The file was restored, but the state could not be restored completely (" + err.Error() + "). Look at the loop before anything else; the backup is in " + buildTilde(dir) + "."}
	}
	_ = os.Remove(filepath.Join(entry.StateDir, buildUpdateName))
	buildHistory(entry.StateDir, "undo_replace_build")
	return panelResult{Outcome: "done", Text: "Restored " + productName + " " + u.FromLabel + " and the loop's state as they were. Nothing was restarted."}
}

// buildHistory records a build change in the loop's history: the operation
// alone, with no path or label. The control record is not changed.
func buildHistory(stateDir, operation string) {
	_, _ = updateControlFromAnnotated(stateDir, operation, panelChangeOrigin, func(*control.File) error { return nil }, nil)
}

// buildSwap puts src's bytes in place of target: written beside it, checked,
// flushed, then renamed over it, keeping mode. A failure leaves target as it was.
func buildSwap(ctx context.Context, src, target string, mode os.FileMode, want string) error {
	in, err := os.Open(src)
	if err != nil {
		return errors.New("the source could not be opened")
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".replacing-*")
	if err != nil {
		return errors.New("its folder cannot be written")
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(in, serviceBinaryLimit+1)); err != nil {
		return errors.New("the new file could not be written")
	}
	if hex.EncodeToString(h.Sum(nil)) != want || ctx.Err() != nil {
		return errors.New("the source changed while it was copied")
	}
	if err := tmp.Chmod(mode); err != nil {
		return errors.New("its permissions could not be set")
	}
	if err := tmp.Sync(); err != nil || tmp.Close() != nil {
		return errors.New("the new file could not be saved")
	}
	if err := os.Rename(name, target); err != nil {
		return errors.New("it could not be put in place")
	}
	ok = true
	if d, err := os.Open(filepath.Dir(target)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// buildBackUp copies the file, the configuration and the state directory into
// a new private backup directory. It returns the directory, or why not.
func buildBackUp(entry registry.Entry, target string, now time.Time) (string, string) {
	root := filepath.Join(entry.StateDir, buildBackupsDir)
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
	if err := buildCopyFile(target, filepath.Join(dir, buildBackupFile), 0o700); err != nil {
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

// buildSkipState: lock files, the backups, the update record and the daemon's
// endpoint are not part of what is backed up or restored.
func buildSkipState(name string) bool {
	return strings.HasSuffix(name, ".lock") || name == buildUpdateName || name == buildBackupsDir || name == "daemon.json" || name == "daemon.sock"
}

func buildCopyFile(src, dst string, mode os.FileMode) error {
	b, err := snapshot.ReadFile(src)
	if err != nil {
		return err
	}
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

// buildRestoreState puts the backed-up state back: each file is replaced
// atomically, and files made since the backup are removed. Locks, the
// backups and the daemon's endpoint are left alone.
func buildRestoreState(backup, stateDir string) error {
	keep := map[string]bool{}
	if err := filepath.WalkDir(backup, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(backup, path)
		keep[rel] = true
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(stateDir, rel), 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return control.WritePrivateFile(filepath.Join(stateDir, rel), b)
	}); err != nil {
		return errors.New("a backed-up file could not be put back")
	}
	var extra []string
	_ = filepath.WalkDir(stateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == stateDir {
			return nil
		}
		rel, _ := filepath.Rel(stateDir, path)
		if buildSkipState(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !keep[rel] && d.Type().IsRegular() {
			extra = append(extra, path)
		}
		return nil
	})
	for _, p := range extra {
		if err := os.Remove(p); err != nil {
			return errors.New("a file written since the backup could not be removed")
		}
	}
	return nil
}

// buildPrune keeps the newest backups.
func buildPrune(root string) {
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
		os.RemoveAll(filepath.Join(root, names[0]))
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
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<10 {
		return nil, errors.New("unreadable")
	}
	b, err := snapshot.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var u buildUpdate
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&u) != nil || u.Version != 1 || u.Backup == "" || strings.ContainsAny(u.Backup, `/\`) || !filepath.IsAbs(u.Target) {
		return nil, errors.New("unreadable")
	}
	return &u, nil
}
