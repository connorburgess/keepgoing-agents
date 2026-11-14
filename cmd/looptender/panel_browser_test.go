package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// standInLookups replaces the lookup commands with fixed answers and records
// what was asked, for one platform.
func standInLookups(t *testing.T, platform string, answers map[string]string) *[]string {
	t.Helper()
	t.Setenv(panelOpenerEnv, "")
	previousLookup, previousPlatform, previousDirs := panelLookup, panelPlatform, panelDesktopDirectories
	t.Cleanup(func() {
		panelLookup, panelPlatform, panelDesktopDirectories = previousLookup, previousPlatform, previousDirs
	})
	var asked []string
	panelPlatform = platform
	panelLookup = func(_ context.Context, name string, args ...string) (string, error) {
		asked = append(asked, filepath.Base(name)+" "+strings.Join(args[:min(len(args), 3)], " "))
		answer, ok := answers[filepath.Base(name)]
		if !ok {
			return "", errors.New("fictional lookup failure")
		}
		return answer, nil
	}
	return &asked
}

const fictionalPage = "/private/example panel/open-0123456789abcdef0123456789abcdef.html"

func TestPanelOpensTheLaunchPageWithTheWebLinkHandlerOnMacOS(t *testing.T) {
	asked := standInLookups(t, "darwin", map[string]string{"osascript": "/Applications/Example Browser.app"})
	browser, err := panelBrowser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The application is named explicitly: whatever opens .html files, an
	// editor for instance, is never asked.
	if got := browser(fictionalPage); !reflect.DeepEqual(got, []string{"/usr/bin/open", "-a", "/Applications/Example Browser.app", fictionalPage}) {
		t.Fatalf("command: %q", got)
	}
	if len(*asked) != 1 || !strings.HasPrefix((*asked)[0], "osascript -l JavaScript -e") || !strings.Contains(panelMacBrowserScript, "URLForApplicationToOpenURL") || !strings.Contains(panelMacBrowserScript, `"http://`) {
		t.Fatalf("lookup: %q", *asked)
	}
	for name, answer := range map[string]string{"nothing": "", "not an application": "/usr/bin/vim", "a relative path": "Example.app", "two lines": "/Applications/A.app\n/Applications/B.app"} {
		standInLookups(t, "darwin", map[string]string{"osascript": answer})
		if _, err := panelBrowser(context.Background()); err == nil {
			t.Errorf("%s was accepted as the browser", name)
		}
	}
	standInLookups(t, "darwin", nil)
	if _, err := panelBrowser(context.Background()); err == nil || !strings.Contains(err.Error(), "could not be determined") {
		t.Fatalf("failed lookup: %v", err)
	}
}

