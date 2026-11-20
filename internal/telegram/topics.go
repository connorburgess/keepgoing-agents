package telegram

import (
	"context"
	"sort"
	"strings"
	"time"
)

const maxTopicNameRunes = 128

// TopicNameForLoop returns a sanitized, bounded private-chat topic title for
// a registered loop. Pure function.
func TopicNameForLoop(loopName string) string {
	clean := strings.Join(strings.Fields(sanitizeInline(loopName)), " ")
	if clean == "" {
		return "loop"
	}
	return TruncateRunes(clean, maxTopicNameRunes)
}

// LookupLoopByThreadID returns the mapped loop name for a private-chat topic
// thread ID, or ("", false) when threadID is unmapped or topics are disabled.
// If multiple names ever share a thread ID, the lexicographically first name
// is returned deterministically. Pure function.
func LookupLoopByThreadID(rec PairingRecord, threadID int64) (string, bool) {
	if !rec.TopicsEnabled || threadID <= 0 || len(rec.LoopTopics) == 0 {
		return "", false
	}
	var matched string
	for name, tid := range rec.LoopTopics {
		if tid == threadID && (matched == "" || name < matched) {
			matched = name
		}
	}
	return matched, matched != ""
}

// TopicSyncResult reports the outcome of syncing registered loops to
// private-chat forum topics.
type TopicSyncResult struct {
	Created  map[string]int64
	Existing map[string]int64
	Failed   map[string]error
	Skipped  bool
}

// EnsureLoopTopics creates private-chat forum topics for any registered loop
// names not already mapped in rec.LoopTopics when rec.TopicsEnabled is true.
// When rec.TopicsEnabled is false, it makes no API calls and returns Skipped=true
// so callers cleanly fall back to a single private DM.
//
// Each newly created topic is persisted atomically to dir via SavePairing
// outside the network call so a partial failure never loses earlier mappings.
func EnsureLoopTopics(ctx context.Context, client *Client, dir string, rec PairingRecord, loopNames []string, now time.Time) (PairingRecord, TopicSyncResult, error) {
	res := TopicSyncResult{
		Created:  map[string]int64{},
		Existing: map[string]int64{},
		Failed:   map[string]error{},
	}
	if !rec.TopicsEnabled {
		res.Skipped = true
		return rec, res, nil
	}
	if rec.Validate() != nil || client == nil {
		return rec, res, ErrInvalidConfig
	}

	seen := map[string]bool{}
	var sorted []string
	for _, raw := range loopNames {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)

	updated := rec
	if updated.LoopTopics == nil {
		updated.LoopTopics = map[string]int64{}
	} else {
		copied := make(map[string]int64, len(updated.LoopTopics))
		for k, v := range updated.LoopTopics {
			copied[k] = v
		}
		updated.LoopTopics = copied
	}

	if now.IsZero() {
		now = time.Now().UTC()
	}

	for _, name := range sorted {
		if tid, ok := updated.LoopTopics[name]; ok && tid > 0 {
			res.Existing[name] = tid
			continue
		}
		if ctx.Err() != nil {
			res.Failed[name] = ctx.Err()
			break
		}
		topic, err := client.CreateForumTopic(ctx, updated.AllowedChatID, TopicNameForLoop(name))
		if err != nil {
			res.Failed[name] = err
			continue
		}
		updated.LoopTopics[name] = topic.MessageThreadID
		updated.UpdatedAt = now.UTC()
		if dir != "" {
			if err := SavePairing(dir, updated); err != nil {
				return rec, res, err
			}
		}
		res.Created[name] = topic.MessageThreadID
	}
	return updated, res, nil
}
