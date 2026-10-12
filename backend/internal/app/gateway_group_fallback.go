package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// runtimeGroupFallbackResolver applies authorization, funding, and session checks in the same order for text
// handlers.
type runtimeGroupFallbackResolver func(context.Context, *apikey.APIKey, int64, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error)

// provideFundingAdmission shares eligibility and RPM stores; successful funding checks precede RPM accounting.
func provideFundingAdmission(funds *billing.Eligibility, rpm scheduler.UserRPMCache, rates billing.UserGroupRateRepository) *admission.FundingAdmission {
	limiter := scheduler.NewRPMAdmission(rpm, rates, scheduler.Diagnostics{Logf: logging.LegacyPrintf})
	return admission.NewFundingAdmission(funds, limiter)
}

func provideRuntimeGroupFallbackResolver(keys *apikey.APIKeyService, funding *admission.FundingAdmission, subscriptions *billing.SubscriptionService, cache session.GatewayCache) runtimeGroupFallbackResolver {
	return provideGroupFallbackResolver(keys, funding, subscriptions, cache, false)
}

func provideGroupFallbackResolver(keys *apikey.APIKeyService, funding *admission.FundingAdmission, subscriptions *billing.SubscriptionService, cache session.GatewayCache, fundingOnly bool) runtimeGroupFallbackResolver {
	if keys == nil || funding == nil {
		return nil
	}
	var subscriptionReader admission.SubscriptionReader
	if subscriptions != nil {
		subscriptionReader = subscriptions
	}
	isolate := messageSessionIsolation(cache)
	return func(ctx context.Context, key *apikey.APIKey, id int64, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
		resolved, err := keys.ResolveRuntimeGroup(ctx, key, id)
		if err != nil {
			return nil, nil, err
		}
		if resolved == nil || resolved.Group == nil || !resolved.Group.AllowsClientProtocol(source) {
			return nil, nil, apperror.Forbidden("PROTOCOL_NOT_ALLOWED", "fallback group does not allow this protocol")
		}
		funds, err := admission.ResolveFundingFromKey(ctx, resolved, subscriptionReader, true)
		if err != nil {
			return nil, nil, err
		}
		if err := funding.CheckKey(ctx, resolved, funds.Subscription, "", fundingOnly); err != nil {
			return nil, nil, err
		}
		hints := requeststate.ExecutionHintsFromContext(ctx)
		sessionUserID := key.UserID
		if key.User != nil {
			sessionUserID = key.User.ID
		}
		if err := isolate(ctx, resolved, sessionUserID, hints.SessionIsolationSource, hints.SessionIsolationHash); err != nil {
			return nil, nil, err
		}
		return resolved, funds.Subscription, nil
	}
}

// provideClientGroupFallbackResolver rechecks funding without counting RPM. The final handler acquires
// concurrency and counts RPM once.
func provideClientGroupFallbackResolver(keys *apikey.APIKeyService, funding *admission.FundingAdmission, subscriptions *billing.SubscriptionService, cache session.GatewayCache) func(context.Context, *apikey.APIKey, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	resolve := provideGroupFallbackResolver(keys, funding, subscriptions, cache, true)
	if resolve == nil {
		return nil
	}
	return func(ctx context.Context, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
		current := key
		var subscription *billing.UserSubscription
		visited := map[int64]struct{}{}
		for current != nil && current.Group != nil {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if _, exists := visited[current.Group.ID]; exists {
				return nil, nil, apperror.Forbidden("GROUP_FALLBACK_CYCLE", "fallback group cycle detected")
			}
			visited[current.Group.ID] = struct{}{}
			if !current.Group.ClaudeCodeOnly || (source == protocol.ProtocolAnthropicMessages && requeststate.IsClaudeCodeClient(ctx)) {
				return current, subscription, nil
			}
			target := current.Group.FallbackGroupID
			if target == nil || *target <= 0 {
				return nil, nil, routing.ErrClaudeCodeOnly
			}
			if _, exists := visited[*target]; exists {
				return nil, nil, apperror.Forbidden("GROUP_FALLBACK_CYCLE", "fallback group cycle detected")
			}
			var err error
			current, subscription, err = resolve(ctx, current, *target, source)
			if err != nil {
				return nil, nil, err
			}
		}
		return nil, nil, apperror.Forbidden("GROUP_REQUIRED", "API Key must be assigned to a group")
	}
}
