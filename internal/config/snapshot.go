package config

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
)

// Snapshots bind exactly the bytes parsed by Load, not a later read. They are
// private in-memory metadata and are never included in YAML or JSON output.
type sourceSnapshot struct {
	path string
	size int64
	sum  [sha256.Size]byte
}

func snapshot(path string, raw []byte) sourceSnapshot {
	return sourceSnapshot{path: path, size: int64(len(raw)), sum: sha256.Sum256(raw)}
}

func (s sourceSnapshot) matches() bool {
	if s.path == "" {
		return true // Parse/in-memory configurations have no filesystem source.
	}
	info, err := os.Stat(s.path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != s.size {
		return false
	}
	f, err := os.Open(s.path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != s.size {
		return false
	}
	h := sha256.New()
	// Bound the read to the original digest comparison's purpose: do not
	// allocate a second copy of a configuration that may contain secrets.
	if n, err := io.Copy(h, io.LimitReader(f, s.size+1)); err != nil || n != s.size {
		return false
	}
	return bytes.Equal(h.Sum(nil), s.sum[:])
}

// CheckSources rejects a stale loaded configuration, including its fallbacks.
// Call under the tick lock before provider work. This coordinates with writers
// using that same lock; it cannot serialize arbitrary external file edits.
// Content, paths, and digests are deliberately absent from returned errors.
func (c *Config) CheckSources() error {
	if !c.source.matches() {
		return errors.New("configuration changed or is unreadable")
	}
	if c.FallbackSet != nil && !c.FallbackSet.source.matches() {
		return errors.New("fallback configuration changed or is unreadable")
	}
	return nil
}

// Path is the absolute path Load read this configuration from; empty for a
// configuration parsed from memory.
func (c *Config) Path() string { return c.source.path }

// SourcesDigest identifies the parsed config and fallback source snapshots,
// including their paths. It is private control metadata, never diagnostic
// output or an authorization token. CheckSources must still succeed at use.
func (c *Config) SourcesDigest() string {
	if c.source.path == "" {
		return ""
	}
	h := sha256.New()
	for _, s := range []sourceSnapshot{c.source, fallbackSource(c)} {
		fmt.Fprintf(h, "%q:%d:%x;", s.path, s.size, s.sum)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func fallbackSource(c *Config) sourceSnapshot {
	if c.FallbackSet == nil {
		return sourceSnapshot{}
	}
	return c.FallbackSet.source
}
