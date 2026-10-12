package modelcatalog

import (
	"maps"
	"net/url"
	"slices"
	"strings"
)

// Target 保存决定模型能力的路线信息，Model 是本次实际发送的完整 ID。
type Target struct {
	Model            string `json:"model"`
	Platform         string `json:"platform,omitempty"`
	ProviderType     string `json:"provider_type,omitempty"`
	ProviderMode     string `json:"provider_mode,omitempty"`
	Site             string `json:"site,omitempty"`
	CatalogProvider  string `json:"catalog_provider,omitempty"`
	ClientProtocol   string `json:"client_protocol,omitempty"`
	UpstreamProtocol string `json:"upstream_protocol,omitempty"`
	Endpoint         string `json:"endpoint,omitempty"`
	BaseURL          string `json:"-"`
	Ollama           bool   `json:"-"`
	OllamaOutputCap  *int   `json:"-"`
}

// RequestState 区分展示时的未知状态与请求明确指定的推理选项。
type RequestState struct {
	Effort   *string
	Thinking *bool
}

// ExecutionRules 保存供应商报文需要的选择项。字段由平台实现解释。
type ExecutionRules struct {
	ThinkingMode           string            `json:"thinking_mode,omitempty"`
	SignatureMode          string            `json:"signature_mode,omitempty"`
	EffortAliases          map[string]string `json:"effort_aliases,omitempty"`
	DropFields             []string          `json:"drop_fields,omitempty"`
	OmitEmptyToolConfig    bool              `json:"omit_empty_tool_config,omitempty"`
	OmitStopSequences      bool              `json:"omit_stop_sequences,omitempty"`
	AdaptiveThinkingBudget int               `json:"adaptive_thinking_budget,omitempty"`
	BudgetPadding          int               `json:"budget_padding,omitempty"`
	RuntimeContext         bool              `json:"runtime_context,omitempty"`
	ImageGenerationTool    *bool             `json:"image_generation_tool,omitempty"`
	ClampOutput            bool              `json:"clamp_output,omitempty"`
	OutputParameter        *bool             `json:"output_parameter,omitempty"`
	CacheTTL               []string          `json:"cache_ttl,omitempty"`
	ToolSearch             *bool             `json:"tool_search,omitempty"`
	Verbosity              *bool             `json:"verbosity,omitempty"`
}

// Resolution 是一条路线的模型属性、字段来源和报文规则。
type Resolution struct {
	Target     Target            `json:"target"`
	Attributes Attributes        `json:"attributes"`
	Sources    map[string]string `json:"sources"`
	Rules      ExecutionRules    `json:"rules"`
	Version    string            `json:"version"`
}

// View 持有一次已发布目录，查询返回值副本，热更新不会改变已取得的 View。
type View struct{ catalog *Catalog }

// TargetKey 按值区分所有影响属性的路线字段。
type TargetKey struct {
	target       Target
	outputCap    int
	hasOutputCap bool
}

func (c *Catalog) View() View { return View{catalog: c} }

func (v View) Version() string {
	if v.catalog == nil {
		return ""
	}
	return v.catalog.Version
}

// Resolve 合并目录、平台差异和管理员覆盖，并应用当前转换器的格式限制。
func (v View) Resolve(target Target, override Attributes) Resolution {
	target = target.WithServiceIdentity()
	result := Resolution{Target: target, Sources: map[string]string{}, Version: v.Version()}
	model := target.Model
	if target.Platform == "qoder" {
		model = QoderModelIdentity(target.Site, model)
	}
	if target.Platform == "antigravity" && model == "claude-sonnet-4-5-thinking" {
		model = "claude-sonnet-4-5"
	}
	entry := v.lookup(target, model)
	result.Attributes = entry.Attributes.Clone()
	for _, field := range result.Attributes.fieldNames() {
		result.Sources[field] = "models.dev"
	}
	maps.Copy(result.Sources, entry.AttributeSources)
	result.applyPlatform()
	result.patch(override, "config")
	if result.Attributes.Reasoning != nil && !*result.Attributes.Reasoning {
		result.Attributes.ReasoningOptions = valuePointer([]ReasoningOption{})
		result.Sources["reasoning_options"] = result.Sources["reasoning"]
	}
	result.applyAdapter()
	return result
}

func (v View) lookup(target Target, model string) Entry {
	if model == "" || v.catalog == nil {
		return Entry{}
	}
	model = normalize(model)
	if target.Platform == "gemini" || target.Platform == "antigravity" {
		model = googleResourceModel(model)
	}
	if prefix, _, qualified := strings.Cut(model, "/"); qualified && v.catalog.Providers[prefix] {
		if entry, found := v.catalog.Lookup([]string{model}); found && entry.Provider == prefix {
			return entry
		}
		return v.catalog.publicEntries[model]
	}
	service := target.ServiceID()
	if service == "" {
		if entry, found := v.catalog.Lookup([]string{model}); found {
			return entry
		}
		attrs, _ := v.catalog.LookupAttributes(model, nil)
		return Entry{Attributes: attrs}
	}
	if !v.catalog.Providers[service] {
		if entry, exists := v.catalog.publicEntries[service+"/"+model]; exists {
			return entry
		}
	}
	if entry, exists := v.catalog.Lookup([]string{service + "/" + model}); exists && entry.Provider == service {
		return entry
	}
	// 服务没有该型号时，使用明确的公共资料，防止借用中继的服务限制。
	for _, identity := range []string{model, publicLab(service) + "/" + model} {
		if entry, found := v.catalog.publicEntries[identity]; found {
			return entry
		}
	}
	if entry, found := v.catalog.Lookup([]string{model}); found && entry.Provider == service {
		return entry
	}
	return Entry{}
}

