// Package prbacklog acquires bounded, read-only GitHub evidence and evaluates a
// configured WIP policy. It never decides whether a repository may be changed.
package prbacklog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxRows = 500
const MaxBytes = 2 << 20
const Timeout = 5 * time.Second

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_.-]{1,100}$`)

// Policy has no defaults: callers apply and validate their own configuration.
type Policy struct {
	Repo            string        `json:"repo"`
	MaxOpen         int           `json:"maxOpen"`
	HeldLabel       string        `json:"-"`
	SupersededLabel string        `json:"-"`
	Interval        time.Duration `json:"-"`
	MaxAge          time.Duration `json:"-"`
}

func (p Policy) Validate() error {
	if !repoPattern.MatchString(p.Repo) || strings.HasSuffix(p.Repo, "/.") || strings.HasSuffix(p.Repo, "/..") {
		return errors.New("repo must be OWNER/NAME on github.com")
	}
	if p.MaxOpen < 1 || p.MaxOpen > 5000 {
		return errors.New("max_open must be 1..5000")
	}
	if p.Interval < 5*time.Minute || p.Interval > 24*time.Hour {
		return errors.New("refresh_interval must be 5m..24h")
	}
	if p.MaxAge < p.Interval || p.MaxAge > 24*time.Hour {
		return errors.New("max_age must be at least refresh_interval and at most 24h")
	}
	for _, s := range []string{p.HeldLabel, p.SupersededLabel} {
		if len(s) > 100 || strings.IndexFunc(s, unicode.IsControl) >= 0 || strings.TrimSpace(s) != s {
			return errors.New("labels must be at most 100 bytes without surrounding whitespace or control text")
		}
	}
	if p.HeldLabel != "" && p.HeldLabel == p.SupersededLabel {
		return errors.New("held and superseded labels must differ")
	}
	return nil
}

func (p Policy) Binding() string {
	b, _ := json.Marshal([]any{strings.ToLower(p.Repo), p.MaxOpen, p.HeldLabel, p.SupersededLabel, int64(p.Interval), int64(p.MaxAge)})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Row withholds titles, bodies, names, labels and branch names. DependsOn is
// only a same-repository base-to-head match among the observed open PRs.
type Row struct {
	Number     int       `json:"number"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Additions  int64     `json:"additions"`
	Deletions  int64     `json:"deletions"`
	Files      int64     `json:"files"`
	Draft      bool      `json:"draft"`
	Held       bool      `json:"held"`
	Superseded bool      `json:"superseded"`
	MergeState string    `json:"mergeState"`
	Review     string    `json:"review"`
	DependsOn  []int     `json:"dependsOn"`
}

type Summary struct {
	Open         int       `json:"open"`
	Draft        int       `json:"draft"`
	Held         int       `json:"held"`
	Superseded   int       `json:"superseded"`
	Blocked      int       `json:"blocked"`
	Unknown      int       `json:"unknown"`
	Dependencies int       `json:"branchDependencies"`
	Additions    int64     `json:"additions"`
	Deletions    int64     `json:"deletions"`
	Files        int64     `json:"files"`
	Oldest       time.Time `json:"oldest,omitempty"`
}

func (s Summary) Valid() bool {
	if s.Open < 0 || s.Open > MaxRows || s.Additions < 0 || s.Deletions < 0 || s.Files < 0 || s.Additions > 1e15 || s.Deletions > 1e15 || s.Files > 1e15 {
		return false
	}
	for _, n := range []int{s.Draft, s.Held, s.Superseded, s.Blocked, s.Unknown, s.Dependencies} {
		if n < 0 || n > s.Open {
			return false
		}
	}
	if s.Open == 0 {
		return s == (Summary{})
	}
	return !s.Oldest.IsZero()
}

type Report struct {
	HeldLabelConfigured       bool      `json:"heldLabelConfigured"`
	SupersededLabelConfigured bool      `json:"supersededLabelConfigured"`
	Version                   int       `json:"version"`
	Repo                      string    `json:"repo"`
	StartedAt                 time.Time `json:"startedAt"`
	FinishedAt                time.Time `json:"finishedAt"`
	Complete                  bool      `json:"complete"`
	Failure                   string    `json:"failure,omitempty"`
	Summary                   *Summary  `json:"summary,omitempty"`
	Rows                      []Row     `json:"rows"`
}

// Query returns stdout only; errors and raw output are never exposed in reports.
type Query func(context.Context, ...string) ([]byte, error)

const query = `query($owner:String!,$name:String!,$cursor:String){repository(owner:$owner,name:$name){nameWithOwner pullRequests(first:100,after:$cursor,states:OPEN,orderBy:{field:CREATED_AT,direction:ASC}){totalCount pageInfo{hasNextPage endCursor} nodes{number createdAt updatedAt additions deletions changedFiles isDraft baseRefName headRefName headRepository{nameWithOwner} mergeStateStatus reviewDecision labels(first:100){totalCount nodes{name}}}}}}`

