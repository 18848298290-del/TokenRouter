package testkit

import (
	"context"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// model_health.go supplies a missing-model provider fixture and records model
// or provider cooldown writes. The provider fixture is an active OpenAI API Key
// provider with a ten-minute 404 rule, allowing tests to distinguish the
// production health decision through its observed writes.

// ModelHealthStore records model cooldowns and supports injected write errors.
type ModelHealthStore struct {
	HealthStoreBase                      // permissive inherited health write fixture
	TempCalls           int              // temporary cooldown write count
	ModelRateLimitCalls []ModelLimitCall // observed model cooldown write inputs
	ModelRateLimitErr   error            // injected model cooldown write error
}

// SetTempUnschedulable counts provider-wide cooldown attempts for model-health
// assertions.
func (r *ModelHealthStore) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.TempCalls++
	return nil
}

// SetModelRateLimit appends provider, scope, reset time, and the first
// optional reason before returning ModelRateLimitErr. Further reasons are
// ignored; injected failure still leaves the attempted model write observable.
func (r *ModelHealthStore) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := ModelLimitCall{
		ProviderID: id,
		Scope:      scope,
		ResetAt:    resetAt,
	}
	if len(reason) > 0 {
		call.Reason = reason[0]
	}
	r.ModelRateLimitCalls = append(r.ModelRateLimitCalls, call)
	return r.ModelRateLimitErr
}

// ModelNotFoundProvider returns an active OpenAI API Key fixture with
// temporary unscheduling enabled. Its rule matches 404 and not found text for
// ten minutes, exposing configured provider cooldown to model-health tests.
func ModelNotFoundProvider() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 101,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusNotFound),
						"keywords":         []any{"not found"},
						"duration_minutes": float64(10),
					},
				},
			},
		},
	}
}
