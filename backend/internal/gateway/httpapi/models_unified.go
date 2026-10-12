package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// WriteUnifiedModelsList uses catalogue names and owners with group display attributes.
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
		if h.grokDisplayModel(c, id, model.OwnedBy) {
			item = h.unifiedGrokModel(GrokModel{ID: id, Object: "model", Type: "model", OwnedBy: "xai", DisplayName: model.DisplayName}, attributes)
		}
		models = append(models, item)
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
}

// unifiedGrokModel includes the reasoning-effort metadata consumed by clients.
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
