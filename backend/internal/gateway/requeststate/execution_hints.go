package requeststate

import (
	"context"
	"strings"
)

var openAILegacySessionHashKey = openAILegacySessionHashContextKey{}

type agentTaskRecoveryKey struct{}

// cacheBillingKey identifies forced cache billing for the current attempt.
type cacheBillingKey struct{}

// Hint distinguishes an absent value from an explicit zero using Set.
type Hint[T bool | int | int64] struct {
	Value T
	Set   bool
}

// ExecutionHints is a value snapshot; deriving an attempt leaves the parent request unchanged.
type ExecutionHints struct {
	// HealthModel is the normalized model used for this attempt's health observations.
	HealthModel            string
	SessionIsolationSource string
	SessionIsolationHash   string
	// Selection thresholds, group requirements, and per-attempt bypass flags.
	QuotaAutoPauseThreshold5h   float64
	QuotaAutoPauseThreshold7d   float64
	GroupPrivacyRequirement     Hint[bool]
	GroupPrivacyGroupID         int64
	ProxyQuarantineBypass       bool
	ClaudeCode                  bool
	ClaudeCodeVersion           string
	OpenAIImageGenerationIntent bool
	OpenAIImagesEndpoint        bool
	IsMaxTokensOneHaikuRequest  Hint[bool]
	ThinkingEnabled             Hint[bool]
	PrefetchedStickyProviderID  Hint[int64]
	PrefetchedStickyGroupID     Hint[int64]
	SingleProviderRetry         Hint[bool]
	ProviderSwitchCount         Hint[int]
}

type executionHintsKey struct{}

type openAILegacySessionHashContextKey struct{}

// WithAgentTaskRecovery marks the recovery opportunity as used for this attempt sequence.
func WithAgentTaskRecovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentTaskRecoveryKey{}, true)
}

// AgentTaskRecoveryTried reports whether this attempt sequence used its recovery opportunity.
func AgentTaskRecoveryTried(ctx context.Context) bool {
	value, _ := ctx.Value(agentTaskRecoveryKey{}).(bool)
	return value
}

// IsForceCacheBilling returns false when the flag is absent or has the wrong type.
func IsForceCacheBilling(ctx context.Context) bool {
	value, _ := ctx.Value(cacheBillingKey{}).(bool)
	return value
}

// WithForceCacheBilling sets the flag in a derived attempt context.
func WithForceCacheBilling(ctx context.Context) context.Context {
	return context.WithValue(ctx, cacheBillingKey{}, true)
}

// IsClaudeCodeClient returns the client classification recorded at entry.
func IsClaudeCodeClient(ctx context.Context) bool {
	return ExecutionHintsFromContext(ctx).ClaudeCode
}

func SetClaudeCodeClient(ctx context.Context, value bool) context.Context {
	return updateHints(ctx, func(h *ExecutionHints) { h.ClaudeCode = value })
}

func SetClaudeCodeVersion(ctx context.Context, value string) context.Context {
	return updateHints(ctx, func(h *ExecutionHints) { h.ClaudeCodeVersion = value })
}

func GetClaudeCodeVersion(ctx context.Context) string {
	return ExecutionHintsFromContext(ctx).ClaudeCodeVersion
}

func WithOpenAIImageGenerationIntent(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return updateHints(ctx, func(h *ExecutionHints) { h.OpenAIImageGenerationIntent = true })
}

func OpenAIImageGenerationIntentFromContext(ctx context.Context) bool {
	return ExecutionHintsFromContext(ctx).OpenAIImageGenerationIntent
}

func WithOpenAIImagesEndpoint(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return updateHints(ctx, func(h *ExecutionHints) { h.OpenAIImagesEndpoint = true })
}

func OpenAIImagesEndpointFromContext(ctx context.Context) bool {
	return ExecutionHintsFromContext(ctx).OpenAIImagesEndpoint
}