func googleResourceModel(model string) string {
	parts := strings.Split(model, "/")
	if len(parts) == 2 && parts[0] == "models" {
		return parts[1]
	}
	if len(parts) == 4 && parts[0] == "publishers" && parts[1] == "google" && parts[2] == "models" {
		return parts[3]
	}
	if len(parts) == 8 && parts[0] == "projects" && parts[1] != "" && parts[2] == "locations" && parts[3] != "" && parts[4] == "publishers" && parts[5] == "google" && parts[6] == "models" {
		return parts[7]
	}
	return model
}

func (r *Resolution) patch(attrs Attributes, source string) {
	for _, field := range attrs.fieldNames() {
		if field != "media" {
			r.Sources[field] = source
		}
	}
	if attrs.Media != nil {
		for _, field := range attrs.Media.fieldNames() {
			r.Sources["media."+field] = source
		}
	}
	r.Attributes = Merge(r.Attributes, attrs)
}

// ForRequest 根据本次推理状态计算条件属性，调用不会改变展示值。
func (r Resolution) ForRequest(state RequestState) Resolution {
	r.Attributes = r.Attributes.Clone()
	if state.Thinking == nil {
		switch r.Target.servicePlatform() {
		case "anthropic":
			state.Thinking = valuePointer(r.Rules.ThinkingMode == "adaptive" || r.Rules.ThinkingMode == "adaptive_between_tools")
		case "deepseek":
			state.Thinking = valuePointer(true)
		}
	}
	if r.Attributes.Conditions == nil {
		return r
	}
	for parameter, condition := range *r.Attributes.Conditions {
		allowed := true
		if condition.Thinking != nil {
			allowed = state.Thinking != nil && *state.Thinking == *condition.Thinking
		}
		if len(condition.Efforts) > 0 {
			effort := ""
			if state.Effort != nil {
				effort = *state.Effort
			} else if slices.Contains([]string{"gpt-5.2", "gpt-5.4"}, strings.TrimPrefix(normalize(r.Target.Model), "openai/")) {
				effort = "none"
			}
			allowed = allowed && slices.Contains(condition.Efforts, effort)
		}
		if !allowed {
			switch parameter {
			case "temperature":
				r.Attributes.Temperature = valuePointer(false)
			case "top_p":
				r.Attributes.TopP = valuePointer(false)
			case "tool_call":
				r.Attributes.ToolCall = valuePointer(false)
			}
		}
	}
	return r
}

func (t Target) ServiceID() string {
	if t.CatalogProvider != "" {
		return t.CatalogProvider
	}
	u, _ := url.Parse(t.BaseURL)
	host := ""
	if u != nil {
		host = strings.ToLower(u.Hostname())
	}
	if t.Platform == "openai" && t.ProviderType != "oauth" && t.ProviderType != "setup-token" {
		path := ""
		if u != nil {
			path = u.Path
		}
		if service := compatibleService(host, path, t.ProviderMode); service != "" {
			return service
		}
	}
	switch t.Platform {
	case "openai":
		if t.ProviderType == "oauth" || t.ProviderType == "setup-token" || host == "" || host == "api.openai.com" || host == "chatgpt.com" {
			return "openai"
		}
	case "anthropic":
		switch t.ProviderType {
		case "bedrock":
			return "amazon-bedrock"
		case "service_account":
			return "google-vertex-anthropic"
		}
		if t.ProviderType == "oauth" || t.ProviderType == "setup-token" || host == "" || host == "api.anthropic.com" {
			return "anthropic"
		}
	case "gemini":
		if t.ProviderType == "service_account" {
			return "google-vertex"
		}
		if t.ProviderType == "oauth" || host == "" || host == "generativelanguage.googleapis.com" {
			return "google"
		}
	case "grok":
		if t.ProviderType == "oauth" || host == "" || host == "api.x.ai" || strings.HasSuffix(host, ".api.x.ai") {
			return "xai"
		}
	case "kimi":
		if t.ProviderMode == "coding" {
			if host == "api.kimi.com" || host == "" && t.Site != "global" {
				return "kimi-code-plan-cn"
			}
			if host == "api.kimi.ai" || host == "" && t.Site == "global" {
				return "kimi-code-plan-global"
			}
		} else if host == "" || host == "api.moonshot.cn" {
			return "moonshotai-cn"
		} else if host == "api.moonshot.ai" {
			return "moonshotai"
		}
	case "zhipu":
		service := ""
		switch host {
		case "", "open.bigmodel.cn":
			service = "zhipuai"
		case "api.z.ai":
			service = "zai"
		}
		if service != "" && t.ProviderMode == "coding" {
			service += "-coding-plan"
		}
		return service
	case "deepseek":
		if host == "" || host == "api.deepseek.com" {
			return "deepseek"
		}
	case "jev":
		if host == "" || host == "api.typesafe.ai" {
			return "typesafe"
		}
	}
	return ""
}

