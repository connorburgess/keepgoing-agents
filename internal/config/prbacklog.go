package config

import (
	"github.com/looptender/looptender/internal/prbacklog"
	"time"
)

// PRBacklog opts ordinary wakes into read-only GitHub backlog evidence.
type PRBacklog struct {
	Repo            string   `yaml:"repo"`
	MaxOpen         int      `yaml:"max_open"`
	RefreshInterval Duration `yaml:"refresh_interval,omitempty"`
	MaxAge          Duration `yaml:"max_age,omitempty"`
	HeldLabel       string   `yaml:"held_label,omitempty"`
	SupersededLabel string   `yaml:"superseded_label,omitempty"`
}

func (p PRBacklog) Policy() prbacklog.Policy {
	return prbacklog.Policy{Repo: p.Repo, MaxOpen: p.MaxOpen, HeldLabel: p.HeldLabel, SupersededLabel: p.SupersededLabel, Interval: p.RefreshInterval.Duration, MaxAge: p.MaxAge.Duration}
}
func (p *PRBacklog) defaults() {
	setDur(&p.RefreshInterval, 15*time.Minute)
	setDur(&p.MaxAge, time.Hour)
}
