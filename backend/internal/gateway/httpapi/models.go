package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeldisplay"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// models.go builds model lists from requestable group models, custom
// visibility rules, and API-key aliases.
// Model list entries describe names that pass configured group and provider
// rules; they do not prove that an upstream account will accept a later
// request. Custom lists intersect that eligible set, then Key mappings
// contribute concrete aliases whose targets are still available. For ordinary
// Keys, a forced platform selects the response family and constrains resolution.
//
// Composite Keys aggregate authorized groups in binding order and attach each
// configured prefix to model names after alias expansion. Subscription-only
// Keys also require the selected subscription to cover each group. This
// ordering keeps a displayed alias tied to one permitted target and group,
// while deduplication retains the first occurrence. Metadata lookup supplies
// display fields after eligibility has been established.

// ModelsBackend supplies request identity and model eligibility for HTTP
// catalogue responses.
type ModelsBackend interface {
	// Access returns the current request Key and its presence flag.
	// Implementations can supply request-local storage; handlers inspect the
	// returned pointer before using group, alias or funding information.
	Access(*gin.Context) (*apikey.APIKey, bool)
	// ForcedPlatform returns the optional platform restriction supplied by route
	// middleware. A false presence flag or blank name allows ordinary list
	// resolution across platforms; a supplied platform constrains its candidate
	// scope.
	ForcedPlatform(*gin.Context) (string, bool)
	// Available reports whether configured model eligibility resolution is
	// currently bound.
	Available() bool
	// Resolve validates finite configured candidates for the supplied group and
	// optional platform. A nil group permits the implementation's unbound
	// catalogue scope; failure can produce an empty restricted result.
	Resolve(context.Context, *int64, string) routing.RequestableModelsResult
	// ResolveSelected validates only the supplied concrete names under one group
	// and platform. Single-resource callers pass a requested name and possible
	// exact alias target, keeping their lookup separate from complete list
	// enumeration.
	ResolveSelected(context.Context, *int64, string, []string) routing.RequestableModelsResult
	// PreferredSubscription returns the selected subscription eligible for
	// catalogue filtering. Subscription-only composite display requires a nonnil
	// result with a true presence flag before considering any bound group.
	PreferredSubscription(*gin.Context) (*billing.UserSubscription, bool)
	// SelectGemini obtains a model resource reader for the supplied group.
	SelectGemini(context.Context, *int64) (GeminiModelReader, error)
	// HasAntigravity reports whether the supplied group has Antigravity provider
	// candidates.
	HasAntigravity(context.Context, *int64) (bool, error)
	// CapacityLimited marks request telemetry for unavailable scheduler
	// candidates. Other errors leave that capacity classification unchanged,
	// allowing callers to separate a local model-policy failure from the absence
	// of usable providers.
	CapacityLimited(*gin.Context, error)
	// SafeModelSegment reports whether a model name is permitted as one URL
	// segment.
	SafeModelSegment(string) bool
}

// GeminiModelReader reads buffered model resources through a selected
// execution target.
type GeminiModelReader interface {
	// Read performs a buffered model query at the supplied path under ctx. The
	// response can accompany an error, so callers handle both observations
	// according to their HTTP presentation policy.
	Read(context.Context, string) (*ModelHTTPResponse, error)
}

// ModelHTTPResponse holds buffered status, headers and bytes from a model
// query.
type ModelHTTPResponse struct {
	StatusCode int         // upstream HTTP response status
	Headers    http.Header // buffered upstream response headers
	Body       []byte      // buffered upstream response bytes
}
type (
	// ModelsCatalog names the metadata lookup interface used by HTTP model
	// writers.
	ModelsCatalog = modeldisplay.Catalog
	// ModelsHandler combines configured model eligibility with protocol-specific
	// list and resource presentation.
	ModelsHandler struct {
		RequestLifetime // application active-request barrier binding

		backend ModelsBackend // identity and eligibility operations
		catalog ModelsCatalog // protocol display metadata lookup
		// attributes resolves group display overrides when supplied. A nil service
		// leaves displayAttributes to use the optional catalogue attributes capability.
		attributes *routing.ModelAttributeService
	}
)

