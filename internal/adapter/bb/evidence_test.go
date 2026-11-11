package bb

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestErrorEvidenceRestartAndRecovery(t *testing.T) {
	dir := t.TempDir()
	log := `[` + turnStarted + `,` + providerError("stream-disconnected", 503, "responseStreamDisconnected", true) + `]`
	for i := 0; i < 2; i++ {
		a, _ := newFake(`{"thread":{"status":"active"}}`, log, false)
		a.WithStateDir(dir)
		o, err := a.Observe(context.Background())
		d := o.Evidence
		if err != nil || d.Source != "bb_provider_error" || d.Code != "responsestreamdisconnected" || d.Category != "stream-disconnected" || d.HTTPStatus == nil || *d.HTTPStatus != 503 {
			t.Fatal(d, err)
		}
	}
	a, _ := newFake(`{"thread":{"status":"active"}}`, strings.TrimSuffix(log, "]")+`,{"type":"item/agentMessage/delta","data":{"providerThreadId":"p1","itemId":"item_example"}}]`, false)
	a.WithStateDir(dir)
	o, err := a.Observe(context.Background())
	if err != nil || o.Evidence.Source != "bb_status" || o.Evidence.Code != "unavailable" || o.Evidence.HTTPStatus != nil {
		t.Fatal(o, err)
	}
}

func TestErrorEvidenceUnknownTokensAndLegacySummary(t *testing.T) {
	dir := t.TempDir()
	a, _ := newFake(`{"thread":{"status":"error"}}`, `[`+turnStarted+`,`+providerError("SECRET_CATEGORY", nil, "SECRET_CODE", false)+`]`, false)
	a.WithStateDir(dir)
	o, err := a.Observe(context.Background())
	if err != nil || o.ErrorClass != decide.ErrUnknown || o.Evidence.Code != "unavailable" {
		t.Fatal(o, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "bb-cursor.json"))
	if strings.Contains(string(b), "SECRET_") {
		t.Fatal("cursor retained raw codes")
	}
	legacy := Cursor{Thread: "thr_example123", RetrySchema: 1, LastSeq: 2, Error: &errorSignal{Class: decide.ErrNetwork, WillRetry: true, RetryKnown: true}}
	b, _ = json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "bb-cursor.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	a, _ = newFake(`{"thread":{"status":"active"}}`, `[]`, false)
	a.WithStateDir(dir)
	o, err = a.Observe(context.Background())
	if err != nil || o.Evidence.Source != "bb_error_summary" || o.Evidence.Code != "unavailable" || !o.Retrying {
		t.Fatal(o, err)
	}
}
