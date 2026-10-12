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

// classifyNoProviderErrorFromGin uses the authenticated key and request context to diagnose a selection error.
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

func handleGroupSelectionBusinessError(c *gin.Context, err error, started bool, write func(int, string, string, bool)) bool {
	return gatewayhttp.WriteGroupSelectionBusinessError(c, err, started, keyhttp.GetAPIKeyFromContext, gatewayadapter.ModelDisplayCatalogue{}, write)
}

func handleGeminiGroupModelUnsupportedError(c *gin.Context, err error) bool {
	var model *routing.GroupModelUnsupportedError
	if !errors.As(err, &model) {
		return false
	}
	gatewayhttp.MarkOpsClientBusinessLimited(c, gatewayhttp.OpsClientBusinessLimitedReasonLocalFeatureGate)
	gatewayhttp.WriteGoogleError(c, http.StatusForbidden, model.Error())
	return true
}

func derefGroupID(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
