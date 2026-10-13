package openaiattempt

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Provider selection errors distinguish absent model support from temporary
// pool capacity using the effective group's diagnosis. HTTP adapters retain the
// routing model for that lookup and the display model for client messages, then
// mark model-configuration limitations separately from routing-capacity
// observations.

// noProviderErrorClassification aliases the HTTP selection response and its
// model-support attribution.
type noProviderErrorClassification = gatewayhttp.SelectionErrorResponse

// classifySelectionFailureError refines fallback output using scheduler error
// evidence. It delegates model-not-found precedence and rate-limit recognition
// to the shared HTTP classifier, returning fallback when the error supplies no
// stronger classification.
func classifySelectionFailureError(err error, fallback noProviderErrorClassification) noProviderErrorClassification {
	return gatewayhttp.RefineSelectionError(err, fallback)
}

// classifyNoProviderError diagnoses selection against the effective key's
// optional group. The routing model controls lookup and displayModel controls
// client text. Missing key data passes a nil group to the shared classifier's
// availability defaults.
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

// ClassifyNoProviderErrorFromGin diagnoses request selection and publishes
// local model attribution. It uses the inbound context when available,
// otherwise background context. Model-not-found results mark the HTTP request
// as locally limited; the returned classification still remains usable when c
// is nil.
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

// ClassifyOpenAICompatibleNoProviderErrorFromGin diagnoses compatible model
// availability for the effective key. Its platform argument comes from the
// shared compatible-request resolver, allowing mixed-group lookup when that
// resolver supplies an empty filter.
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
