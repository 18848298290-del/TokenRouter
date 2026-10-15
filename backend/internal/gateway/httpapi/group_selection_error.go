package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeldisplay"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// group_selection_error.go presents group selection business errors and
// filters model suggestions through the effective group configuration.
// Group selection can fail because of a client restriction or a model
// restriction. These failures become local feature-gate observations and
// permission errors, with the effective group custom list limiting the model
// alternatives shown to the client.
//
// The presenter recognizes the client-only restriction before looking for a
// typed unsupported-model error. For the latter, it keeps the requested model
// and rebuilds the available-model message when the effective group enables a
// custom list. The shared filter intersects that configured list with the
// error's available models, so the denial does not suggest targets hidden by
// the group. Unrecognized errors return to the caller for other failure
// handling.

// WriteGroupSelectionBusinessError presents Claude-Code-only and unsupported
// group model errors as business denials. It returns true after invoking
// writeError and false for other errors. A custom model list filters displayed
// alternatives using the effective key; the catalogue argument is currently
// unused.
func WriteGroupSelectionBusinessError(c *gin.Context, err error, streamStarted bool, readAccess func(*gin.Context) (*apikey.APIKey, bool), catalogue modeldisplay.Catalog, writeError func(int, string, string, bool)) bool {
	if errors.Is(err, routing.ErrClaudeCodeOnly) {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		writeError(http.StatusForbidden, "permission_error", routing.ErrClaudeCodeOnly.Error(), streamStarted)
		return true
	}

	if modelErr, ok := errors.AsType[*routing.GroupModelUnsupportedError](err); ok {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		message := modelErr.Error()
		// Apply the effective group display restriction before rebuilding the error,
		// so its alternatives agree with the group catalogue visible to this key.
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