// WithServiceIdentity 填入目录服务与已识别的站点，供路线规则和目录查询共用。
func (t Target) WithServiceIdentity() Target {
	t.CatalogProvider = t.ServiceID()
	if t.ClientProtocol == "openai_responses_compact" || t.UpstreamProtocol == "openai_responses_compact" {
		t.Endpoint = "compact"
	}
	switch t.CatalogProvider {
	case "kimi-code-plan-cn", "kimi-code-plan-global", "zhipuai-coding-plan", "zai-coding-plan":
		t.ProviderMode = "coding"
	case "moonshotai", "moonshotai-cn", "zhipuai", "zai":
		t.ProviderMode = "payg"
	}
	switch t.CatalogProvider {
	case "moonshotai-cn", "kimi-code-plan-cn", "zhipuai", "zhipuai-coding-plan":
		t.Site = "cn"
	case "moonshotai", "kimi-code-plan-global", "zai", "zai-coding-plan":
		t.Site = "global"
	}
	if t.Platform == "qoder" && t.Site == "" {
		t.Site = "global"
	}
	return t
}

func publicLab(service string) string {
	switch service {
	case "moonshotai-cn", "kimi-code-plan-cn", "kimi-code-plan-global":
		return "moonshotai"
	case "zai", "zai-coding-plan", "zhipuai-coding-plan":
		return "zhipuai"
	case "google-vertex":
		return "google"
	case "google-vertex-anthropic":
		return "anthropic"
	}
	return service
}

func compatibleService(host, path, mode string) string {
	switch host {
	case "api.anthropic.com":
		return "anthropic"
	case "generativelanguage.googleapis.com":
		return "google"
	case "api.x.ai":
		return "xai"
	case "api.deepseek.com":
		return "deepseek"
	case "api.moonshot.cn":
		return "moonshotai-cn"
	case "api.moonshot.ai":
		return "moonshotai"
	case "api.kimi.com":
		return "kimi-code-plan-cn"
	case "api.kimi.ai":
		return "kimi-code-plan-global"
	case "open.bigmodel.cn", "api.z.ai":
		service := "zhipuai"
		if host == "api.z.ai" {
			service = "zai"
		}
		if mode == "coding" || strings.Contains(path, "/coding/") {
			service += "-coding-plan"
		}
		return service
	case "ollama.com", "api.ollama.com":
		return "ollama-cloud"
	case "api.typesafe.ai":
		return "typesafe"
	default:
		return ""
	}
}

func (t Target) servicePlatform() string {
	if t.Platform != "openai" || t.ProviderType == "oauth" || t.ProviderType == "setup-token" {
		return t.Platform
	}
	switch service := t.ServiceID(); service {
	case "anthropic", "deepseek":
		return service
	case "xai":
		return "grok"
	case "google":
		return "gemini"
	case "moonshotai", "moonshotai-cn", "kimi-code-plan-cn", "kimi-code-plan-global":
		return "kimi"
	case "zhipuai", "zhipuai-coding-plan", "zai", "zai-coding-plan":
		return "zhipu"
	case "typesafe":
		return "jev"
	default:
		return t.Platform
	}
}

func (t Target) Key() TargetKey {
	t = t.WithServiceIdentity()
	key := TargetKey{target: t}
	if t.OllamaOutputCap != nil {
		key.outputCap, key.hasOutputCap = *t.OllamaOutputCap, true
		key.target.OllamaOutputCap = nil
	}
	return key
}

// ForAttributes reuses the resolved service identity and omits the raw address.
// Caches that return the route itself use the full key.
func (key TargetKey) ForAttributes() TargetKey {
	key.target.BaseURL = ""
	return key
}

// Clone 返回可独立修改的路线解析结果。
func (r Resolution) Clone() Resolution {
	r.Target.OllamaOutputCap = clonePointer(r.Target.OllamaOutputCap)
	r.Attributes = r.Attributes.Clone()
	r.Sources = maps.Clone(r.Sources)
	r.Rules.EffortAliases = maps.Clone(r.Rules.EffortAliases)
	r.Rules.DropFields = slices.Clone(r.Rules.DropFields)
	r.Rules.CacheTTL = slices.Clone(r.Rules.CacheTTL)
	r.Rules.ImageGenerationTool = clonePointer(r.Rules.ImageGenerationTool)
	r.Rules.OutputParameter = clonePointer(r.Rules.OutputParameter)
	r.Rules.ToolSearch = clonePointer(r.Rules.ToolSearch)
	r.Rules.Verbosity = clonePointer(r.Rules.Verbosity)
	return r
}
