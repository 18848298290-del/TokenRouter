package routing

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ModelAvailabilityDiagnosis records support in the group's persistently eligible provider pool.
// The reader selects active, schedulable providers and ignores transient blocks,
// allowing handlers to distinguish 404 model_not_found from 503 service_unavailable.
type ModelAvailabilityDiagnosis struct {
	// HasProvidersInPool reports whether the group has a provider matching the optional platform filter.
	HasProvidersInPool bool
	// HasModelSupport reports whether at least one provider supports the resolved model.
	HasModelSupport bool
}

// ModelAvailabilityDiagnoser supplies persistent model-support checks to request handlers.
type ModelAvailabilityDiagnoser interface {
	DiagnoseModelAvailabilityForPlatform(
		ctx context.Context,
		groupID *int64,
		requestedModel string,
		platform string,
	) ModelAvailabilityDiagnosis
}

// AvailabilityProvider pairs a platform with a model-support check.
type AvailabilityProvider struct {
	Platform string
	Supports func(context.Context, string) bool
}
type AvailabilityReader func(context.Context, *int64, []string, bool) ([]AvailabilityProvider, error)

// ModelAvailability distinguishes missing models from temporary unavailability through uncached reads.
type ModelAvailability struct {
	Read     AvailabilityReader
	MapModel func(context.Context, *int64, string) string
}

// ModelAvailabilityDiagnoserFunc adapts a function to ModelAvailabilityDiagnoser.
type ModelAvailabilityDiagnoserFunc func(context.Context, *int64, string, string) ModelAvailabilityDiagnosis

// DiagnoseGeneral maps the request model and checks the persistent provider pool.
// The reader bypasses scheduler snapshots and transient limits, overload, expiry,
// quota, and runtime blocks.
//
// A missing service or reader, an empty model, or a read failure returns {true, true}
// so handlers retain the 503 response. A missing or invalid group returns {false, false}.
func (s *ModelAvailability) DiagnoseGeneral(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil {
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		// An empty model cannot establish model_not_found; leave the handler on its 503 path.
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}
	if groupID == nil || *groupID <= 0 {
		return ModelAvailabilityDiagnosis{}
	}
	if s.Read == nil {
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}
	providers, err := s.Read(ctx, groupID, availabilityPlatforms(platform), false)
	if err != nil {
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{}
	routingModel := requestedModel
	if s.MapModel != nil {
		routingModel = s.MapModel(ctx, groupID, requestedModel)
	}
	for i := range providers {
		if platform != "" && providers[i].Platform != strings.TrimSpace(platform) {
			continue
		}
		diag.HasProvidersInPool = true
		if providers[i].Supports(ctx, routingModel) {
			diag.HasModelSupport = true
			return diag
		}
	}
	return diag
}

// DiagnoseCompatible maps the request model before checking provider support.
// A nonempty platform restricts the pool; empty checks all platforms in the group.
// The reader uses persistent configuration and ignores transient runtime state.
//
// A missing service or reader, an empty model, or a read failure returns {true, true}
// for the 503 path. A missing or invalid group returns {false, false}.
func (s *ModelAvailability) DiagnoseCompatible(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil {
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}
	if groupID == nil || *groupID <= 0 {
		return ModelAvailabilityDiagnosis{}
	}
	routingModel := requestedModel
	if s.MapModel != nil {
		routingModel = s.MapModel(ctx, groupID, requestedModel)
	}
	return s.DiagnoseCompatibleRouting(ctx, groupID, routingModel, platform)
}

// DiagnoseCompatibleRouting checks a model that has already passed group mapping.
// Messages error handling uses it to avoid applying the group mapping twice.
func (s *ModelAvailability) DiagnoseCompatibleRouting(
	ctx context.Context,
	groupID *int64,
	routingModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil {
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}
	routingModel = strings.TrimSpace(routingModel)
	if routingModel == "" {
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}
	if groupID == nil || *groupID <= 0 {
		return ModelAvailabilityDiagnosis{}
	}
	if s.Read == nil {
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}

	providers, err := s.Read(ctx, groupID, availabilityPlatforms(platform), false)
	if err != nil {
		// A query failure cannot establish model absence; retain the 503 response.
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{}
	for i := range providers {
		if platform != "" && providers[i].Platform != strings.TrimSpace(platform) {
			continue
		}
		diag.HasProvidersInPool = true
		// Use the same configured model and protocol eligibility checks as provider selection.
		if providers[i].Supports(ctx, routingModel) {
			diag.HasModelSupport = true
			return diag
		}
	}
	return diag
}

func (f ModelAvailabilityDiagnoserFunc) DiagnoseModelAvailabilityForPlatform(ctx context.Context, group *int64, model, platform string) ModelAvailabilityDiagnosis {
	return f(ctx, group, model, platform)
}

// availabilityPlatforms returns the forced platform or the shared provider-platform catalogue.
func availabilityPlatforms(platform string) []string {
	if platform = strings.TrimSpace(platform); platform != "" {
		return []string{platform}
	}
	return capability.ProviderPlatforms()
}