func TestPanelOpensTheLaunchPageWithTheDefaultWebBrowserOnLinux(t *testing.T) {
	applications := filepath.Join(t.TempDir(), "applications")
	if err := os.MkdirAll(filepath.Join(applications, "org"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(applications, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entry("example-browser.desktop", "[Desktop Entry]\nName=Example Browser\nExec=/opt/example browser/bin/browse %U\n\n[Desktop Action new-window]\nExec=/opt/example/bin/other --new-window\n")
	entry("quoted.desktop", "[Desktop Action first]\nExec=/wrong %u\n[Desktop Entry]\nExec=\"/opt/example browser/bin/browse\" --profile \"work \\\"main\\\"\" %u --flag=100%% %i %c\n")
	entry("plain.desktop", "[Desktop Entry]\nExec=browse\n")
	entry("file.desktop", "[Desktop Entry]\nExec=browse --open %f\n")
	entry("broken.desktop", "[Desktop Entry]\nExec=\"unterminated %u\n")
	entry("editor.desktop", "[Desktop Entry]\nExec=/usr/bin/fictional-editor %F\n")
	use := func(answers map[string]string) (*[]string, []string, error) {
		asked := standInLookups(t, "linux", answers)
		panelDesktopDirectories = func() []string { return []string{filepath.Join(t.TempDir(), "missing"), applications} }
		browser, err := panelBrowser(context.Background())
		if err != nil {
			return asked, nil, err
		}
		return asked, browser(fictionalPage), nil
	}
	fileURL := "file:///private/example%20panel/open-0123456789abcdef0123456789abcdef.html"
	for name, tc := range map[string]struct {
		answers map[string]string
		want    []string
	}{
		"xdg-settings names the browser": {map[string]string{"xdg-settings": "plain.desktop", "xdg-mime": "editor.desktop"}, []string{"browse", fictionalPage}},
		"a file field code":              {map[string]string{"xdg-settings": "file.desktop"}, []string{"browse", "--open", fictionalPage}},
		"quoting, escapes and other field codes": {map[string]string{"xdg-settings": "quoted.desktop"},
			[]string{"/opt/example browser/bin/browse", "--profile", `work "main"`, fileURL, "--flag=100%"}},
		// xdg-settings can be missing or answer nothing: then the handler
		// for the http scheme is asked, never the one for text/html.
		"falls back to the http scheme handler": {map[string]string{"xdg-mime": "example-browser.desktop"}, []string{"/opt/example", "browser/bin/browse", fileURL}},
		"xdg-settings answered something else":  {map[string]string{"xdg-settings": "not a desktop id", "xdg-mime": "plain.desktop"}, []string{"browse", fictionalPage}},
	} {
		asked, got, err := use(tc.answers)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q %v", name, got, err)
		}
		for _, question := range *asked {
			if strings.Contains(question, "text/html") {
				t.Errorf("%s asked for the .html handler: %s", name, question)
			}
		}
	}
	asked, _, _ := use(map[string]string{"xdg-mime": "plain.desktop"})
	if got := strings.Join(*asked, " | "); got != "xdg-settings get default-web-browser | xdg-mime query default x-scheme-handler/http" {
		t.Fatalf("lookups: %s", got)
	}
	// An ID whose dashes, read as folders, would leave the applications
	// directory is never followed there.
	if err := os.WriteFile(filepath.Join(filepath.Dir(applications), "x.desktop"), []byte("[Desktop Entry]\nExec=/private/example/outside %u\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(applications, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, answers := range map[string]map[string]string{
		"an ID that maps outside":    {"xdg-settings": "a-..-..-x.desktop"},
		"no lookup works":            nil,
		"no desktop entry":           {"xdg-settings": "absent.desktop"},
		"an entry that cannot parse": {"xdg-settings": "broken.desktop"},
		"a path, not an ID":          {"xdg-settings": "../../etc/passwd.desktop"},
	} {
		if _, got, err := use(answers); err == nil {
			t.Errorf("%s gave a command: %q", name, got)
		}
	}
}

func TestPanelOpenerOverrideGetsOnlyThePage(t *testing.T) {
	standInLookups(t, "darwin", nil)
	t.Setenv(panelOpenerEnv, "/private/example/launcher")
	browser, err := panelBrowser(context.Background())
	if err != nil || !reflect.DeepEqual(browser(fictionalPage), []string{"/private/example/launcher", fictionalPage}) {
		t.Fatal(err)
	}
}

// With no browser to be found, nothing is launched and no launch page is
// written: the command prints what --no-open prints and says why.
func TestPanelWithoutABrowserPrintsTheAddressAndLaunchesNothing(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, time.Hour)
	for _, platform := range []string{"darwin", "linux"} {
		asked := standInLookups(t, platform, nil)
		var out, errs bytes.Buffer
		code := panelCommand(env{context.Background(), &out, &errs}, []string{"--panel-directory", dir, "--directory", rec.Registry, "--offline"})
		if code != exitOK || len(*asked) == 0 {
			t.Fatalf("%s: %d %s %s", platform, code, out.String(), errs.String())
		}
		text := out.String()
		if !strings.HasPrefix(text, "No browser was started: ") || !strings.Contains(text, "could not be determined") || !strings.Contains(text, "Open this address in a browser on this computer within 30 seconds") ||
			!strings.Contains(text, "Anyone who can read this terminal can use it") {
			t.Fatalf("%s: %s", platform, text)
		}
		address := text[strings.Index(text, "http://127.0.0.1:"):]
		address = address[:strings.IndexAny(address, " \n")]
		if _, c := panelRedeem(t, address); c == nil {
			t.Fatalf("%s: the printed address did not open the panel", platform)
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if panelLaunchPattern.MatchString(e.Name()) {
				t.Fatalf("%s: a launch page was written: %s", platform, e.Name())
			}
		}
	}
}
