package http

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/go-sql-driver/mysql"

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

func TestProviderIndexUsesProviderKeyAsBusinessIdentity(t *testing.T) {
	wrongID := int64(10)
	wrong := catalog.Provider{
		ID: 10, Key: "wrong", Status: "active",
		Capabilities: map[string]any{"attachments": false},
	}
	correct := catalog.Provider{
		ID: 20, Key: "feishu_aily", Status: "active",
		Capabilities: map[string]any{"attachments": true},
	}
	index := newProviderIndex([]catalog.Provider{wrong, correct})

	tests := []struct {
		name       string
		binding    *catalog.Binding
		want       *catalog.Provider
		consumable bool
	}{
		{
			name: "wrong provider id cannot override active provider key",
			binding: &catalog.Binding{
				ProviderID: &wrongID, ProviderKey: "feishu_aily", Enabled: true,
			},
			want: &correct, consumable: true,
		},
		{
			name: "missing provider key does not fall back to provider id",
			binding: &catalog.Binding{
				ProviderID: &wrongID, ProviderKey: "missing", Enabled: true,
			},
		},
		{
			name: "nil provider id remains compatible",
			binding: &catalog.Binding{
				ProviderKey: "feishu_aily", Enabled: true,
			},
			want: &correct, consumable: true,
		},
	}

	app := &catalog.Application{Enabled: true, Kind: "chat"}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := index.activeForBinding(tc.binding)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("provider = %#v, want nil", got)
				}
			} else if got == nil || got.ID != tc.want.ID {
				t.Fatalf("provider = %#v, want provider id %d", got, tc.want.ID)
			}
			ok, _ := consumptionStatus(app, tc.binding, got)
			if ok != tc.consumable {
				t.Fatalf("consumable = %v, want %v", ok, tc.consumable)
			}
		})
	}

	selected := index.activeForBinding(&catalog.Binding{
		ProviderID: &wrongID, ProviderKey: "feishu_aily", Enabled: true,
	})
	if caps := (&catalog.Binding{Capabilities: map[string]any{}}).EffectiveCapabilities(selected); !caps["attachments"] {
		t.Fatalf("capabilities came from wrong provider: %#v", caps)
	}
}

func TestProviderIndexInactiveKeyDoesNotUseActiveWrongID(t *testing.T) {
	wrongID := int64(10)
	index := newProviderIndex([]catalog.Provider{{ID: wrongID, Key: "wrong", Status: "active"}})
	binding := &catalog.Binding{ProviderID: &wrongID, ProviderKey: "inactive", Enabled: true}
	provider := index.activeForBinding(binding)
	if provider != nil {
		t.Fatalf("provider = %#v, want nil because inactive providers are absent from the active index", provider)
	}
	if ok, reason := consumptionStatus(&catalog.Application{Enabled: true, Kind: "chat"}, binding, provider); ok || reason != "runtime_unavailable" {
		t.Fatalf("consumptionStatus() = (%v, %q), want (false, runtime_unavailable)", ok, reason)
	}
}

func TestActiveProviderIndexPropagatesDatabaseFailure(t *testing.T) {
	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:1)/closed")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close test db: %v", err)
	}
	server := &Server{CatalogRepo: &catalog.Repo{DB: db}}
	if _, err := server.activeProviderIndex(context.Background()); err == nil {
		t.Fatal("activeProviderIndex swallowed a database error")
	}
}
