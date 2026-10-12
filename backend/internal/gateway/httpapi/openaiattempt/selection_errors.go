package openaiattempt

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// noProviderErrorClassification aliases the shared HTTP selection error type.
type noProviderErrorClassification = gatewayhttp.SelectionErrorResponse

func classifySelectionFailureError(err error, fallback noProviderErrorClassification) noProviderErrorClassification {
	return gatewayhttp.RefineSelectionError(err, fallback)
}

// classifyNoProviderError uses the final key group for diagnostics and HTTP error mapping.
func classifyNoProviderError(
	ctx context.Context,
	diag routing.ModelAvailabilityDiagnoser,
	apiKey *apikey.APIKey,
	routingModel string,
	displayModel string,
	platform string,
) noProviderErrorClassification {
	var groupID *int64
	if apiKey != nil {
		groupID = apiKey.GroupID
	}
	return gatewayhttp.ClassifySelectionError(ctx, diag, groupID, routingModel, displayModel, platform)
}

// ClassifyNoProviderErrorFromGin reads the request context from Gin for selection diagnostics.
func ClassifyNoProviderErrorFromGin(
	c *gin.Context,
	diag routing.ModelAvailabilityDiagnoser,
	apiKey *apikey.APIKey,
	routingModel string,
	displayModel string,
	platform string,
) noProviderErrorClassification {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	classification := classifyNoProviderError(ctx, diag, apiKey, routingModel, displayModel, platform)
	if classification.ModelNotFound {
		gatewayhttp.MarkOpsClientBusinessLimited(c, gatewayhttp.OpsClientBusinessLimitedReasonLocalModelConfiguration)
	}
	return classification
}

// ClassifyOpenAICompatibleNoProviderErrorFromGin diagnoses group candidates under an optional forced platform.
func ClassifyOpenAICompatibleNoProviderErrorFromGin(
	c *gin.Context,
	diag routing.ModelAvailabilityDiagnoser,
	apiKey *apikey.APIKey,
	routingModel string,
	displayModel string,
) noProviderErrorClassification {
	return ClassifyNoProviderErrorFromGin(
		c,
		diag,
		apiKey,
		routingModel,
		displayModel,
		gatewayhttp.OpenAICompatibleRequestPlatform(apiKey),
	)
}
