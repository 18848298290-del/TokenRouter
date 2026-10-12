package admission

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

const (
	FundingSourceSubscription = "subscription"
	FundingSourceBalance      = "balance"
)

// FundingInput contains the payer, key billing mode, and selected group.
type FundingInput struct {
	Mode                    string
	HasPayer                bool
	PayerID, GroupID        int64
	PreferredSubscriptionID *int64
	IsComposite             bool
}

// SubscriptionReader loads subscriptions and checks their usage limits.
type SubscriptionReader interface {
	GetUsableSubscription(context.Context, int64, ...int64) (*billing.UserSubscription, bool, error)
	GetSubscriptionForAPIKey(context.Context, int64, int64) (*billing.UserSubscription, error)
	ValidateAndCheckLimits(*billing.UserSubscription) (bool, error)
}

// ResolveFunding resolves the API key funding source.
// Auto mode prefers an available subscription; subscription and balance modes use the specified source.
func ResolveFunding(ctx context.Context, input FundingInput, subscriptionService SubscriptionReader, enforce bool) (*billing.APIKeyBillingContext, error) {
	mode := input.Mode
	result := &billing.APIKeyBillingContext{Mode: mode, Source: FundingSourceBalance, Available: true}
	if !input.HasPayer {
		return result, billing.ErrPreferredSubscriptionInvalid
	}

	groupID := input.GroupID

	switch mode {
	case billing.APIKeyBillingModeBalance:
		return result, nil
	case billing.APIKeyBillingModeAuto:
		if subscriptionService == nil {
			return result, nil
		}
		subscription, _, err := subscriptionService.GetUsableSubscription(ctx, input.PayerID, groupID)
		if err != nil {
			if errors.Is(err, billing.ErrSubscriptionNotFound) {
				return result, nil
			}
			return nil, err
		}
		if subscription != nil {
			result.Source = FundingSourceSubscription
			result.Subscription = subscription
		}
		return result, nil
	case billing.APIKeyBillingModeSubscription:
		result.Source = FundingSourceSubscription
		result.Available = false
		if input.PreferredSubscriptionID == nil || *input.PreferredSubscriptionID <= 0 || subscriptionService == nil {
			if enforce {
				return nil, billing.ErrPreferredSubscriptionInvalid
			}
			return result, nil
		}
		subscription, err := subscriptionService.GetSubscriptionForAPIKey(ctx, input.PayerID, *input.PreferredSubscriptionID)
		if err != nil {
			if enforce {
				return nil, billing.ErrPreferredSubscriptionInvalid
			}
			return result, nil
		}
		result.Subscription = subscription
		// Restricted plans require the final group for ordinary keys. Composite model listings have no single group,
		// so the handler filters each mapping. Consumption requests supply GroupID after composite selection.
		requiresGroupCoverage := subscription.Plan != nil && len(subscription.Plan.GroupIDs) > 0 && (!input.IsComposite || groupID > 0)
		if requiresGroupCoverage && !billing.SubscriptionAllowsGroup(subscription, groupID) {
			if enforce {
				return nil, billing.ErrPreferredSubscriptionGroup
			}
			return result, nil
		}
		if subscription.IsEffective() {
			_, validateErr := subscriptionService.ValidateAndCheckLimits(subscription)
			result.Available = validateErr == nil
		}
		return result, nil
	default:
		return nil, apikey.ErrInvalidAPIKeyBillingMode
	}
}

// ResolveFundingFromKey extracts payer and subscription information from the authenticated key.
func ResolveFundingFromKey(ctx context.Context, key *apikey.APIKey, reader SubscriptionReader, enforce bool) (*billing.APIKeyBillingContext, error) {
	input := FundingInput{Mode: apikey.APIKeyEffectiveBillingMode(key)}
	if key != nil {
		input.IsComposite = key.IsComposite
		input.PreferredSubscriptionID = key.PreferredSubscriptionID
		if key.GroupID != nil {
			input.GroupID = *key.GroupID
		}
		if key.User != nil {
			input.HasPayer = true
			input.PayerID = key.User.ID
		}
	}
	return ResolveFunding(ctx, input, reader, enforce)
}
