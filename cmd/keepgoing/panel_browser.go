package main

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
)

// The launch page is an .html file, and the application a computer opens
// .html files with is not always a browser: for many developers it is an
// editor, which would show the page's source, opening code included, and
// open nothing. So the file association is never used. The launch page is
// handed to the application that handles http links: the default browser.
// When that cannot be determined, nothing is launched.

// panelLookup runs one lookup command and returns its trimmed output. Tests
// replace it with a stand-in for each lookup.
var panelLookup = func(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	if out.Len() > 4096 {
		return "", errors.New("lookup output too long")
	}
	return strings.TrimSpace(out.String()), nil
}

// panelPlatform is runtime.GOOS; tests set it to exercise each lookup.
var panelPlatform = runtime.GOOS

// panelDesktopDirectories lists where desktop entries live, most specific
// first, following the XDG base directory rules.
var panelDesktopDirectories = func() []string {
	home, _ := os.UserHomeDir()
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" && home != "" {
		data = filepath.Join(home, ".local", "share")
	}
	dirs := []string{data}
	system := os.Getenv("XDG_DATA_DIRS")
	if system == "" {
		system = "/usr/local/share:/usr/share"
	}
	dirs = append(dirs, strings.Split(system, ":")...)
	dirs = append(dirs, "/var/lib/flatpak/exports/share", "/var/lib/snapd/desktop")
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "flatpak", "exports", "share"))
	}
	var out []string
	for _, d := range dirs {
		if filepath.IsAbs(d) {
			out = append(out, filepath.Join(d, "applications"))
		}
	}
	return out
}

// The LaunchServices question "which application opens an http link",
// asked through the system's scripting bridge. It prints the application's
// path, or nothing.
const panelMacBrowserScript = `ObjC.import("AppKit"); var u = $.NSWorkspace.sharedWorkspace.URLForApplicationToOpenURL($.NSURL.URLWithString("http://127.0.0.1/")); u.isNil() ? "" : u.path.js`

var panelDesktopID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.desktop$`)

// panelBrowser returns a function that builds the command to show a page in
// the default web browser, or an error saying why no browser is known.
func panelBrowser(ctx context.Context) (func(page string) []string, error) {
	if custom := envvar.PanelOpener.Get(); custom != "" {
		return func(page string) []string { return []string{custom, page} }, nil
	}
	switch panelPlatform {
	case "darwin":
		app, err := panelLookup(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", panelMacBrowserScript)
		if err != nil || !filepath.IsAbs(app) || !strings.HasSuffix(app, ".app") || strings.ContainsAny(app, "\n\r\x00") {
			return nil, errors.New("the application that opens web links could not be determined")
		}
		// -a names the application; the file association is not consulted.
		return func(page string) []string { return []string{"/usr/bin/open", "-a", app, page} }, nil
	case "windows":
		return nil, errors.New(panelUnsupported)
	}
	id, err := panelLookup(ctx, "xdg-settings", "get", "default-web-browser")
	if err != nil || !panelDesktopID.MatchString(id) {
		// The handler for the http scheme, never the one for text/html.
		id, err = panelLookup(ctx, "xdg-mime", "query", "default", "x-scheme-handler/http")
	}
	if err != nil || !panelDesktopID.MatchString(id) {
		return nil, errors.New("the default web browser could not be determined")
	}
	for _, dir := range panelDesktopDirectories() {
		// Flatpak and others nest entries; the ID's dashes can be folders.
		for _, name := range []string{id, strings.ReplaceAll(id, "-", "/")} {
			// Turning dashes into folders must not leave the directory:
			// "a-..-..-x.desktop" would.
			if !filepath.IsLocal(name) || strings.Contains(name, "..") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || len(b) > 256<<10 {
				continue
			}
			if argv := panelDesktopExec(string(b)); len(argv) > 0 {
				return func(page string) []string { return panelFillExec(argv, page) }, nil
			}
		}
	}
	return nil, errors.New("the default web browser's desktop entry could not be read")
}

// panelDesktopExec returns the words of the Exec line of a desktop entry's
// main group, split by the desktop entry rules: words are separated by
// spaces, and a word in double quotes may contain spaces and escaped
// characters. Nothing is given to a shell.
func panelDesktopExec(entry string) []string {
	inMain := false
	for _, line := range strings.Split(entry, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "[") {
			inMain = line == "[Desktop Entry]"
			continue
		}
		value, ok := strings.CutPrefix(line, "Exec=")
		if !inMain || !ok {
			continue
		}
		var words []string
		var word strings.Builder
		quoted, started := false, false
		for i := 0; i < len(value); i++ {
			c := value[i]
			switch {
			case quoted && c == '\\' && i+1 < len(value):
				i++
				word.WriteByte(value[i])
			case c == '"':
				quoted, started = !quoted, true
			case c == ' ' && !quoted:
				if started {
					words = append(words, word.String())
					word.Reset()
					started = false
				}
			default:
				word.WriteByte(c)
				started = true
			}
		}
		if quoted {
			return nil
		}
		if started {
			words = append(words, word.String())
		}
		return words
	}
	return nil
}

// panelFillExec puts the page where the entry's field code asks for a file
// or an address, drops the other field codes, and appends the page when the
// entry has no place for one.
func panelFillExec(argv []string, page string) []string {
	out := []string{}
	placed := false
	for i, word := range argv {
		switch word {
		case "%f", "%F":
			out, placed = append(out, page), true
		case "%u", "%U":
			out, placed = append(out, (&url.URL{Scheme: "file", Path: page}).String()), true
		case "%i", "%c", "%k", "%d", "%D", "%n", "%N", "%v", "%m":
		default:
			if i > 0 && strings.Contains(word, "%") {
				word = strings.ReplaceAll(word, "%%", "%")
			}
			out = append(out, word)
		}
	}
	if !placed {
		out = append(out, page)
	}
	return out
}
