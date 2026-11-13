package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

type probePlan struct {
	SchemaVersion       int                  `json:"schemaVersion"`
	Type                string               `json:"type"`
	Tier                probe.Tier           `json:"tier"`
	InvocationCount     int                  `json:"invocationCount"`
	ScheduledDailyCap   int                  `json:"scheduledDailyCapPerCandidate"`
	ScheduledDisabled   bool                 `json:"scheduledDisabled"`
	ScheduledCapApplies bool                 `json:"scheduledCapApplies"`
	EstimatedCostUSD    *float64             `json:"estimatedCostUSD"`
	CostStatus          string               `json:"costStatus"`
	CostNote            string               `json:"costNote"`
	Candidates          []probePlanCandidate `json:"candidates"`
}

type probePlanCandidate struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Group    string `json:"group"`
	Model    string `json:"model"`
	Disabled bool   `json:"disabled"`
	Timeout  string `json:"timeout"`
	CostNote string `json:"costNote"`
}

// planProbes uses the same selected candidates as execution. It does not read
// runtime state, resolve CLIs, or expose commands, environment values or tasks.
func planProbes(fb *config.Fallbacks, only string, exact bool) (probePlan, []config.Candidate, error) {
	p := probePlan{SchemaVersion: 1, Type: "probe_plan", Tier: probe.Manual, ScheduledDailyCap: fb.Probe.DailyCap, ScheduledDisabled: fb.Probe.Disabled, CostStatus: "unknown", CostNote: "Dollar cost and provider request count are unknown. Manual checks are outside the scheduled daily cap and have no keepgoing spending limit.", Candidates: []probePlanCandidate{}}
	if exact {
		p.Tier = probe.Exact
	}
	var selected []config.Candidate
	for _, c := range fb.Candidates {
		if (only != "" && c.ID != only) || (only == "" && c.Disabled) {
			continue
		}
		cost := "One CLI probe turn; may consume subscription quota or API credit. Billing, context overhead, and provider retries are not estimated."
		if c.Probe.Kind == "command" {
			cost = "Custom command; monetary cost, model requests, and side effects are unknown."
		}
		if c.Probe.Kind == config.CustomAdapter {
			cost = "The probe command from the agent's definition, given the fixed probe prompt. keepgoing cannot tell what it does: it may be a full paid turn of the agent, with tools and side effects. Cost and model requests are unknown."
		}
		p.Candidates = append(p.Candidates, probePlanCandidate{ID: c.ID, Kind: c.Probe.Kind, Provider: c.Provider, Group: c.Group, Model: c.ProbeModel(exact), Disabled: c.Disabled, Timeout: fb.Probe.Timeout.Duration.String(), CostNote: cost})
		selected = append(selected, c)
	}
	if only != "" && len(selected) == 0 {
		return probePlan{}, nil, fmt.Errorf("no fallback candidate %q", only)
	}
	p.InvocationCount = len(selected)
	if p.InvocationCount == 0 {
		zero := 0.0
		p.EstimatedCostUSD, p.CostStatus, p.CostNote = &zero, "no_probes", "No candidates selected; no probes will run."
	}
	return p, selected, nil
}

func printProbePlan(w io.Writer, p probePlan, asJSON bool) error {
	if asJSON {
		b, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Probe plan: %d invocation(s), tier %s\n", p.InvocationCount, p.Tier)
	fmt.Fprintf(&b, "Scheduled cap: %d per candidate per UTC day; scheduled probes disabled: %t\n", p.ScheduledDailyCap, p.ScheduledDisabled)
	cost := "unknown"
	if p.CostStatus == "no_probes" {
		cost = "none (no probes selected)"
	}
	fmt.Fprintf(&b, "Expected cost: %s. %s\n", cost, p.CostNote)
	for _, c := range p.Candidates {
		model := c.Model
		if model == "" {
			model = "provider default (unresolved)"
		}
		fmt.Fprintf(&b, "  %s: %s; provider %s; quota group %s; model %s; timeout %s; disabled %t\n    %s\n", c.ID, c.Kind, c.Provider, c.Group, model, c.Timeout, c.Disabled, c.CostNote)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
