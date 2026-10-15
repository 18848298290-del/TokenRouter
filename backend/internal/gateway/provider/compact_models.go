package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway/compact"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// compact_models.go binds provider and global model rules to compact recovery.
// Legacy compact failures can select a provider-specific replacement or a
// configured global model. The attempt adapter reads the selected provider's
// compact rules first and maps a global fallback through its ordinary upstream
// model policy before recovery rewrites the body.

// CompactModels supplies configured fallback rules to compact recovery for each attempt.
type CompactModels struct {
	// Default supplies the configured global compact model. An empty value
	// disables this fallback when the selected provider has no usable compact mapping.
	Default string
}

// compactAttemptModels binds fallback settings to one selected execution provider.
type compactAttemptModels struct {
	defaults CompactModels // configured global fallback settings
	// target supplies this attempt's provider mappings. A nil target has no
	// compact override and delegates global model resolution to the empty model policy.
	target *ExecutionProvider
}

// ProviderModel reads the selected provider's compact mapping for model.
// A nil target returns an empty value and false, allowing compact recovery
// to consult the global configured model.
func (p compactAttemptModels) ProviderModel(model string) (string, bool) {
	if p.target == nil {
		return "", false
	}
	return p.target.View().ResolveCompactMappedModel(model)
}

// GlobalModel returns the configured global fallback for this compact attempt.
func (p compactAttemptModels) GlobalModel() string { return p.defaults.Default }

// ResolveGlobalModel maps the configured fallback through this attempt's ordinary
// OpenAI model policy. The compact flag is false because compact-specific mapping
// has already been considered separately by ProviderModel.
func (p compactAttemptModels) ResolveGlobalModel(model string) string {
	return ExecutionModelPolicy(p.target).OpenAIUpstream(model, false)
}

// Recovery binds fallback model reads, OpenAI failure classification, and the
// body rewriter for one provider attempt. The returned value delegates retry
// decisions to compact.Recovery and leaves response observations and closure
// to the HTTP adapter.
func (p CompactModels) Recovery(target *ExecutionProvider) compact.Recovery {
	return compact.Recovery{Models: compactAttemptModels{defaults: p, target: target}, ContextWindow: openai.IsOpenAIContextWindowError, RewriteModel: protocolopenai.ReplaceModelInBody}
}
