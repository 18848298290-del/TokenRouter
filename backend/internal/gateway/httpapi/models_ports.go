package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// models_ports.go binds authenticated key, billing, and model eligibility
// readers to model-list HTTP handlers.
// Model HTTP requests use authenticated Key data and a configured candidate
// resolver before producing catalogue metadata. Effective request Keys carry
// any current group changes, while middleware Keys are copied for callers
// that need a separate view; model-resource selection remains an
// independently bound operation.

// ModelsPorts binds model eligibility, request identity and selected-resource
// read operations.
type ModelsPorts struct {
	// Catalogue is required by Resolve and ResolveSelected. Available checks this
	// binding's presence; callers needing an empty result check it before resolving.
	Catalogue interface {
		// ResolveRequestableModels validates finite configured candidates for one
		// group and optional platform. An empty result represents current
		// eligibility or a read failure; Restricted can distinguish configured or
		// failed resolution from absence.
		ResolveRequestableModels(context.Context, *int64, string) routing.RequestableModelsResult
		// ResolveSelectedModels validates the supplied concrete names using current
		// group and provider rules. Single-model reads include the exact alias
		// target when configured, while list enumeration uses
		// ResolveRequestableModels instead.
		ResolveSelectedModels(context.Context, *int64, string, []string) routing.RequestableModelsResult
	}
	ReadAccess   func(*gin.Context) (*apikey.APIKey, bool) // authenticated middleware Key reader
	ReadPlatform func(*gin.Context) (string, bool)         // forced route platform reader
	// selected billing context reader
	ReadBilling func(*gin.Context) (*billing.APIKeyBillingContext, bool)
	// group model reader selection
	SelectModel      func(context.Context, *int64) (GeminiModelReader, error)
	CheckAntigravity func(context.Context, *int64) (bool, error) // group Antigravity presence checker
	SafeSegment      func(string) bool                           // model URL segment validator
}

// Access prefers the typed effective request Key and otherwise copies the
// authenticated middleware Key. A successfully typed effective pointer is
// returned as stored, including typed nil; callers inspect pointer presence
// separately from the flag.
func (p ModelsPorts) Access(c *gin.Context) (*apikey.APIKey, bool) {
	if key, ok := EffectiveAPIKey(c); ok {
		return key, true
	}
	key, ok := p.ReadAccess(c)
	return apikey.CopyAPIKey(key), ok
}

// ForcedPlatform returns the middleware platform restriction and its presence
// flag. Ordinary list callers use a nonblank present platform to constrain
// candidate resolution and otherwise permit the catalogue's cross-platform
// scope.
func (p ModelsPorts) ForcedPlatform(c *gin.Context) (string, bool) {
	return p.ReadPlatform(c)
}

// Available reports whether the model candidate resolver interface is bound.
func (p ModelsPorts) Available() bool { return p.Catalogue != nil }

// Resolve delegates configured candidate validation for a group and optional
// platform restriction. Callers provide a bound Catalogue; its result carries
// empty and restricted outcomes according to current provider and group
// eligibility.
func (p ModelsPorts) Resolve(ctx context.Context, id *int64, platform string) routing.RequestableModelsResult {
	return p.Catalogue.ResolveRequestableModels(ctx, id, platform)
}

// ResolveSelected validates the supplied names against one group's current
// model rules. A bound Catalogue is required; single-model callers supply the
// requested name and exact alias target instead of enumerating the complete
// catalogue.
func (p ModelsPorts) ResolveSelected(ctx context.Context, id *int64, platform string, models []string) routing.RequestableModelsResult {
	return p.Catalogue.ResolveSelectedModels(ctx, id, platform, models)
}

// PreferredSubscription reads the selected subscription for subscription-only
// catalogue filtering. Missing billing data, another mode, or a nil
// subscription returns false; a present subscription is returned as shared
// billing-context storage.
func (p ModelsPorts) PreferredSubscription(c *gin.Context) (*billing.UserSubscription, bool) {
	value, ok := p.ReadBilling(c)
	if !ok || value == nil || value.Mode != apikey.APIKeyBillingModeSubscription || value.Subscription == nil {
		return nil, false
	}
	return value.Subscription, true
}

// SelectGemini delegates group-scoped selection of a model resource reader.
func (p ModelsPorts) SelectGemini(ctx context.Context, id *int64) (GeminiModelReader, error) {
	return p.SelectModel(ctx, id)
}

// HasAntigravity delegates checking whether a group has eligible Antigravity
// provider candidates.
func (p ModelsPorts) HasAntigravity(ctx context.Context, id *int64) (bool, error) {
	return p.CheckAntigravity(ctx, id)
}

// CapacityLimited marks Ops routing capacity for unavailable scheduler
// candidates. Other errors leave this classification unchanged, keeping local
// model denial distinct from a failure to obtain any usable execution
// provider.
func (p ModelsPorts) CapacityLimited(c *gin.Context, err error) {
	MarkOpsRoutingCapacityLimitedIfNoAvailable(c, err)
}

// SafeModelSegment delegates validating a name used in a Gemini model URL.
func (p ModelsPorts) SafeModelSegment(model string) bool { return p.SafeSegment(model) }
