package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// BBTransferEdit retains private config bytes only in memory. Callers hold the
// loop's tick lock, persist a recovery fence first, and never print this value.
type BBTransferEdit struct {
	source sourceSnapshot
	after  []byte
}

func (c *Config) SourceDigest() string {
	if c.source.path == "" {
		return ""
	}
	return hex.EncodeToString(c.source.sum[:])
}
func (e *BBTransferEdit) AfterDigest() string {
	h := sha256.Sum256(e.after)
	return hex.EncodeToString(h[:])
}

// PrepareBBTransfer changes only the literal primary BB thread scalar. Aliases,
// merged mappings, multiple documents and non-regular config files are refused.
func (c *Config) PrepareBBTransfer(thread string) (*BBTransferEdit, error) {
	bad := errors.New("configuration cannot be safely rewritten for transfer")
	if c.source.path == "" || c.Loop.Adapter != "bb" || c.Loop.BB == nil || c.FallbackSet != nil || c.source.size > 1<<20 || c.CheckSources() != nil {
		return nil, bad
	}
	info, err := os.Lstat(c.source.path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, bad
	}
	f, err := os.Open(c.source.path)
	if err != nil {
		return nil, bad
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || snapshot(c.source.path, raw) != c.source {
		return nil, bad
	}
	var doc yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(raw))
	if d.Decode(&doc) != nil || d.Decode(new(any)) != io.EOF || len(doc.Content) != 1 {
		return nil, bad
	}
	// Refuse YAML indirection anywhere: a rewritten anchor could affect more
	// than the one target, and retaining an alias could preserve the old target.
	var plain func(*yaml.Node) bool
	plain = func(n *yaml.Node) bool {
		if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Tag == "!!merge" {
			return false
		}
		for _, child := range n.Content {
			if !plain(child) {
				return false
			}
		}
		return true
	}
	if !plain(&doc) {
		return nil, bad
	}
	field := func(n *yaml.Node, key string) *yaml.Node {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return n.Content[i+1]
			}
		}
		return nil
	}
	n := field(field(field(doc.Content[0], "loop"), "bb"), "thread")
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" || n.Value != c.Loop.BB.Thread {
		return nil, bad
	}
	n.Value = thread
	after, err := yaml.Marshal(&doc)
	if err != nil || len(after) > 1<<20 {
		return nil, bad
	}
	return &BBTransferEdit{source: c.source, after: after}, nil
}

// Commit atomically replaces the configuration after rechecking its source.
// This does not promise power-loss durability or fence uncooperative writers.
func (e *BBTransferEdit) Commit() error {
	bad := errors.New("configuration replacement failed; transfer remains pending")
	info, err := os.Lstat(e.source.path)
	if err != nil || !info.Mode().IsRegular() || !e.source.matches() {
		return bad
	}
	f, err := os.CreateTemp(filepath.Dir(e.source.path), ".transfer-*")
	if err != nil {
		return bad
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(e.after); err == nil {
		err = f.Chmod(0600)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || !e.source.matches() {
		return bad
	}
	if os.Rename(name, e.source.path) != nil {
		return bad
	}
	return nil
}
