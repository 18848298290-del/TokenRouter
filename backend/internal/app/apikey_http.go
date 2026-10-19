package app

import (
	"context"
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

// apikey_http.go constructs API key HTTP handlers and enriches their group
// responses with capacity and model data.
// Available key groups include capacity and localized display data assembled
// from routing services. Model lists are resolved when requested by the HTTP
// caller, and attribute lookup errors are logged while the remaining group
// response stays available.

// provideKeyHTTP constructs key handlers with group presentation and capacity
// readers. The presentation callback resolves models when requested and returns
// the group response even when model attribute lookup fails.
func provideKeyHTTP(keys *apikey.APIKeyService, capacity *routing.CapacityService, catalogue *routing.RequestableCatalogue, attributes *routing.ModelAttributeService) *keyhttp.APIKeyHandler[dto.Group] {
	handler := keyhttp.NewAPIKeyHandler(keys, func(group *routing.Group, summary *accessview.GroupCapacitySummary) *dto.Group {
		result := dto.GroupFromRouting(apikey.RoutingGroup(group))
		if result != nil && summary != nil {
			result.Capacity = dto.GroupCapacityFromSummary(summary)
		}
		return result
	})
	handler.SetGroupCapacityService(capacity)
	handler.SetGroupPresentation(func(ctx context.Context, group *routing.Group, summary *accessview.GroupCapacitySummary, includeModels bool) *dto.Group {
		result := dto.GroupFromRouting(apikey.RoutingGroup(group), locale.FromContext(ctx))
		if summary != nil {
			result.Capacity = dto.GroupCapacityFromSummary(summary)
		}
		if !includeModels {
			return result
		}
		if attributes != nil && routing.ModelAttributesFromContext(ctx) == nil {
			ctx = routing.WithModelAttributes(ctx, attributes)
		}
		resolved := catalogue.ResolveRequestableModels(ctx, &group.ID, "")
		if resolved.Err != nil {
			return result
		}
		result.Models = make([]string, 0, len(resolved.Models))
		result.ModelProtocols = make(map[string][]protocol.ProtocolID)
		for _, model := range resolved.Models {
			result.Models = append(result.Models, model.ID)
			result.ModelProtocols[model.ID] = model.Protocols
		}
		var err error
		result.ModelAttributes, err = attributes.ResolveModels(ctx, group.ID, resolved.Models)
		if err != nil {
			// Attribute enrichment can fail after the model list is resolved. Keep
			// the group and models available and log the failed optional lookup.
			slog.Warn("failed to read group model attributes", "group_id", group.ID, "error", err)
		}
		return result
	})
	return handler
}

// provideKeyAdminHTTP binds API key administration and group response conversion.
func provideKeyAdminHTTP(keys *apikey.Admin) *keyhttp.AdminAPIKeyHandler[dto.Group] {
	return keyhttp.NewAdminAPIKeyHandler(keys, func(group *routing.Group) *dto.Group {
		return dto.GroupFromRouting(apikey.RoutingGroup(group))
	})
}
