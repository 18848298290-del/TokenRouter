package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// ModelAttributeTarget 从提供商配置提取能力查询所需的路线字段。
func (r *Record) ModelAttributeTarget(model, upstreamProtocol string) modelcatalog.Target {
	target := modelcatalog.Target{Model: model, UpstreamProtocol: upstreamProtocol}
	if r == nil {
		return target
	}
	target.Platform, target.ProviderType = r.Platform, r.Type
	target.ProviderMode, target.Site = r.GetCredential("provider_mode"), r.GetCredential("site")
	target.BaseURL = r.GetCredential("base_url")
	if r.IsCNProvider() {
		target.ProviderMode = r.GetProviderMode()
		protocol := APIProtocolChatCompletions
		switch upstreamProtocol {
		case "anthropic_messages":
			protocol = APIProtocolAnthropic
		case "openai_responses":
			protocol = APIProtocolResponses
		}
		target.BaseURL = (ProtocolTarget{Record: r}).GetCNProtocolBaseURL(protocol)
	}
	if r.Platform == "anthropic" && r.IsCustomBaseURLEnabled() && r.GetCustomBaseURL() != "" {
		target.BaseURL = r.GetCustomBaseURL()
	}
	if target.Platform == "qoder" && target.Site == "" {
		target.Site = "global"
	}
	chat := upstreamProtocol == "openai_chat_completions"
	if upstreamProtocol == "" {
		chat = ResolveUpstreamTextProtocol(r.Extra, TextProtocolResponses) == TextProtocolChatCompletions
	}
	if r.Platform == "openai" && r.Type == "apikey" && chat {
		target.Ollama = egress.IsOllamaCloudBaseURL(target.BaseURL)
		for _, key := range []string{OllamaCloudUsageSessionExtraKey, OllamaCloudUsageAutoRefreshExtraKey, OllamaCloudUsageSnapshotExtraKey} {
			if _, found := r.Extra[key]; found {
				target.Ollama = true
			}
		}
		if target.Ollama {
			value, present := r.Extra[modelcatalog.OllamaOutputCapExtraKey]
			limit := int(modelcatalog.OllamaOutputCap(value, present))
			target.OllamaOutputCap = &limit
		}
	}
	return target.WithServiceIdentity()
}
