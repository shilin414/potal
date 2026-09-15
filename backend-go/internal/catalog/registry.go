// Package catalog owns the Application / Runtime / Provider / Renderer
// model and the provider-neutral RuntimeAdapter boundary.
//
// Invariants (§19/§20):
//
//	Application ≠ Runtime ≠ Provider ≠ Renderer.
//	Business code never switches on provider keys — everything goes
//	through the RuntimeRegistry.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Runtime type / mode / policy enumerations (mirror the API contract).
const (
	RuntimeTypeAgent     = "agent"
	RuntimeTypeWorkflow  = "workflow"
	RuntimeTypeHTTP      = "http"
	RuntimeTypeLocalTask = "local_task"
	RuntimeTypeMedia     = "media"
	RuntimeTypeNone      = "none"

	IdentityModeUser   = "user"
	IdentityModeTenant = "tenant"

	ExecutionModeInteractive = "interactive"
	ExecutionModeBackground  = "background"

	SessionPolicyLazy  = "lazy"
	SessionPolicyEager = "eager"

	ArtifactPolicyExternalRefresh  = "external_refresh"
	ArtifactPolicyMirrorOnAccess   = "mirror_on_access"
	ArtifactPolicyMirrorOnComplete = "mirror_on_complete"
)

// ProviderAuthContext carries resolved provider credentials at call time.
// Tokens are held in memory only — never persisted, never logged.
type ProviderAuthContext struct {
	Provider      string
	IdentityMode  string
	SubjectUserID string
	TenantID      string
	CredentialRef string
	Token         string
}

// IdempotencyClass describes what a provider can do about a submission whose
// outcome is UNKNOWN to the caller (第九轮 P0-2 §6): the HTTP request may have
// been delivered, and the caller has no way to tell.
//
//	IdempotencyNative  the provider accepts a stable idempotency key and will
//	                   collapse a resend, so resending is safe
//	IdempotencyNone    nothing: a resend is an independent external action
//
// NOTE — deliberately TWO classes, not the three the review sketched. The
// middle class ("can be looked up by a stable request key") has no
// implementation in this codebase: the only provider client resolves a chat
// by `agent_chat_id`, which is precisely the value missing in the unknown
// case. Declaring a class that no client can execute would add an untestable
// branch to the most safety-critical decision in the executor. A provider
// that can only be looked up therefore behaves like the weakest class until a
// request-key lookup actually exists: park the run, never resend.
type IdempotencyClass string

const (
	IdempotencyNone   IdempotencyClass = "none"
	IdempotencyNative IdempotencyClass = "native"
)

// IdempotencyAware is an OPTIONAL adapter capability, probed by type
// assertion (same pattern as Service.RunDurationTimestamps): adding it to the
// RuntimeAdapter interface would force every adapter — including test
// doubles — to answer a question most providers do not need to answer.
type IdempotencyAware interface {
	SubmitIdempotency() IdempotencyClass
}

// SubmitIdempotencyOf reports the adapter's submit idempotency class.
// FAIL-CLOSED: an adapter that does not implement the interface is treated as
// the weakest class, so a NEW provider silently inherits the safe policy
// (park on unknown) instead of the dangerous one (resend blindly).
func SubmitIdempotencyOf(a RuntimeAdapter) IdempotencyClass {
	if a == nil {
		return IdempotencyNone
	}
	if aware, ok := a.(IdempotencyAware); ok {
		return aware.SubmitIdempotency()
	}
	return IdempotencyNone
}

// SubmitInput is the neutral submission contract.
type SubmitInput struct {
	RunID                 string
	Auth                  *ProviderAuthContext
	ExternalResourceID    string
	Payload               map[string]any
	SessionID             string
	ExternalAttachmentIDs []string
	Stream                bool
	TimeoutSeconds        int

	// ProviderIdempotencyKey is the STABLE key for this external action
	// (第九轮 P0-2 §5). It is the same value across every transport retry of
	// one submission and is deliberately NOT derived from the attempt
	// counter, so a provider that supports idempotency keys can collapse
	// them. Adapters whose provider has no such notion must ignore it —
	// the guarantee is provided by provider_submissions instead.
	ProviderIdempotencyKey string
}

// SubmitResult carries the provider-side identifiers.
type SubmitResult struct {
	ExternalRunID string
	SessionID     string
	Raw           map[string]any
}

// StatusResult is a neutral status poll result.
type StatusResult struct {
	ExternalRunID  string
	ProviderStatus string
	FinishReason   string
	Output         map[string]any
	Raw            map[string]any
}

// StreamEvent is one neutral streamed event.
type StreamEvent struct {
	EventType string
	Payload   map[string]any
}

// ArtifactRef is the neutral artifact handle.
type ArtifactRef struct {
	ExternalArtifactID string
	Name               string
	URL                string
}

// Capabilities describes what a runtime supports; the frontend drives
// feature visibility from it.
type Capabilities map[string]bool

// Attachment upload input — the worker streams bytes from studio storage.
type AttachmentInput struct {
	Data           []byte
	Filename       string
	ContentType    string
	AttachmentType string // image | file | feishu_doc | bitable
	DocURL         string
}

