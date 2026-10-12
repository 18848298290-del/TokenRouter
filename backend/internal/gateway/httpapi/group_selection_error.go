package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeldisplay"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// WriteGroupSelectionBusinessError writes selection errors and filters displayed models through key configuration.
func WriteGroupSelectionBusinessError(c *gin.Context, err error, streamStarted bool, readAccess func(*gin.Context) (*apikey.APIKey, bool), catalogue modeldisplay.Catalog, writeError func(int, string, string, bool)) bool {
	if errors.Is(err, routing.ErrClaudeCodeOnly) {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		writeError(http.StatusForbidden, "permission_error", routing.ErrClaudeCodeOnly.Error(), streamStarted)
		return true
	}

	if modelErr, ok := errors.AsType[*routing.GroupModelUnsupportedError](err); ok {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		message := modelErr.Error()
		if apiKey, ok := readAccess(c); ok && apiKey != nil && apiKey.Group != nil && apiKey.Group.CustomModelsListEnabled() {
			availableModels := FilterModelsByCustomList(modelErr.AvailableModels, nil, apiKey.Group.ModelsListConfig.Models)
			message = (&routing.GroupModelUnsupportedError{
				RequestedModel:  modelErr.RequestedModel,
				AvailableModels: availableModels,
			}).Error()
		}
		writeError(http.StatusForbidden, "permission_error", message, streamStarted)
		return true
	}
	return false
}
