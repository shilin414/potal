package catalog

import "time"

// Provider is a runtime provider row.
type Provider struct {
	ID                    int64
	Key                   string
	Name                  string
	SupportedRuntimeTypes []string
	Capabilities          map[string]any
	Status                string
	MaxInflight           int
}

// Application is the product-level business app.
type Application struct {
	ID           int64
	Slug         string
	Name         string
	Description  string
	Icon         string
	AvatarKey    string
	Color        string
	Kind         string
	RendererKey  string
	ExecutorKey  string
	CategoryID   *int64
	CategorySlug string
	CategoryName string
	IsPublic     bool
	// Enabled is the 应用中心 switch (§21): disabled applications are hidden
	// from every non-admin surface; admins keep management visibility.
	Enabled        bool
	IsDefaultAgent bool
	UsageCount     int64
	// Skills is the agent-scoped 技能配置, projected out of default_config
	// (see skills.go). Read-only here: writes go through MergeSkills.
	Skills    []Skill
	CreatedBy *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Binding couples an application to a provider runtime.
type Binding struct {
	ID                 int64
	ApplicationID      int64
	ProviderID         *int64
	ProviderKey        string
	RuntimeType        string
	ExternalResourceID string
	IdentityMode       string
	ExecutionMode      string
	SessionPolicy      string
	ArtifactPolicy     string
	Capabilities       map[string]any
	Config             map[string]any
	TimeoutSeconds     int64
	Enabled            bool
}

// MentionCandidate is one application the `@` router may select. Only the
// identity fields are needed: the composer either sends to a chat app or
// opens a non-chat one.
type MentionCandidate struct {
	ID   int64
	Slug string
	Name string
	Kind string
}

// Snapshot is the frozen runtime configuration stored on each Run
// (never contains secrets/tokens).
func (b *Binding) Snapshot() map[string]any {
	return map[string]any{
		"runtime_type":         b.RuntimeType,
		"provider_key":         b.ProviderKey,
		"external_resource_id": b.ExternalResourceID,
		"identity_mode":        b.IdentityMode,
		"execution_mode":       b.ExecutionMode,
		"session_policy":       b.SessionPolicy,
		"artifact_policy":      b.ArtifactPolicy,
		"timeout_seconds":      b.TimeoutSeconds,
		"config":               b.Config,
	}
}

// EffectiveCapabilities: binding overrides provider defaults.
func (b *Binding) EffectiveCapabilities(provider *Provider) map[string]bool {
	out := map[string]bool{}
	if provider != nil {
		for k, v := range provider.Capabilities {
			if bv, ok := v.(bool); ok {
				out[k] = bv
			}
		}
	}
	for k, v := range b.Capabilities {
		if bv, ok := v.(bool); ok {
			out[k] = bv
		}
	}
	return out
}
