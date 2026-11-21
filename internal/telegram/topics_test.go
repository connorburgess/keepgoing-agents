package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTopicNameForLoopAndLookup(t *testing.T) {
	if got := TopicNameForLoop("  alpha\n\tloop  "); got != "alpha loop" {
		t.Fatalf("got %q, want %q", got, "alpha loop")
	}
	if got := TopicNameForLoop("   "); got != "loop" {
		t.Fatalf("empty fallback: got %q, want %q", got, "loop")
	}
	long := strings.Repeat("a", 200)
	if got := len([]rune(TopicNameForLoop(long))); got > maxTopicNameRunes {
		t.Fatalf("expected <= %d runes, got %d", maxTopicNameRunes, got)
	}

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	rec := PairingRecord{
		SchemaVersion: PairingSchemaVersion,
		Mode:          ModeAlerts,
		BotID:         1,
		BotUsername:   "example_looptender_bot",
		AllowedUserID: 10,
		AllowedChatID: 20,
		TopicsEnabled: true,
		LoopTopics: map[string]int64{
			"beta":  202,
			"alpha": 101,
		},
		PairedAt:  now,
		UpdatedAt: now,
	}
	if name, ok := LookupLoopByThreadID(rec, 101); !ok || name != "alpha" {
		t.Fatalf("LookupLoopByThreadID(101) = (%q, %v), want (alpha, true)", name, ok)
	}
	if _, ok := LookupLoopByThreadID(rec, 999); ok {
		t.Fatal("expected unmapped thread to return false")
	}
	rec.TopicsEnabled = false
	if _, ok := LookupLoopByThreadID(rec, 101); ok {
		t.Fatal("expected disabled topics to return false")
	}
}

func TestEnsureLoopTopicsSkipsWhenDisabled(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{Token: "123456:TEST_TOKEN", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	rec := PairingRecord{
		SchemaVersion: PairingSchemaVersion,
		Mode:          ModeAlerts,
		BotID:         1,
		BotUsername:   "example_looptender_bot",
		AllowedUserID: 10,
		AllowedChatID: 20,
		TopicsEnabled: false,
		PairedAt:      now,
		UpdatedAt:     now,
	}
	dir := t.TempDir()
	got, res, err := EnsureLoopTopics(context.Background(), client, dir, rec, []string{"alpha"}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Skipped {
		t.Fatal("expected Skipped=true when TopicsEnabled is false")
	}
	if calls.Load() != 0 {
		t.Fatalf("expected 0 API calls when topics disabled, got %d", calls.Load())
	}
	if len(got.LoopTopics) != 0 {
		t.Fatalf("expected no loop topics, got %v", got.LoopTopics)
	}
}

func TestEnsureLoopTopicsCreatesPersistsAndHandlesPartialFailure(t *testing.T) {
	var createdNames []string
	var nextID int64 = 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/createForumTopic") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			ChatID int64  `json:"chat_id"`
			Name   string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode createForumTopic: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Name == "broken-loop" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"topics disabled"}`))
			return
		}
		createdNames = append(createdNames, req.Name)
		nextID++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_thread_id": nextID,
				"name":              req.Name,
			},
		})
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{Token: "123456:TEST_TOKEN", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	rec := PairingRecord{
		SchemaVersion: PairingSchemaVersion,
		Mode:          ModeAlerts,
		BotID:         1,
		BotUsername:   "example_looptender_bot",
		AllowedUserID: 10,
		AllowedChatID: 20,
		TopicsEnabled: true,
		LoopTopics:    map[string]int64{"existing-loop": 42},
		PairedAt:      now,
		UpdatedAt:     now,
	}
	if err := SavePairing(dir, rec); err != nil {
		t.Fatal(err)
	}

	got, res, err := EnsureLoopTopics(context.Background(), client, dir, rec, []string{"zeta", "existing-loop", "broken-loop", "alpha", "alpha", ""}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("EnsureLoopTopics returned fatal error: %v", err)
	}
	if res.Skipped {
		t.Fatal("expected Skipped=false")
	}
	if res.Existing["existing-loop"] != 42 {
		t.Fatalf("expected existing-loop=42, got %v", res.Existing)
	}
	if got.LoopTopics["alpha"] != 501 || got.LoopTopics["zeta"] != 502 {
		t.Fatalf("unexpected created topics: %v", got.LoopTopics)
	}
	if len(res.Failed) != 1 || res.Failed["broken-loop"] == nil {
		t.Fatalf("expected broken-loop failure recorded, got %v", res.Failed)
	}
	// Verify persistence on disk.
	persisted, ok, err := LoadPairing(dir)
	if err != nil || !ok {
		t.Fatalf("LoadPairing: ok=%v err=%v", ok, err)
	}
	if persisted.LoopTopics["alpha"] != 501 || persisted.LoopTopics["zeta"] != 502 || persisted.LoopTopics["existing-loop"] != 42 {
		t.Fatalf("persisted LoopTopics mismatch: %v", persisted.LoopTopics)
	}
	if _, hasBroken := persisted.LoopTopics["broken-loop"]; hasBroken {
		t.Fatal("broken-loop should not have been persisted")
	}
}
