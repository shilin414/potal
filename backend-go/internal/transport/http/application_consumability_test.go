package http

import (
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
)

func TestConsumptionStatusUsesCatalogConsumableSemantics(t *testing.T) {
	active := &catalog.Provider{Status: "active"}
	inactive := &catalog.Provider{Status: "inactive"}
	enabledChat := &catalog.Application{Enabled: true, Kind: "chat"}
	disabledChat := &catalog.Application{Enabled: false, Kind: "chat"}
	enabledFixed := &catalog.Application{Enabled: true, Kind: "task"}
	disabledFixed := &catalog.Application{Enabled: false, Kind: "task"}
	binding := &catalog.Binding{Enabled: true}

	tests := []struct {
		name     string
		app      *catalog.Application
		binding  *catalog.Binding
		provider *catalog.Provider
		ok       bool
		reason   string
	}{
		{name: "disabled chat", app: disabledChat, binding: binding, provider: active, reason: "disabled"},
		{name: "unbound chat", app: enabledChat, reason: "unbound"},
		{name: "inactive provider", app: enabledChat, binding: binding, provider: inactive, reason: "runtime_unavailable"},
		{name: "missing provider", app: enabledChat, binding: binding, reason: "runtime_unavailable"},
		{name: "healthy chat", app: enabledChat, binding: binding, provider: active, ok: true},
		{name: "disabled fixed", app: disabledFixed, reason: "disabled"},
		{name: "enabled fixed", app: enabledFixed, ok: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := consumptionStatus(tc.app, tc.binding, tc.provider)
			if ok != tc.ok || reason != tc.reason {
				t.Fatalf("consumptionStatus()=(%v,%q), want (%v,%q)", ok, reason, tc.ok, tc.reason)
			}
		})
	}
}

func TestActiveProviderForBindingFallsBackToProviderKey(t *testing.T) {
	provider := &catalog.Provider{ID: 9, Key: "feishu_aily", Status: "active"}
	binding := &catalog.Binding{ProviderKey: "feishu_aily", Enabled: true}
	if got := activeProviderForBinding(binding, map[int64]*catalog.Provider{9: provider}); got != provider {
		t.Fatalf("provider key fallback got %#v, want %#v", got, provider)
	}
}
