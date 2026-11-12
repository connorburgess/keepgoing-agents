package factory

import (
	"context"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestCursorFactoryUsesPrivateTargetState(t *testing.T) {
	a, err := New(config.Agent{Adapter: "cursor", Cursor: &config.Cursor{Workdir: t.TempDir()}}, t.TempDir(), "primary")
	if err != nil || a.Name() != "cursor" {
		t.Fatalf("%v %v", a, err)
	}
	obs, err := a.Observe(context.Background())
	if err != nil || obs.State != decide.Idle || obs.Evidence.Source != "runner_state" {
		t.Fatalf("%+v %v", obs, err)
	}
}