type rawPR struct {
	Number                             *int
	CreatedAt, UpdatedAt               *time.Time
	Additions, Deletions, ChangedFiles *int64
	IsDraft                            *bool
	BaseRefName, HeadRefName           *string
	HeadRepository                     *struct{ NameWithOwner string }
	MergeStateStatus                   *string
	ReviewDecision                     *string
	Labels                             *struct {
		TotalCount *int
		Nodes      []*struct{ Name *string }
	}
}

type page struct {
	Errors json.RawMessage
	Data   *struct {
		Repository *struct {
			NameWithOwner string
			PullRequests  *struct {
				TotalCount *int
				PageInfo   *struct {
					HasNextPage *bool
					EndCursor   *string
				}
				Nodes []*rawPR
			}
		}
	}
}

// Collect performs at most five sequential pages, with no retries and a shared
// five-second deadline. Page churn, errors and caps never become an all-clear.
func Collect(ctx context.Context, run Query, p Policy, now func() time.Time) Report {
	r := Report{HeldLabelConfigured: p.HeldLabel != "", SupersededLabelConfigured: p.SupersededLabel != "", Version: 1, Repo: p.Repo, StartedAt: now().UTC(), Rows: []Row{}}
	finish := func(code string) Report { r.FinishedAt = now().UTC(); r.Failure = code; return r }
	if p.Validate() != nil {
		return finish("invalid_policy")
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	parts := strings.Split(p.Repo, "/")
	cursor := ""
	total := -1
	seen := map[int]bool{}
	cursors := map[string]bool{}
	raw := []*rawPR{}
	for i := 0; i < 5; i++ {
		args := []string{"api", "graphql", "--hostname", "github.com", "-f", "query=" + query, "-f", "owner=" + parts[0], "-f", "name=" + parts[1]}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		b, err := run(ctx, args...)
		if err != nil || ctx.Err() != nil {
			return finish("query_failed")
		}
		if len(b) > MaxBytes {
			return finish("output_limit")
		}
		var pg page
		if json.Unmarshal(b, &pg) != nil || (len(pg.Errors) > 0 && string(pg.Errors) != "[]" && string(pg.Errors) != "null") || pg.Data == nil || pg.Data.Repository == nil {
			return finish("invalid_response")
		}
		repo := pg.Data.Repository
		c := repo.PullRequests
		if !strings.EqualFold(repo.NameWithOwner, p.Repo) || c == nil || c.TotalCount == nil || *c.TotalCount < 0 || c.PageInfo == nil || c.PageInfo.HasNextPage == nil || c.Nodes == nil || len(c.Nodes) > 100 {
			return finish("invalid_response")
		}
		if total != -1 && total != *c.TotalCount {
			return finish("inventory_changed")
		}
		total = *c.TotalCount
		for _, pr := range c.Nodes {
			if !validPR(pr, r.StartedAt) {
				return finish("invalid_response")
			}
			if seen[*pr.Number] {
				return finish("inventory_changed")
			}
			seen[*pr.Number] = true
			if *pr.Labels.TotalCount != len(pr.Labels.Nodes) {
				return finish("partial_labels")
			}
			raw = append(raw, pr)
		}
		if !*c.PageInfo.HasNextPage {
			if len(raw) != total {
				return finish("inventory_changed")
			}
			r.Rows, r.Summary = project(raw, p)
			r.Complete = true
			return finish("")
		}
		if len(c.Nodes) == 0 || c.PageInfo.EndCursor == nil || *c.PageInfo.EndCursor == "" || len(*c.PageInfo.EndCursor) > 1024 || cursors[*c.PageInfo.EndCursor] {
			return finish("invalid_response")
		}
		cursor = *c.PageInfo.EndCursor
		cursors[cursor] = true
	}
	return finish("inventory_capped")
}

func validPR(p *rawPR, now time.Time) bool {
	if p == nil || p.Number == nil || *p.Number < 1 || p.CreatedAt == nil || p.UpdatedAt == nil || p.CreatedAt.IsZero() || p.UpdatedAt.Before(*p.CreatedAt) || p.UpdatedAt.After(now.Add(time.Minute)) || p.CreatedAt.After(now) || p.Additions == nil || p.Deletions == nil || p.ChangedFiles == nil || p.IsDraft == nil || p.BaseRefName == nil || p.HeadRefName == nil || p.MergeStateStatus == nil || p.Labels == nil || p.Labels.TotalCount == nil || *p.Labels.TotalCount < 0 || p.Labels.Nodes == nil || len(p.Labels.Nodes) > 100 {
		return false
	}
	for _, l := range p.Labels.Nodes {
		if l == nil || l.Name == nil || len(*l.Name) == 0 || len(*l.Name) > 256 || strings.TrimSpace(*l.Name) == "" || !utf8.ValidString(*l.Name) || strings.IndexFunc(*l.Name, unicode.IsControl) >= 0 {
			return false
		}
	}
	for _, n := range []int64{*p.Additions, *p.Deletions, *p.ChangedFiles} {
		if n < 0 || n > 1e12 {
			return false
		}
	}
	for _, s := range []string{*p.BaseRefName, *p.HeadRefName} {
		if s == "" || len(s) > 1024 || strings.ContainsAny(s, "\r\n\x00") {
			return false
		}
	}
	return true
}

func project(raw []*rawPR, p Policy) ([]Row, *Summary) {
	rows := make([]Row, 0, len(raw))
	s := &Summary{Open: len(raw)}
	for _, v := range raw {
		row := Row{Number: *v.Number, CreatedAt: *v.CreatedAt, UpdatedAt: *v.UpdatedAt, Additions: *v.Additions, Deletions: *v.Deletions, Files: *v.ChangedFiles, Draft: *v.IsDraft, MergeState: knownMerge(*v.MergeStateStatus), Review: "unknown", DependsOn: []int{}}
		if v.ReviewDecision != nil {
			switch *v.ReviewDecision {
			case "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED":
				row.Review = *v.ReviewDecision
			}
		}
		for _, l := range v.Labels.Nodes {
			if p.HeldLabel != "" && *l.Name == p.HeldLabel {
				row.Held = true
			}
			if p.SupersededLabel != "" && *l.Name == p.SupersededLabel {
				row.Superseded = true
			}
		}
		for _, other := range raw {
			if *v.Number != *other.Number && other.HeadRepository != nil && strings.EqualFold(other.HeadRepository.NameWithOwner, p.Repo) && *v.BaseRefName == *other.HeadRefName {
				row.DependsOn = append(row.DependsOn, *other.Number)
			}
		}
		sort.Ints(row.DependsOn)
		if row.Draft {
			s.Draft++
		}
		if row.Held {
			s.Held++
		}
		if row.Superseded {
			s.Superseded++
		}
		if row.Draft || row.Review == "CHANGES_REQUESTED" || row.Review == "REVIEW_REQUIRED" || row.MergeState == "BLOCKED" || row.MergeState == "DIRTY" || row.MergeState == "BEHIND" || row.MergeState == "UNSTABLE" {
			s.Blocked++
		}
		if row.MergeState == "unknown" || row.Review == "unknown" {
			s.Unknown++
		}
		if len(row.DependsOn) > 0 {
			s.Dependencies++
		}
		s.Additions += row.Additions
		s.Deletions += row.Deletions
		s.Files += row.Files
		if s.Oldest.IsZero() || row.CreatedAt.Before(s.Oldest) {
			s.Oldest = row.CreatedAt
		}
		rows = append(rows, row)
	}
	return rows, s
}
func knownMerge(s string) string {
	switch s {
	case "CLEAN", "BLOCKED", "BEHIND", "DIRTY", "UNSTABLE", "HAS_HOOKS", "DRAFT":
		return s
	}
	return "unknown"
}

// Notice is fixed wording and aggregate evidence only. A current observation is
// not proof of landing readiness, branch permission or a verified override.
func Notice(p Policy, c *Cache, now time.Time) (string, string) {
	status := "unknown"
	summary := "No complete current inventory is available."
	if c != nil && c.Valid(p, now) && c.Good != nil {
		s := c.Good
		summary = fmt.Sprintf("Last complete observation %s: %d open PRs (%d draft, %d held-label, %d superseded-label, %d structured blockers, %d unknown review/merge state, %d with observed branch dependencies); %d additions, %d deletions, %d files.", c.GoodAt.UTC().Format(time.RFC3339), s.Open, s.Draft, s.Held, s.Superseded, s.Blocked, s.Unknown, s.Dependencies, s.Additions, s.Deletions, s.Files)
		if s.Open > 0 {
			summary += " Oldest PR opened " + s.Oldest.UTC().Format(time.RFC3339) + "."
		}
		if c.Failure == "" && now.Sub(c.GoodAt) <= p.MaxAge {
			status = "below_threshold"
			if s.Open >= p.MaxOpen {
				status = "integration_first"
			}
		} else {
			summary += " This is historical evidence; the current backlog is unknown."
		}
	}
	if p.HeldLabel == "" {
		summary += " Held-label classification is not configured."
	}
	if p.SupersededLabel == "" {
		summary += " Superseded-label classification is not configured."
	}
	advice := "Inspect the backlog before expanding work. Unknown is not an all-clear."
	if status == "integration_first" {
		advice = "Finish review, fixes and integration of authorized existing work before starting more branches. If exceeding this WIP policy, state an explicit reason and the existing authority for that work."
	}
	if status == "below_threshold" {
		advice = "The observed count is below the WIP threshold; this grants no permission to start work."
	}
	text := fmt.Sprintf("\n\n[looptender:pr-backlog status=%s max_open=%d]\n%s %s GitHub evidence is an interval observation, not an atomic snapshot or landing readiness. Looptender has not changed or merged any repository.\n", status, p.MaxOpen, summary, advice)
	// Do not repeat solely because the observation timestamp or PR ages advance.
	var sem *Summary
	if c != nil && c.Valid(p, now) && c.Good != nil {
		v := *c.Good
		sem = &v
	}
	b, _ := json.Marshal([]any{p.Binding(), status, sem})
	h := sha256.Sum256(b)
	return text, hex.EncodeToString(h[:])
}
