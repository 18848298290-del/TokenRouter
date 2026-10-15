package routing

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// model_availability.go diagnoses provider-pool presence and configured model
// support after selection failures.
// A failed provider selection can mean either an unsupported model or
// temporarily exhausted capacity. Availability diagnosis reads persistent group
// candidates and delegates model support to the supplied provider checks,
// letting handlers classify missing configuration separately from transient
// execution state.
//
// The production reader filters on saved active and schedulable flags while
// leaving transient windows unchecked for this diagnostic query. Unknown
// evidence, including a missing reader or query failure, returns both flags
// true so handlers keep the unavailable response. An empty or
// platform-filtered-empty pool returns both flags false; a populated
// unsupported pool retains provider presence with model support false.
// Requested-model helpers map the group once; the routing-model helper accepts
// that prior result to keep one-hop mapping intact.

// ModelAvailabilityDiagnosis records persistent provider membership and locally
// configured model support for error classification.
type ModelAvailabilityDiagnosis struct {
	// HasProvidersInPool and HasModelSupport distinguish an empty persistent pool
	// from an unsupported model in a populated pool. Unknown evidence keeps both
	// true; an empty pool clears both, and a populated unsupported pool clears
	// HasModelSupport alone.
	HasProvidersInPool bool // matching persistent provider presence
	HasModelSupport    bool // configured model support evidence
}

// ModelAvailabilityDiagnoser supplies group model support evidence for
// selection-failure classification.
type ModelAvailabilityDiagnoser interface {
	// DiagnoseModelAvailabilityForPlatform evaluates the supplied group, model,
	// and optional forced platform for error classification. Implementations
	// represent unknown evidence conservatively so temporary selection failure
	// does not become a false missing-model response.
	DiagnoseModelAvailabilityForPlatform(
		ctx context.Context,
		groupID *int64,
		requestedModel string,
		platform string,
	) ModelAvailabilityDiagnosis
}

// AvailabilityProvider pairs one provider platform with its configured model
// support callback.
type AvailabilityProvider struct {
	Platform string // configured provider platform name
	// Supports checks a routing model against this candidate's supplied rules.
	// Readers supply a nonnil callback for each returned provider; the production
	// adapter created by NewModelAvailability also includes protocol eligibility.
	Supports func(context.Context, string) bool
}

// AvailabilityReader supplies persistent candidate records for a group and
// platform query.
type AvailabilityReader func(context.Context, *int64, []string, bool) ([]AvailabilityProvider, error)

// ModelAvailability diagnoses group model configuration through its injected
// persistent reader and mapping helper.
type ModelAvailability struct {
	// Read supplies the candidate population and defines its persistent-state
	// filtering. Production wiring ignores transient execution blocks so diagnosis
	// can separate temporary capacity loss from model configuration.
	Read AvailabilityReader
	// MapModel applies the configured group rewrite to requested model input.
	// DiagnoseCompatibleRouting accepts an already mapped model and leaves this
	// callback unused.
	MapModel func(context.Context, *int64, string) string
}

// ModelAvailabilityDiagnoserFunc adapts a callback to the shared model
// diagnosis interface.
type ModelAvailabilityDiagnoserFunc func(context.Context, *int64, string, string) ModelAvailabilityDiagnosis

// DiagnoseGeneral reads persistent group candidates and checks the requested
// model after one optional group mapping. Nil service, blank model, missing
// reader, or query failure returns both flags true to keep error classification
// on the unavailable path.
//
// A nil or nonpositive group returns both flags false before reading providers.
// Read occurs before mapping, and the optional platform filters each returned
// candidate as well as the reader query. The reader defines persistent
// eligibility and each Supports callback defines model rules; production wiring
// excludes transient state from those inputs.
//
// Callers use the empty string for an unrestricted platform. A nonempty
// whitespace-only value compares against an empty platform after trimming, so
// real platform records are excluded.
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
		// Blank input supplies no model-absence evidence, so return conservative
		// support flags and let the handler retain its service-unavailable
		// classification.
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
		// Candidate reads can fail independently of model support. Treat that missing
		// evidence as unknown so a storage outage is not reported as model absence.
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

// DiagnoseCompatible applies the optional group mapping once and delegates
// diagnosis of that routing model. Nil service or blank requested model keeps
// both flags true; an invalid group returns both false before mapping.
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

// DiagnoseCompatibleRouting checks a model that has already passed group
// mapping, allowing Messages error handling to retain one-hop model identity.
// Blank models, missing service or reader, and read failures retain both flags
// true for unavailable classification.
//
// Invalid group identifiers return both flags false. Otherwise the persistent
// reader is queried within the group, then platform filtering and each
// provider's Supports callback distinguish an empty pool from configured model
// absence. A supporting provider ends the search immediately; transient runtime
// behavior belongs to the injected reader and support adapter.
//
// Callers use the empty string for an unrestricted platform. A nonempty
// whitespace-only value compares against an empty platform after trimming, so
// real platform records are excluded.
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
		// A failed query supplies no proof of configured model absence. Return
		// conservative support flags so the entry keeps its service-unavailable
		// response.
		return ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{}
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

// DiagnoseModelAvailabilityForPlatform passes diagnosis inputs directly to the
// wrapped callback and returns its evidence. The adapter requires a nonnil
// function; group and platform interpretation remain with that function.
func (f ModelAvailabilityDiagnoserFunc) DiagnoseModelAvailabilityForPlatform(ctx context.Context, group *int64, model, platform string) ModelAvailabilityDiagnosis {
	return f(ctx, group, model, platform)
}

// availabilityPlatforms returns a trimmed forced-platform singleton or a fresh
// list of every provider platform. Blank and whitespace-only platform filters
// therefore let the persistent reader inspect cross-platform group candidates.
func availabilityPlatforms(platform string) []string {
	if platform = strings.TrimSpace(platform); platform != "" {
		return []string{platform}
	}
	return capability.ProviderPlatforms()
}
