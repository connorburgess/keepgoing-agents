//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO at the list's path, or a link to one, must be refused at once: a
// blocking open would hang every command and check of a following loop.
func TestStandardRoutesFIFOIsRefusedWithoutBlocking(t *testing.T) {
	for _, viaLink := range []bool{false, true} {
		name := map[bool]string{false: "fifo", true: "owned_symlink_to_fifo"}[viaLink]
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fifo := filepath.Join(dir, "routes.fifo")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
			path := fifo
			if viaLink {
				path = filepath.Join(dir, RoutesFileName)
				if err := os.Symlink(fifo, path); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(RoutesFileEnv, path)
			done := make(chan error, 1)
			go func() {
				_, err := LoadRoutes(path)
				done <- err
			}()
			select {
			case err := <-done:
				if RoutesProblem(err) != RoutesNotRegular || !strings.Contains(err.Error(), "regular file") {
					t.Fatalf("%v (%s)", err, RoutesProblem(err))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reading the list blocked on a FIFO")
			}
			loaded := make(chan *Config, 1)
			go func() {
				cfg, err := Load(loopFile(t, bbLoop))
				if err != nil {
					t.Error(err)
				}
				loaded <- cfg
			}()
			select {
			case cfg := <-loaded:
				if cfg == nil || !cfg.RouteList.Unreadable || cfg.RouteList.Problem != RoutesNotRegular {
					t.Fatalf("%+v", cfg)
				}
				if cfg.CheckSources() != nil {
					t.Fatal("an unchanged FIFO made the configuration stale")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("loading a following loop blocked on a FIFO")
			}
		})
	}
}

func TestStandardRoutesRefusalReasons(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(t *testing.T) string
	}{
		{"not_private", RoutesNotPrivate, func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), RoutesFileName)
			if err := os.WriteFile(p, []byte(standardList), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, 0o640); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"invalid", RoutesInvalid, func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), RoutesFileName)
			if err := os.WriteFile(p, []byte("version: 3\nroutes: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"directory", RoutesNotRegular, func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), RoutesFileName)
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
			return p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.setup(t)
			t.Setenv(RoutesFileEnv, p)
			cfg, err := Load(loopFile(t, bbLoop))
			if err != nil || cfg.RouteList.Problem != tc.want || !cfg.RouteList.Unreadable {
				t.Fatalf("%v %+v", err, cfg.RouteList)
			}
			if (tc.want == RoutesInvalid) != (cfg.RouteList.Invalid != "") || strings.Contains(cfg.RouteList.Invalid, p) {
				t.Fatalf("invalid detail: %q", cfg.RouteList.Invalid)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		writeRoutes(t, "")
		cfg, err := Load(loopFile(t, bbLoop))
		if err != nil || cfg.RouteList.Problem != "missing" {
			t.Fatalf("%v %+v", err, cfg.RouteList)
		}
	})
	// A symbolic link you own is followed wherever it points, a dotfiles
	// checkout included, as long as the file is private and yours.
	t.Run("owned_symlink_into_a_checkout", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		real := filepath.Join(repo, "routes.yaml")
		if err := os.WriteFile(real, []byte(standardList), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), RoutesFileName)
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		t.Setenv(RoutesFileEnv, link)
		cfg, err := Load(loopFile(t, bbLoop))
		if err != nil || cfg.RouteList.Unreadable || ids(cfg.FallbackSet) != "large-same" {
			t.Fatalf("%v %+v", err, cfg.RouteList)
		}
	})
}
