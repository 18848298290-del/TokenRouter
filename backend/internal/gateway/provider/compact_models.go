package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway/compact"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// CompactModels holds compaction fallback settings and reads provider mappings and defaults on demand.
type CompactModels struct{ Default string }

type compactAttemptModels struct {
	defaults CompactModels
	target   *ExecutionProvider
}

func (p compactAttemptModels) ProviderModel(model string) (string, bool) {
	if p.target == nil {
		return "", false
	}
	return p.target.View().ResolveCompactMappedModel(model)
}
func (p compactAttemptModels) GlobalModel() string { return p.defaults.Default }
func (p compactAttemptModels) ResolveGlobalModel(model string) string {
	return ExecutionModelPolicy(p.target).OpenAIUpstream(model, false)
}

func (p CompactModels) Recovery(target *ExecutionProvider) compact.Recovery {
	return compact.Recovery{Models: compactAttemptModels{defaults: p, target: target}, ContextWindow: openai.IsOpenAIContextWindowError, RewriteModel: protocolopenai.ReplaceModelInBody}
}