type (
	// ClaudeModel names the Claude-compatible metadata value written by model
	// responses.
	ClaudeModel = modeldisplay.ClaudeModel
	// OpenAIModel names the OpenAI metadata value supplied by catalogue lookup.
	OpenAIModel = modeldisplay.OpenAIModel
	// GrokModel names the xAI metadata value extended with reasoning choices.
	GrokModel = modeldisplay.GrokModel
	// GeminiModel names the Gemini model resource value returned by HTTP
	// queries.
	GeminiModel = modeldisplay.GeminiModel
	// GeminiModelsList names the Gemini list envelope containing visible model
	// resources.
	GeminiModelsList = modeldisplay.GeminiModelsList
)

// grokReasoningEffortOption represents one client-selectable effort and its
// displayed default flag.
type grokReasoningEffortOption struct {
	Value   string `json:"value"`             // upstream effort selection value
	Label   string `json:"label"`             // client effort display label
	Default bool   `json:"default,omitempty"` // displayed default option flag
}

// grokModelListItem combines xAI model metadata with optional effort
// selection fields.
type grokModelListItem struct {
	GrokModel // base xAI model metadata
	// SupportsReasoningEffort, ReasoningEffort and ReasoningEfforts are populated
	// together when display attributes expose effort values. The selected default
	// is high when present, otherwise the first supplied option.
	// effort selection available flag
	SupportsReasoningEffort bool `json:"supportsReasoningEffort,omitempty"`
	// selected default effort value
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// ordered selectable effort options
	ReasoningEfforts []grokReasoningEffortOption `json:"reasoningEfforts,omitempty"`
	Type             string                      `json:"type"` // client model object type
	// empty catalogue creation timestamp
	CreatedAt string `json:"created_at"`
}

// NewModelsHandler binds eligibility and metadata providers for HTTP
// catalogue requests. The first optional attribute service supplies group
// display overrides; additional values are ignored and missing or nil values
// leave that service disabled.
func NewModelsHandler(backend ModelsBackend, catalog ModelsCatalog, attributes ...*routing.ModelAttributeService) *ModelsHandler {
	result := &ModelsHandler{backend: backend, catalog: catalog}
	if len(attributes) > 0 {
		result.attributes = attributes[0]
	}
	return result
}

// customListEnabled reports whether the group enables its configured model
// display list. Nil groups return false; the setting is interpreted through
// the routing group accessor rather than direct flag inspection.
func customListEnabled(g *routing.Group) bool {
	if g == nil {
		return false
	}
	return (&routing.Group{ModelsListConfig: g.ModelsListConfig}).CustomModelsListEnabled()
}

// Models resolves locally eligible model IDs and writes their selected
// response envelope. It reads current Key and route platform data, and a
// composite Key delegates to authorized binding aggregation. Custom lists are
// applied before concrete Key aliases are appended.
//
// The initial eligible routes also supply group display attributes. A forced
// platform constrains ordinary-Key resolution and protocol presentation; an
// unrestricted list uses the unified OpenAI envelope. An empty resolution
// remains empty, so metadata lookup cannot introduce a model that failed
// configured eligibility.
func (h *ModelsHandler) Models(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	apiKey, _ := h.backend.Access(c)
	if apiKey != nil && apiKey.IsComposite {
		models := h.CompositeRequestableModels(c, apiKey, "")
		if !c.IsAborted() {
			h.WriteCompositeModelsList(c, models)
		}
		return
	}

	var groupID *int64
	var platform string

	if apiKey != nil && apiKey.Group != nil {
		groupID = &apiKey.Group.ID
	}
	if forcedPlatform, ok := h.backend.ForcedPlatform(c); ok && strings.TrimSpace(forcedPlatform) != "" {
		platform = forcedPlatform
	}

	// Resolve configured candidate routes before metadata lookup so display names
	// and Key aliases cannot add models that group or provider policy rejected.
	resolution := h.backend.Resolve(c.Request.Context(), groupID, platform)
	if resolution.Err != nil {
		DefaultOpenAIErrorOutput().WriteStreamingErrorWithCode(c, http.StatusServiceUnavailable, "api_error", "MODEL_ATTRIBUTES_UNAVAILABLE", resolution.Err.Error(), false, false)
		return
	}
	h.captureModelAttributes(c, groupID, resolution.Models, "")
	availableModels := routing.RequestableModelIDs(resolution.Models)
	if apiKey != nil && apiKey.Group != nil && customListEnabled(apiKey.Group) {
		// Intersect before adding aliases so their target needs to remain visible
		// under both configured request eligibility and the group display selection.
		availableModels = FilterModelsByCustomList(availableModels, nil, apiKey.Group.ModelsListConfig.Models)
		availableModels = apikey.AppendAPIKeyModelAliases(availableModels, apiKey.ModelMapping)
		h.WriteCustomModelsList(c, platform, availableModels)
		return
	}
	if apiKey != nil {
		availableModels = apikey.AppendAPIKeyModelAliases(availableModels, apiKey.ModelMapping)
	}

	// An unrestricted catalogue combines platforms in one OpenAI list envelope,
	// while each entry retains its own resolved display and ownership metadata.
	if platform == "" {
		h.WriteUnifiedModelsList(c, availableModels)
		return
	}
	h.WriteDefaultModelsList(c, platform, availableModels)
}