// RuntimeAdapter is the provider-neutral execution boundary (§20).
type RuntimeAdapter interface {
	Key() string // provider_key:runtime_type

	Submit(ctx context.Context, input *SubmitInput) (*SubmitResult, error)
	Status(ctx context.Context, auth *ProviderAuthContext, externalResourceID, externalRunID string) (*StatusResult, error)

	// Stream submits with stream=true and yields raw provider events;
	// the first event must surface run/session identity when available.
	Stream(ctx context.Context, input *SubmitInput) (<-chan StreamEvent, func(), error)

	UploadAttachment(ctx context.Context, auth *ProviderAuthContext, externalResourceID string, in *AttachmentInput) (string, error)
	ResolveArtifact(ctx context.Context, auth *ProviderAuthContext, externalResourceID, externalArtifactID string) (*ArtifactRef, error)
	CheckVisibility(ctx context.Context, auth *ProviderAuthContext, externalResourceID string) (bool, error)

	Capabilities() Capabilities

	// BuildAuth resolves studio identity → provider credential.
	BuildAuth(ctx context.Context, userID int64, identityMode string) (*ProviderAuthContext, error)

	// Catalog descriptors for the agent-marketplace form.
	DisplayLabel() string
	ResourceIDLabel() string
	ResourceIDPattern() string
	ResourceIDHint() string
	ResourceIDRequired() bool
}

// ErrAdapterNotFound is returned by the registry for unknown keys.
var ErrAdapterNotFound = errors.New("catalog: runtime adapter not registered")

// RuntimeRegistry maps provider_key:runtime_type → adapter.
type RuntimeRegistry struct {
	mu       sync.RWMutex
	adapters map[string]RuntimeAdapter
}

func NewRuntimeRegistry() *RuntimeRegistry {
	return &RuntimeRegistry{adapters: map[string]RuntimeAdapter{}}
}

func (r *RuntimeRegistry) Register(a RuntimeAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.Key()] = a
}

func (r *RuntimeRegistry) Resolve(providerKey, runtimeType string) (RuntimeAdapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[providerKey+":"+runtimeType]
	if !ok {
		return nil, fmt.Errorf("%w: %s:%s", ErrAdapterNotFound, providerKey, runtimeType)
	}
	return a, nil
}

// Descriptors lists catalog entries for GET /api/v2/runtimes.
type Descriptor struct {
	ProviderKey        string       `json:"provider_key"`
	ProviderName       string       `json:"provider_name"`
	RuntimeType        string       `json:"runtime_type"`
	Key                string       `json:"key"`
	Label              string       `json:"label"`
	ResourceIDLabel    string       `json:"resource_id_label"`
	ResourceIDHint     string       `json:"resource_id_hint"`
	ResourceIDPattern  string       `json:"resource_id_pattern"`
	ResourceIDRequired bool         `json:"resource_id_required"`
	IdentityModes      []string     `json:"identity_modes"`
	ExecutionModes     []string     `json:"execution_modes"`
	Capabilities       Capabilities `json:"capabilities"`
}

// Describe returns sorted descriptors for providers (active) × registered
// runtime types.
func (r *RuntimeRegistry) Describe(providers []Provider) []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(providers))
	for _, p := range providers {
		types := append([]string(nil), p.SupportedRuntimeTypes...)
		sort.Strings(types)
		for _, rt := range types {
			a, ok := r.adapters[p.Key+":"+rt]
			if !ok {
				continue
			}
			out = append(out, Descriptor{
				ProviderKey:        p.Key,
				ProviderName:       p.Name,
				RuntimeType:        rt,
				Key:                p.Key + ":" + rt,
				Label:              a.DisplayLabel(),
				ResourceIDLabel:    a.ResourceIDLabel(),
				ResourceIDHint:     a.ResourceIDHint(),
				ResourceIDPattern:  a.ResourceIDPattern(),
				ResourceIDRequired: a.ResourceIDRequired(),
				IdentityModes:      []string{IdentityModeUser, IdentityModeTenant},
				ExecutionModes:     []string{ExecutionModeInteractive, ExecutionModeBackground},
				Capabilities:       a.Capabilities(),
			})
		}
	}
	return out
}

var idPatternCache = sync.Map{}

// ValidateResourceID applies the adapter's declared pattern.
func ValidateResourceID(a RuntimeAdapter, resourceID string) error {
	if !a.ResourceIDRequired() {
		return nil
	}
	if resourceID == "" {
		return fmt.Errorf("%s 不能为空", a.ResourceIDLabel())
	}
	pattern := a.ResourceIDPattern()
	if pattern == "" {
		return nil
	}
	var re *regexp.Regexp
	if cached, ok := idPatternCache.Load(pattern); ok {
		re = cached.(*regexp.Regexp)
	} else {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil // invalid adapter pattern: skip validation
		}
		idPatternCache.Store(pattern, compiled)
		re = compiled
	}
	if !re.MatchString(resourceID) {
		hint := a.ResourceIDHint()
		if hint == "" {
			hint = a.ResourceIDLabel()
		}
		return fmt.Errorf("%s 格式不正确", hint)
	}
	return nil
}