// ExecutionHintsFromContext reads hints for passing to platform execution entry points.
func ExecutionHintsFromContext(ctx context.Context) ExecutionHints {
	if ctx == nil {
		return ExecutionHints{}
	}
	hints, _ := ctx.Value(executionHintsKey{}).(ExecutionHints)
	return hints
}

func WithExecutionHints(ctx context.Context, hints ExecutionHints) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, executionHintsKey{}, hints)
}

func updateHints(ctx context.Context, update func(*ExecutionHints)) context.Context {
	hints := ExecutionHintsFromContext(ctx)
	update(&hints)
	return WithExecutionHints(ctx, hints)
}

func WithIsMaxTokensOneHaikuRequest(ctx context.Context, value bool) context.Context {
	return updateHints(ctx, func(h *ExecutionHints) { h.IsMaxTokensOneHaikuRequest = Hint[bool]{value, true} })
}

func WithThinkingEnabled(ctx context.Context, value bool) context.Context {
	return updateHints(ctx, func(h *ExecutionHints) { h.ThinkingEnabled = Hint[bool]{value, true} })
}

func WithPrefetchedStickySession(ctx context.Context, providerID, groupID int64) context.Context {
	return updateHints(ctx, func(h *ExecutionHints) {
		h.PrefetchedStickyProviderID = Hint[int64]{providerID, true}
		h.PrefetchedStickyGroupID = Hint[int64]{groupID, true}
	})
}

func WithSingleProviderRetry(ctx context.Context, value bool) context.Context {
	return updateHints(ctx, func(h *ExecutionHints) { h.SingleProviderRetry = Hint[bool]{value, true} })
}

func WithProviderSwitchCount(ctx context.Context, value int) context.Context {
	return updateHints(ctx, func(h *ExecutionHints) { h.ProviderSwitchCount = Hint[int]{value, true} })
}

func IsMaxTokensOneHaikuRequestFromContext(ctx context.Context) (bool, bool) {
	h := ExecutionHintsFromContext(ctx).IsMaxTokensOneHaikuRequest
	return h.Value, h.Set
}

func ThinkingEnabledFromContext(ctx context.Context) (bool, bool) {
	h := ExecutionHintsFromContext(ctx).ThinkingEnabled
	return h.Value, h.Set
}

func PrefetchedStickyProviderIDFromContext(ctx context.Context) (int64, bool) {
	h := ExecutionHintsFromContext(ctx).PrefetchedStickyProviderID
	return h.Value, h.Set
}

func PrefetchedStickyGroupIDFromContext(ctx context.Context) (int64, bool) {
	h := ExecutionHintsFromContext(ctx).PrefetchedStickyGroupID
	return h.Value, h.Set
}

func SingleProviderRetryFromContext(ctx context.Context) (bool, bool) {
	h := ExecutionHintsFromContext(ctx).SingleProviderRetry
	return h.Value, h.Set
}

func ProviderSwitchCountFromContext(ctx context.Context) (int, bool) {
	h := ExecutionHintsFromContext(ctx).ProviderSwitchCount
	return h.Value, h.Set
}

// WithSessionIsolation saves the parsed session identity for revalidation after group failover.
func WithSessionIsolation(ctx context.Context, source, hash string) context.Context {
	return updateHints(ctx, func(h *ExecutionHints) { h.SessionIsolationSource, h.SessionIsolationHash = source, hash })
}

// WithOpenAILegacySessionHash stores a trimmed legacy session hash in a derived context.
func WithOpenAILegacySessionHash(ctx context.Context, legacyHash string) context.Context {
	if ctx == nil {
		return nil
	}
	trimmed := strings.TrimSpace(legacyHash)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, openAILegacySessionHashKey, trimmed)
}

// OpenAILegacySessionHashFromContext returns the trimmed legacy session hash.
func OpenAILegacySessionHashFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(openAILegacySessionHashKey).(string)
	return strings.TrimSpace(value)
}
