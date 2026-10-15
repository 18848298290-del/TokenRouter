package textattempt

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// selection_errors.go presents text selection failures using authenticated
// group data and client-visible model names.
// Selection error diagnosis needs the authenticated group and client-visible
// model while compatible attempts may have different execution targets. HTTP
// adapters mark model and feature denials locally, retain request context when
// present, and choose Google or generic business presentation before the
// attempt loop continues.

// classifyNoProviderErrorFromGin diagnoses availability using the current
// request context and supplied Key group. Missing requests use Background and
// absent keys omit group identity; model-not-found diagnosis records local
// model-configuration limitation before returning its client response.
func classifyNoProviderErrorFromGin(c *gin.Context, diag routing.ModelAvailabilityDiagnoser, key *apikey.APIKey, model, display, platform string) gatewayhttp.SelectionErrorResponse {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	var id *int64
	if key != nil {
		id = key.GroupID
	}
	result := gatewayhttp.ClassifySelectionError(ctx, diag, id, model, display, platform)
	if result.ModelNotFound {
		gatewayhttp.MarkOpsClientBusinessLimited(c, gatewayhttp.OpsClientBusinessLimitedReasonLocalModelConfiguration)
	}
	return result
}

// handleGroupSelectionBusinessError delegates recognized group business denials
// to shared HTTP presentation. It forwards the supplied stream state and error
// callback while key display filtering reads the authentication context at
// handling time.
func handleGroupSelectionBusinessError(c *gin.Context, err error, started bool, write func(int, string, string, bool)) bool {
	return gatewayhttp.WriteGroupSelectionBusinessError(c, err, started, keyhttp.GetAPIKeyFromContext, gatewayadapter.ModelDisplayCatalogue{}, write)
}

// handleGeminiGroupModelUnsupportedError handles a wrapped group-model
// restriction as Google permission denial. It marks a local feature-gate
// observation and returns true after output; other causes return false for
// subsequent selection diagnosis.
func handleGeminiGroupModelUnsupportedError(c *gin.Context, err error) bool {
	var model *routing.GroupModelUnsupportedError
	if !errors.As(err, &model) {
		return false
	}
	gatewayhttp.MarkOpsClientBusinessLimited(c, gatewayhttp.OpsClientBusinessLimitedReasonLocalFeatureGate)
	gatewayhttp.WriteGoogleError(c, http.StatusForbidden, model.Error())
	return true
}

// derefGroupID returns the pointed-to group identifier or zero for absence.
// Callers use zero as the missing-group value in session operations; positive
// group validation remains with request admission.
func derefGroupID(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
