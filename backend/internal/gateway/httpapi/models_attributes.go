package httpapi

import (
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

const modelAttributesContextKey = "gateway_model_attributes"

type modelDisplayAttributes struct {
	values map[string]modelcatalog.Attributes
	grok   map[string]bool
}

func (h *ModelsHandler) captureModelAttributes(c *gin.Context, groupID *int64, models []routing.RequestableModel, prefix string) {
	if h.attributes == nil || groupID == nil {
		return
	}
	value := &modelDisplayAttributes{values: map[string]modelcatalog.Attributes{}, grok: map[string]bool{}}
	if prior := modelDisplayFromContext(c); prior != nil {
		value = prior
	}
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
				value.values[prefix+alias] = value.values[prefix+mapped]
				value.grok[prefix+alias] = value.grok[prefix+mapped]
			}
		}
	}
	c.Set(modelAttributesContextKey, value)
}

func (h *ModelsHandler) displayAttributes(c *gin.Context, model string) modelcatalog.Attributes {
	if raw := modelDisplayFromContext(c); raw != nil {
		values := raw.values
		if attrs, exists := values[model]; exists {
			return attrs
		}
		if key, ok := h.backend.Access(c); ok && key != nil {
			if mapped, changed := key.ResolveModelMapping(model); changed {
				return values[mapped]
			}
		}
		return modelcatalog.Attributes{}
	}
	if source, ok := h.catalog.(interface {
		ModelAttributes(string) modelcatalog.Attributes
	}); ok {
		return source.ModelAttributes(model)
	}
	return modelcatalog.Attributes{}
}

func (h *ModelsHandler) grokDisplayModel(c *gin.Context, model, owner string) bool {
	if owner == "xai" {
		return true
	}
	if raw := modelDisplayFromContext(c); raw != nil {
		values := raw.grok
		if values[model] {
			return true
		}
		if key, ok := h.backend.Access(c); ok && key != nil {
			if mapped, changed := key.ResolveModelMapping(model); changed {
				return values[mapped]
			}
		}
	}
	return false
}

func grokEffortOptions(values []string) (string, []grokReasoningEffortOption) {
	if len(values) == 0 {
		return "", nil
	}
	defaultValue := values[0]
	if slices.Contains(values, "high") {
		defaultValue = "high"
	}
	options := make([]grokReasoningEffortOption, 0, len(values))
	for _, value := range values {
		label := value
		if value != "" {
			label = strings.ToUpper(value[:1]) + value[1:]
		}
		if value == "xhigh" {
			label = "xHigh"
		}
		options = append(options, grokReasoningEffortOption{Value: value, Label: label, Default: value == defaultValue})
	}
	return defaultValue, options
}

func modelDisplayFromContext(c *gin.Context) *modelDisplayAttributes {
	value, _ := c.Get(modelAttributesContextKey)
	result, _ := value.(*modelDisplayAttributes)
	return result
}