// AntigravityModels lists names eligible for the forced Antigravity route in
// a Claude-compatible envelope. Composite Keys use the same platform
// restriction while aggregating their permitted bindings. Ordinary Keys apply
// custom-list filtering and concrete aliases to resolved names.
//
// A bound group, a restricted result, or available model IDs determines the
// resolved presentation path. Missing catalogue support and an empty unbound
// result still produce an empty list. No synthetic platform model is
// introduced to fill a failed or absent resolution.
func (h *ModelsHandler) AntigravityModels(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	apiKey, _ := h.backend.Access(c)
	if apiKey != nil && apiKey.IsComposite {
		models := h.CompositeRequestableModels(c, apiKey, capability.PlatformAntigravity)
		if !c.IsAborted() {
			h.WriteCompositeModelsList(c, models)
		}
		return
	}
	var groupID *int64
	if apiKey != nil && apiKey.Group != nil {
		value := apiKey.Group.ID
		groupID = &value
	}
	if h != nil && h.backend.Available() {
		resolution := h.backend.Resolve(c.Request.Context(), groupID, capability.PlatformAntigravity)
		if resolution.Err != nil {
			DefaultOpenAIErrorOutput().WriteAnthropicStreamingError(c, http.StatusServiceUnavailable, "api_error", resolution.Err.Error(), false)
			return
		}
		modelIDs := routing.RequestableModelIDs(resolution.Models)
		if apiKey != nil && apiKey.Group != nil && customListEnabled(apiKey.Group) {
			modelIDs = FilterModelsByCustomList(modelIDs, nil, apiKey.Group.ModelsListConfig.Models)
		}
		if apiKey != nil {
			modelIDs = apikey.AppendAPIKeyModelAliases(modelIDs, apiKey.ModelMapping)
		}
		if len(modelIDs) > 0 || resolution.Restricted || groupID != nil {
			h.WriteClaudeCompatiblePlatformModelsList(c, capability.PlatformAntigravity, modelIDs)
			return
		}
	} else if groupID != nil {
		h.WriteClaudeCompatiblePlatformModelsList(c, capability.PlatformAntigravity, nil)
		return
	}
	h.WriteClaudeCompatiblePlatformModelsList(c, capability.PlatformAntigravity, nil)
}

// CompositeRequestableModels aggregates prefixed model IDs from authorized
// composite bindings in their saved order. Missing handler, catalogue,
// request or Key returns nil, as does a missing required subscription. Each
// group first passes user, activity and subscription checks.
//
// Configured candidates are resolved under the optional platform restriction,
// intersected with custom lists, and expanded with concrete Key aliases. The
// binding prefix is added after these checks so one public name identifies
// its group and eligible model. Duplicate prefixed IDs retain their first
// occurrence in the aggregate.
func (h *ModelsHandler) CompositeRequestableModels(c *gin.Context, apiKey *apikey.APIKey, requiredPlatform string) []string {
	if h == nil || !h.backend.Available() || c == nil || c.Request == nil || apiKey == nil {
		return nil
	}
	preferredSubscription, ready := h.CompositePreferredSubscription(c, apiKey)
	if !ready {
		return nil
	}
	ctx := c.Request.Context()
	models := make([]string, 0)
	seen := make(map[string]struct{})
	for _, binding := range apiKey.CompositeGroups {
		group := binding.Group
		if !CompositeGroupAvailableToUser(apiKey, preferredSubscription, group) {
			continue
		}
		groupID := group.ID
		resolution := h.backend.Resolve(ctx, &groupID, requiredPlatform)
		if resolution.Err != nil {
			DefaultOpenAIErrorOutput().WriteStreamingErrorWithCode(c, http.StatusServiceUnavailable, "api_error", "MODEL_ATTRIBUTES_UNAVAILABLE", resolution.Err.Error(), false, false)
			c.Abort()
			return nil
		}
		available := routing.RequestableModelIDs(resolution.Models)
		if customListEnabled(group) {
			available = FilterModelsByCustomList(available, nil, group.ModelsListConfig.Models)
		}
		// Expand aliases within each eligible group before adding its prefix. This
		// prevents an alias from borrowing target availability from a different binding.
		available = apikey.AppendAPIKeyModelAliases(available, apiKey.ModelMapping)
		for _, model := range available {
			prefixed := binding.Prefix + "/" + model
			if _, exists := seen[prefixed]; exists {
				continue
			}
			seen[prefixed] = struct{}{}
			models = append(models, prefixed)
		}
	}
	return models
}

