package app

import (
	"context"
	"log/slog"
	"testing"

	aily "github.com/creation-agent-studio/backend-go/internal/integrations/aily"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
	"github.com/creation-agent-studio/backend-go/internal/transport/sse"
)

// TestNewServerWiresTheSSEHub pins the one wiring step that fails SILENTLY
// (Batch 4 §7 and AC-1).
//
// A gateway built without a hub still compiles, still serves every durable
// event, and still closes on a terminal event — it simply never carries a LIVE
// event. Users would see answers appear only when the run finishes, with
// nothing in the logs saying why: the new hub gauges would read zero, which is
// indistinguishable from "nobody is streaming".
//
// The test therefore asserts the IDENTITY of the hub, not merely non-nil: a
// second private manager would compile, would even work, and would quietly
// restore one Redis subscription per connection.
func TestNewServerWiresTheSSEHub(t *testing.T) {
	hub := sse.NewHubManager(context.Background(), nil, nil, sse.HubOptions{})
	defer hub.Close()

	srv := NewServer(&config.Config{}, &App{
		Log:          slog.Default(),
		Metrics:      telemetry.NewMetrics("test"),
		Redis:        redisx.NewWithPrefix("test"),
		AilyExecutor: &aily.Executor{},
		SSEHub:       hub,
	})

	if srv.SSE == nil {
		t.Fatal("NewServer built no SSE gateway")
	}
	if srv.SSE.Hub != hub {
		t.Fatal("the SSE gateway does not hold the app's hub: every stream would be " +
			"served as a durable-only replay with no live events")
	}
	if srv.SSE.Runs == nil {
		t.Fatal("the SSE gateway has no run reader")
	}
}
