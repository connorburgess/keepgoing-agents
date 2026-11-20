package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os/exec"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

// agy /usage is a model-free slash command (num_turns 0). Plan mode keeps the
// exchange read-only. Freshness stays provider_unknown: the reply may be cached.
var accountAntigravityQuotaArgs = []string{
	"--print", "/usage", "--mode", "plan", "--output-format", "json",
}

func accountAntigravityQuotaRead(ctx context.Context, cli string, env []string, workdir string) (raw []byte, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			raw, resultErr = nil, ctx.Err()
		}
	}()
	cmd := exec.CommandContext(ctx, cli, accountAntigravityQuotaArgs...)
	cmd.Env, cmd.Dir, cmd.Stderr = env, workdir, io.Discard
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func accountAntigravityQuotaParse(raw []byte, err error) (*accounts.QuotaDetails, string) {
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			if errors.Is(err, context.Canceled) {
				return nil, "cancelled"
			}
			return nil, "timeout"
		case errors.As(err, new(*exec.ExitError)):
			return nil, "quota_unavailable"
		default:
			return nil, "status_unavailable"
		}
	}
	if !accounts.QuotaJSONUnique(raw) {
		return nil, "schema_unavailable"
	}
	var response struct {
		Command *struct {
			Name string          `json:"name"`
			Data json.RawMessage `json:"data"`
		} `json:"command"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Command == nil || response.Command.Name != "usage" || len(response.Command.Data) == 0 {
		return nil, "schema_unavailable"
	}
	var data struct {
		Groups []struct {
			Buckets []struct {
				ID                string  `json:"id"`
				Window            string  `json:"window"`
				RemainingFraction float64 `json:"remaining_fraction"`
				ResetTime         string  `json:"reset_time"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if json.Unmarshal(response.Command.Data, &data) != nil || len(data.Groups) == 0 {
		return nil, "schema_unavailable"
	}
	details := &accounts.QuotaDetails{Buckets: []accounts.QuotaBucket{}}
	seen := map[string]bool{}
	for _, g := range data.Groups {
		for _, b := range g.Buckets {
			minutes := int64(0)
			switch b.ID {
			case "gemini-weekly", "3p-weekly":
				if b.Window != "weekly" {
					return nil, "schema_unavailable"
				}
				minutes = 10080
			case "gemini-5h", "3p-5h":
				if b.Window != "5h" {
					return nil, "schema_unavailable"
				}
				minutes = 300
			default:
				details.Partial = true
				details.Omitted++
				continue
			}
			if seen[b.ID] || math.IsNaN(b.RemainingFraction) || math.IsInf(b.RemainingFraction, 0) || b.RemainingFraction < 0 || b.RemainingFraction > 1 {
				return nil, "schema_unavailable"
			}
			reset, err := time.Parse(time.RFC3339, b.ResetTime)
			if err != nil {
				return nil, "schema_unavailable"
			}
			reset = reset.UTC()
			if reset.Year() < 2000 || reset.Year() > 9999 {
				return nil, "schema_unavailable"
			}
			used := math.Round((1-b.RemainingFraction)*1000) / 10 // one decimal; clamp below
			if used < 0 {
				used = 0
			}
			if used > 100 {
				used = 100
			}
			seen[b.ID] = true
			details.Buckets = append(details.Buckets, accounts.QuotaBucket{
				Key: b.ID,
				Windows: []accounts.QuotaMeter{{
					Name:            "primary",
					UsedPercent:     used,
					DurationMinutes: &minutes,
					ResetsAt:        &reset,
				}},
			})
		}
	}
	if len(details.Buckets) == 0 {
		return nil, "no_windows"
	}
	if len(details.Buckets) > 4 {
		return nil, "schema_unavailable"
	}
	if !accounts.ValidQuotaDetails(details) {
		return nil, "schema_unavailable"
	}
	return details, ""
}