// CompositePreferredSubscription supplies subscription-only catalogue
// filtering and a readiness flag. Other billing modes return nil and true;
// subscription mode requires a present nonnil backend subscription before
// bindings can be displayed.
func (h *ModelsHandler) CompositePreferredSubscription(c *gin.Context, apiKey *apikey.APIKey) (*billing.UserSubscription, bool) {
	if apikey.APIKeyEffectiveBillingMode(apiKey) != apikey.APIKeyBillingModeSubscription {
		return nil, true
	}
	subscription, ok := h.backend.PreferredSubscription(c)
	if !ok || subscription == nil {
		return nil, false
	}
	return subscription, true
}

// WriteCompositeModelsList writes already-prefixed IDs in the OpenAI list
// envelope. It uses the public ID as display name, TokenRouter ownership, and
// empty or zero timestamps because group aggregation supplies no merged
// vendor metadata.
func (h *ModelsHandler) WriteCompositeModelsList(c *gin.Context, modelIDs []string) {
	models := make([]gin.H, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, gin.H{
			"id": modelID, "object": "model", "type": "model", "created": 0,
			"created_at": "", "owned_by": "token-router", "display_name": modelID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
}

// WriteModelsList writes Claude-compatible entries using the supplied IDs and
// catalogue display names.
func (h *ModelsHandler) WriteModelsList(c *gin.Context, modelIDs []string) {
	models := make([]ClaudeModel, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, ClaudeModel{
			ID:          modelID,
			Type:        "model",
			DisplayName: h.catalog.Model(modelID).DisplayName,
			CreatedAt:   "",
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

// WriteCustomModelsList chooses the response envelope for a filtered custom
// model list. Empty and OpenAI platform values use unified display, Grok
// includes effort metadata, and other platforms use Claude-compatible
// entries.
func (h *ModelsHandler) WriteCustomModelsList(c *gin.Context, platform string, modelIDs []string) {
	switch platform {
	case "", capability.PlatformOpenAI:
		h.WriteUnifiedModelsList(c, modelIDs)
	case capability.PlatformGrok:
		h.WriteGrokModelsList(c, modelIDs)
	default:
		h.WriteModelsList(c, modelIDs)
	}
}

// WriteGrokModelsList writes xAI entries with effort choices from display
// attributes. Effort metadata is present when resolved values exist, and
// their selected default follows grokEffortOptions; caller-supplied model
// order remains intact.
func (h *ModelsHandler) WriteGrokModelsList(c *gin.Context, modelIDs []string) {
	models := make([]grokModelListItem, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		metadata := h.catalog.Model(modelID)
		model := GrokModel{ID: modelID, Object: "model", OwnedBy: "xai", DisplayName: metadata.DisplayName}
		item := grokModelListItem{
			GrokModel: model,
			Type:      "model",
			CreatedAt: "",
		}
		attributes := h.displayAttributes(c, modelID)
		if efforts := attributes.Efforts(); len(efforts) > 0 {
			item.SupportsReasoningEffort = true
			item.ReasoningEffort, item.ReasoningEfforts = grokEffortOptions(efforts)
		}
		models = append(models, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

// WriteDefaultModelsList selects a platform-specific model list envelope for
// already eligible IDs. OpenAI and Grok have dedicated writers; supported
// Claude-compatible platforms and unknown platforms use their respective
// generic presentation paths.
func (h *ModelsHandler) WriteDefaultModelsList(c *gin.Context, platform string, modelIDs []string) {
	switch platform {
	case capability.PlatformOpenAI:
		h.WriteOpenAIModelsList(c, modelIDs)
	case capability.PlatformGrok:
		h.WriteGrokModelsList(c, modelIDs)
	case capability.PlatformAnthropic, capability.PlatformGemini, capability.PlatformAntigravity, capability.PlatformQoder:
		h.WriteClaudeCompatiblePlatformModelsList(c, platform, modelIDs)
	default:
		h.WriteModelsList(c, modelIDs)
	}
}

// WriteOpenAIModelsList writes catalogue metadata for each supplied eligible
// OpenAI model ID.
func (h *ModelsHandler) WriteOpenAIModelsList(c *gin.Context, modelIDs []string) {
	models := make([]OpenAIModel, 0, len(modelIDs))
	for _, id := range modelIDs {
		models = append(models, h.catalog.Model(id))
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

// WriteClaudeCompatiblePlatformModelsList writes eligible IDs and catalogue
// display names in Claude-compatible entries.
func (h *ModelsHandler) WriteClaudeCompatiblePlatformModelsList(c *gin.Context, platform string, modelIDs []string) {
	models := make([]ClaudeModel, 0, len(modelIDs))
	for _, id := range modelIDs {
		metadata := h.catalog.Model(id)
		models = append(models, ClaudeModel{ID: id, Type: "model", DisplayName: metadata.DisplayName})
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

// FilterModelsByCustomList intersects configured display names with available
// exact or trailing-star patterns. An empty selected list returns
// availableModels directly, retaining its storage and order. When
// availableModels is empty, fallbackModels supplies the pattern source; an
// empty source returns nil.
//
// For an active selection, source and selected entries are trimmed, blanks
// are ignored, and matching names retain selected order with duplicates
// removed. Patterns determine eligibility of a concrete selected name; they
// are not expanded into a generated catalogue. This distinguishes absent
// display configuration from a configured selection whose entries are all
// unavailable.
func FilterModelsByCustomList(availableModels, fallbackModels, selectedModels []string) []string {
	if len(selectedModels) == 0 {
		return availableModels
	}
	// Empty availability can use the explicit pattern fallback supplied by the
	// caller. Production list callers pass nil, so an empty catalogue stays empty.
	source := availableModels
	if len(source) == 0 {
		source = fallbackModels
	}
	if len(source) == 0 {
		return nil
	}

	allowed := make([]string, 0, len(source))
	for _, model := range source {
		model = strings.TrimSpace(model)
		if model != "" {
			allowed = append(allowed, model)
		}
	}

	seen := make(map[string]struct{}, len(selectedModels))
	filtered := make([]string, 0, len(selectedModels))
	for _, model := range selectedModels {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if !CustomModelsListAllowsModel(allowed, model) {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		filtered = append(filtered, model)
	}
	return filtered
}

// CustomModelsListAllowsModel matches an exact name or a trailing-star prefix
// pattern. Inputs are used as supplied; FilterModelsByCustomList performs
// trimming before calling it, so direct callers normalize their own values.
func CustomModelsListAllowsModel(availablePatterns []string, model string) bool {
	for _, pattern := range availablePatterns {
		if pattern == model {
			return true
		}
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(model, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

// CompositeGroupAvailableToUser checks active-group, user-binding and
// selected-subscription eligibility for catalogue display. Missing Key, user
// or group rejects the binding; subscription-only Keys also require the
// preferred subscription to permit that group.
func CompositeGroupAvailableToUser(apiKey *apikey.APIKey, preferredSubscription *billing.UserSubscription, group *routing.Group) bool {
	if apiKey == nil || apiKey.User == nil || group == nil || !group.IsActive() {
		return false
	}
	if apikey.APIKeyEffectiveBillingMode(apiKey) == apikey.APIKeyBillingModeSubscription && !billing.SubscriptionAllowsGroup(preferredSubscription, group.ID) {
		return false
	}
	return apiKey.User.CanBindGroup(group.ID, group.IsExclusive)
}
