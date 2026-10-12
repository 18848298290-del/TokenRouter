package httpapi

import (
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Model display overrides are resolved against the eligible routes for a
// group. Request-local storage keeps those values beside Grok-route flags,
// allowing an eligible Key alias to reuse its target's attributes while the
// public model ID stays unchanged.
//
// An attribute lookup failure leaves route visibility intact and still
// records Grok participation. Once request-local display state exists, a
// missing model attribute stays unknown rather than consulting catalogue
// defaults; otherwise a catalogue exposing ModelAttributes supplies the
// optional default. Effort options follow configured order, choose high when
// available, and carry the selected default flag into the HTTP list. These
// display decisions describe resolved capability metadata, while actual
// request admission remains elsewhere.

// modelAttributesContextKey identifies request-local attributes and route
// flags used by the model list writers after eligibility resolution.
const modelAttributesContextKey = "gateway_model_attributes"

// modelDisplayAttributes retains request-local display values and Grok route
// participation flags.
type modelDisplayAttributes struct {
	// values and grok use the same public model IDs, including any supplied prefix.
	// Both maps are extended within the request; alias entries inherit their target
	// values, and nested attribute storage remains shared for read-only display.
	values map[string]modelcatalog.Attributes // resolved model display attributes
	grok   map[string]bool                    // model has Grok route
}

// captureModelAttributes resolves eligible routes' display attributes for one
// group and retains them under prefixed public IDs. It skips work when the
// attribute service or group ID is absent. Existing request-local state is
// extended, so callers keep this operation within one request.
//
// Resolution errors leave attributes unknown while Grok-route participation
// is still collected. Names rewritten by a Key mapping inherit their stored
// target attribute value and Grok flag; absent targets supply unknown values.
// Map value assignments retain shared nested attribute storage, so display
// consumers treat those values as read-only. Request-local state subsequently
// controls lookup even when a requested model has no stored attributes.
func (h *ModelsHandler) captureModelAttributes(c *gin.Context, groupID *int64, models []routing.RequestableModel, prefix string) {
	if h.attributes == nil || groupID == nil {
		return
	}
	value := &modelDisplayAttributes{values: map[string]modelcatalog.Attributes{}, grok: map[string]bool{}}
	if prior := modelDisplayFromContext(c); prior != nil {
		value = prior
	}
	// Attribute errors do not remove eligible routes. Continue collecting their
	// platform flags so display can classify Grok entries with unknown attributes.
	attributes, err := h.attributes.ResolveModels(c.Request.Context(), *groupID, models)
	for _, model := range models {
		id := prefix + model.ID
		if err == nil {
			value.values[id] = attributes[model.ID].Attributes
		}
		for _, target := range model.AttributeTargets {
			value.grok[id] = value.grok[id] || target.Platform == "grok"
		}
	}
	if key, ok := h.backend.Access(c); ok && key != nil {
		for _, alias := range apikey.AppendAPIKeyModelAliases(routing.RequestableModelIDs(models), key.ModelMapping) {
			if mapped, changed := key.ResolveModelMapping(alias); changed {
				// A mapped public name inherits stored target fields under the same prefix.
				// Missing targets leave attributes unknown and Grok participation false.
				value.values[prefix+alias] = value.values[prefix+mapped]
				value.grok[prefix+alias] = value.grok[prefix+mapped]
			}
		}
	}
	c.Set(modelAttributesContextKey, value)
}

// displayAttributes chooses display metadata for a public model ID while
// respecting request-local resolution. It returns an exact stored entry
// first, then a changed one-hop Key target's stored value; callers treat any
// nested attribute storage as read-only. Missing targets produce unknown
// attributes.
//
// The presence of request-local state closes the catalogue default path even
// when that state has no attributes for this model. That distinction keeps a
// failed or missing group attribute unknown during list presentation. When no
// local state exists, an optional catalogue ModelAttributes reader supplies
// defaults; a catalogue lacking that capability also returns unknown
// attributes.
func (h *ModelsHandler) displayAttributes(c *gin.Context, model string) modelcatalog.Attributes {
	if raw := modelDisplayFromContext(c); raw != nil {
		values := raw.values
		if attrs, exists := values[model]; exists {
			return attrs
		}
		// When the public name has no stored attributes, a changed Key alias can
		// reuse its target under the same local resolution, including an unknown
		// target value.
		if key, ok := h.backend.Access(c); ok && key != nil {
			if mapped, changed := key.ResolveModelMapping(model); changed {
				return values[mapped]
			}
		}
		return modelcatalog.Attributes{}
	}
	// Catalogue defaults apply before any request-local resolution. Keeping a
	// missing entry unknown after resolution prevents bypassing failed group attributes.
	if source, ok := h.catalog.(interface {
		// ModelAttributes returns catalogue capability attributes for one model ID.
		ModelAttributes(string) modelcatalog.Attributes
	}); ok {
		return source.ModelAttributes(model)
	}
	return modelcatalog.Attributes{}
}

// grokDisplayModel chooses whether a public list entry uses xAI effort
// presentation. Exact xai catalogue ownership returns true before
// request-local reads, allowing that known owner to qualify when route
// attributes have not been captured. Callers supply the public ID and its
// catalogue owner.
//
// Otherwise a true request-local Grok flag qualifies the ID, then a changed
// one-hop Key mapping can reuse its target's flag. Missing state, unmapped
// names and absent or false target flags return false. This priority lets
// eligible Grok participation select its presentation for names whose
// catalogue owner describes another platform, while retaining the caller's
// public model spelling.
func (h *ModelsHandler) grokDisplayModel(c *gin.Context, model, owner string) bool {
	if owner == "xai" {
		return true
	}
	if raw := modelDisplayFromContext(c); raw != nil {
		values := raw.grok
		if values[model] {
			return true
		}
		// A public alias without a positive flag can inherit its mapped target's
		// Grok participation, so display follows the eligible target while keeping
		// the alias ID.
		if key, ok := h.backend.Access(c); ok && key != nil {
			if mapped, changed := key.ResolveModelMapping(model); changed {
				return values[mapped]
			}
		}
	}
	return false
}

// grokEffortOptions builds ordered display options and returns their chosen
// default value. Empty input returns an empty default and nil options. High
// is preferred when present; otherwise the first supplied value becomes the
// default.
//
// Every input entry retains its exact Value and position, including
// duplicates or empty strings. Labels capitalize the first-byte prefix of
// nonempty values, with xhigh displayed as xHigh. Each Default flag compares
// its retained value with the chosen default, so duplicate default values
// each receive that flag. Callers display labels while sending the retained
// values upstream.
func grokEffortOptions(values []string) (string, []grokReasoningEffortOption) {
	if len(values) == 0 {
		return "", nil
	}
	defaultValue := values[0]
	// The returned default controls each option's Default flag. Prefer the
	// supplied high value while retaining configured ordering for the separate
	// client choice list.
	if slices.Contains(values, "high") {
		defaultValue = "high"
	}
	options := make([]grokReasoningEffortOption, 0, len(values))
	for _, value := range values {
		label := value
		if value != "" {
			label = strings.ToUpper(value[:1]) + value[1:]
		}
		// Format the xhigh option with its established client label while retaining
		// the upstream effort value and configured order used to select it.
		if value == "xhigh" {
			label = "xHigh"
		}
		options = append(options, grokReasoningEffortOption{Value: value, Label: label, Default: value == defaultValue})
	}
	return defaultValue, options
}

// modelDisplayFromContext returns typed request-local model attributes from a
// usable Gin context. An absent key, different stored type or typed nil
// returns nil, allowing display callers to select catalogue defaults.
func modelDisplayFromContext(c *gin.Context) *modelDisplayAttributes {
	value, _ := c.Get(modelAttributesContextKey)
	result, _ := value.(*modelDisplayAttributes)
	return result
}
