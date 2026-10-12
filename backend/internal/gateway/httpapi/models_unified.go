package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// An unrestricted model list presents admitted IDs in one OpenAI envelope
// even when their eligible routes cross platforms. Catalogue display metadata
// is combined with request-local group attributes, and a nonnil display-name
// override can replace the label with an empty string. Grok-route
// participation can select xAI ownership even when the catalogue names
// another owner.
//
// The unified writer retains each public ID while changing the fields needed
// for Grok effort presentation. Ordinary entries keep catalogue creation
// time; the constructed Grok entry leaves that time zero. Effort values
// retain their configured order and chosen default through the shared option
// builder. These fields describe presentation of already admitted names, so
// callers resolve requestability before writing this response.

// WriteUnifiedModelsList presents supplied requestable IDs with catalogue
// metadata and request-local display attributes. Callers resolve eligibility
// before invoking this writer; empty input produces an allocated empty JSON
// data array. A nonnil group display-name override replaces the catalogue
// label, including an explicitly empty label.
//
// An xai catalogue owner or resolved Grok participation selects the Grok
// presentation helper while retaining the public ID. That branch supplies xai
// ownership and zero creation time from its constructed GrokModel, then adds
// resolved effort choices. Ordinary entries keep catalogue ownership and
// creation time. Model and attribute reads therefore enrich the supplied IDs
// without introducing additional list entries.
func (h *ModelsHandler) WriteUnifiedModelsList(c *gin.Context, ids []string) {
	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": []any{}})
		return
	}
	models := make([]gin.H, 0, len(ids))
	for _, id := range ids {
		model := h.catalog.Model(id)
		attributes := h.displayAttributes(c, id)
		if attributes.DisplayName != nil {
			model.DisplayName = *attributes.DisplayName
		}
		item := gin.H{"id": id, "object": "model", "type": "model", "display_name": model.DisplayName, "owned_by": model.OwnedBy, "created": model.Created}
		// Resolved Grok participation can override another catalogue owner.
		// Construct its presentation with the same public ID and resolved label,
		// while the omitted creation timestamp stays zero.
		if h.grokDisplayModel(c, id, model.OwnedBy) {
			item = h.unifiedGrokModel(GrokModel{ID: id, Object: "model", Type: "model", OwnedBy: "xai", DisplayName: model.DisplayName}, attributes)
		}
		models = append(models, item)
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
}

// unifiedGrokModel builds xAI display fields with optional effort-selection
// metadata. Attributes exposing effort values add the default and ordered
// options; the helper returns a fresh object for the current model list
// entry.
func (h *ModelsHandler) unifiedGrokModel(model GrokModel, attributes modelcatalog.Attributes) gin.H {
	item := gin.H{"id": model.ID, "object": "model", "type": "model", "display_name": model.DisplayName, "owned_by": model.OwnedBy, "created": model.Created}
	if values := attributes.Efforts(); len(values) > 0 {
		item["supportsReasoningEffort"] = true
		current, efforts := grokEffortOptions(values)
		item["reasoningEffort"] = current
		item["reasoningEfforts"] = efforts
	}
	return item
}
