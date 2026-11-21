package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// SendAllowanceEdit holds a reviewed, positive allowance change in memory.
// Callers must hold the loop's tick lock when preparing and committing, check
// caller authority and current control/ledger evidence, and bind their review
// to SourcesDigest. This primitive never changes controls or the send ledger.
// It does not serialize arbitrary external configuration writers.
type SendAllowanceEdit struct {
	cfg      *Config
	identity os.FileInfo
	after    []byte
}

// PrepareSendAllowance changes only loop.limits.max_sends. Zero (unlimited)
// is deliberately not accepted by this bounded editor.
func (c *Config) PrepareSendAllowance(limit int64) (*SendAllowanceEdit, error) {
	bad := errors.New("send allowance configuration cannot be safely edited")
	if limit <= 0 || limit == c.Loop.Limits.MaxSends {
		return nil, bad
	}
	doc, err := c.sourceDoc()
	if err != nil {
		return nil, bad
	}
	identity, err := os.Lstat(c.source.path)
	if err != nil || !identity.Mode().IsRegular() {
		return nil, bad
	}
	loop := field(doc.Content[0], "loop")
	if loop == nil || loop.Kind != yaml.MappingNode {
		return nil, bad
	}
	limits := field(loop, "limits")
	if limits == nil {
		limits = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		loop.Content = append(loop.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "limits"}, limits)
	}
	if limits.Kind != yaml.MappingNode {
		return nil, bad
	}
	scalar := field(limits, "max_sends")
	if scalar == nil {
		scalar = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int"}
		limits.Content = append(limits.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "max_sends"}, scalar)
	}
	if scalar.Kind != yaml.ScalarNode || scalar.Tag != "!!int" {
		return nil, bad
	}
	scalar.Value = strconv.FormatInt(limit, 10)
	after, err := yaml.Marshal(doc)
	if err != nil || len(after) > 1<<20 {
		return nil, bad
	}
	return &SendAllowanceEdit{cfg: c, identity: identity, after: after}, nil
}

func (e *SendAllowanceEdit) AfterDigest() string {
	sum := sha256.Sum256(e.after)
	return hex.EncodeToString(sum[:])
}

// Commit revalidates the loaded sources and regular-file identity before
// atomic publication. It never resumes a hold or resets attempts. Reusing an
// applied edit fails because its loaded source no longer matches.
func (e *SendAllowanceEdit) Commit() error {
	bad := errors.New("send allowance configuration changed or could not be written")
	unchanged := func() bool {
		info, err := os.Lstat(e.cfg.source.path)
		return err == nil && info.Mode().IsRegular() && os.SameFile(e.identity, info) && e.cfg.CheckSources() == nil
	}
	if !unchanged() {
		return bad
	}
	f, err := os.CreateTemp(filepath.Dir(e.cfg.source.path), ".send-allowance-*")
	if err != nil {
		return bad
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(e.after); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || !unchanged() {
		return bad
	}
	if os.Rename(f.Name(), e.cfg.source.path) != nil {
		return bad
	}
	return nil
}
