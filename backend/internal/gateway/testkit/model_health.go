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

// ModelHealthStore records model cooldowns and supports injected write errors.
type ModelHealthStore struct {
	HealthStoreBase
	TempCalls           int
	ModelRateLimitCalls []ModelLimitCall
	ModelRateLimitErr   error
}

func (r *ModelHealthStore) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.TempCalls++
	return nil
}

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
