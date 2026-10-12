package selection

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// ResolveOpenAIWSRoutingModelForProvider resolves and validates the group-mapped model for each WebSocket turn.
// Later turns stay on the selected provider, so an incompatible model rejects the frame.
func (s *Compatible) ResolveOpenAIWSRoutingModelForProvider(
	ctx context.Context,
	groupID *int64,
	provider *gatewayprovider.ExecutionProvider,
	requestedModel string,
	requiredCapability providercore.OpenAIEndpointCapability,
) (string, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return "", errors.New("websocket request model is empty")
	}
	if s.CheckGroupModelRestriction(ctx, groupID, requestedModel) {
		return "", fmt.Errorf("model %s is restricted by group model policy", requestedModel)
	}

	routingModel := strings.TrimSpace(s.resolveGroupRoutingModel(ctx, groupID, requestedModel))
	if routingModel == "" {
		routingModel = requestedModel
	}
	if provider == nil || !gatewayprovider.
		CompatibleProviderEligible(
			ctx,
			provider,
			provider.Record.Platform,
			routingModel,
			false,
			requiredCapability,
		) {
		return "", fmt.Errorf("model %s is not supported by the selected websocket provider", requestedModel)
	}
	if s.isOpenAIProviderRequestRuntimeBlocked(provider, routingModel) {
		return "", fmt.Errorf("model %s is temporarily unavailable on the selected websocket provider", requestedModel)
	}
	if groupID != nil && s.NeedsUpstreamGroupRestriction(ctx, groupID) &&
		s.UpstreamRoutingModelRestricted(ctx, *groupID, provider, routingModel, false) {
		return "", fmt.Errorf("model %s is restricted after provider mapping", requestedModel)
	}
	return routingModel, nil
}
