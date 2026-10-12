package admission

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// A key's billing mode chooses whether consumption uses balance, an available
// subscription, or a specific subscription. Auto mode can use balance when
// no usable subscription exists; an explicitly selected subscription retains
// subscription funding even when it is unavailable. Availability queries can
// therefore show the selected source before consumption admission decides
// whether that source can fund a request.
//
// Group coverage depends on the final selected group. Ordinary keys have one
// such group, while composite model listings can run before a mapping is chosen.
// Those listings defer plan-group filtering to their handler; consuming requests
// provide the chosen GroupID and check coverage here. Enforced resolution rejects
// missing preferred subscriptions and incompatible groups. Subscription status
// and window quota remain subject to the later consumption check.

// Funding-source values tell billing and admission which payer resource was
// selected independently of whether that resource is currently available.
const (
	FundingSourceSubscription = "subscription" // selected subscription funding
	FundingSourceBalance      = "balance"      // selected balance funding
)

// FundingInput contains the payer, key billing mode, and selected group.
type FundingInput struct {
	Mode string // effective key billing mode
	// HasPayer distinguishes missing authenticated payer data from an available
	// payer. ResolveFunding rejects a missing payer before interpreting the mode.
	HasPayer bool
	// PayerID and GroupID identify the subscription owner and final request group.
	// Composite listings may leave GroupID nonpositive until a mapping is selected.
	PayerID, GroupID int64 // authenticated payer ID; selected request-group ID
	// PreferredSubscriptionID names the subscription selected by subscription mode.
	// A nil or nonpositive value is unavailable, or an error under enforced resolution.
	PreferredSubscriptionID *int64
	// IsComposite allows model listings without a final group to defer restricted
	// plan coverage checks. Consuming composite requests supply a positive GroupID.
	IsComposite bool
}

// SubscriptionReader loads subscriptions and checks their usage limits.
type SubscriptionReader interface {
	// GetUsableSubscription selects an effective subscription with remaining quota,
	// optionally restricted to a positive group ID. A true maintenance result
	// requires persistence before consumption, and missing usable subscriptions return
	// billing.ErrSubscriptionNotFound.
	GetUsableSubscription(context.Context, int64, ...int64) (*billing.UserSubscription, bool, error)
	// GetSubscriptionForAPIKey reads a selected subscription for its paying user.
	// Missing subscriptions and ownership mismatches return billing.ErrSubscriptionNotFound,
	// allowing the caller to reject a preferred subscription without revealing another owner.
	GetSubscriptionForAPIKey(context.Context, int64, int64) (*billing.UserSubscription, error)
	// ValidateAndCheckLimits checks status and quota and reports needed maintenance.
	// The validator may clear stale counters in its input; consumption admission
	// persists requested maintenance and validates the stored result again.
	ValidateAndCheckLimits(*billing.UserSubscription) (bool, error)
}

// ResolveFunding selects the key's balance or subscription funding source for
// its authenticated payer and final group. Auto mode prefers a usable
// subscription and uses balance when the reader or a usable subscription is
// absent; other lookup errors are returned.
//
// Subscription mode keeps the selected source even when it is unavailable.
// With enforce enabled, a missing or unreadable preferred subscription or an
// incompatible group is an error; otherwise the context reports Available=false.
// Ineffective subscriptions remain unavailable even under enforcement. An
// effective subscription's quota validation controls Available, with maintenance
// and final consumption checks left to the admission caller.
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
			// Auto funding can use the payer's balance when no usable subscription
			// exists. Other lookup failures remain errors because eligibility is unknown.
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
			// Nonconsuming queries keep the chosen subscription source visible even
			// when lookup fails; enforced resolution rejects that unavailable preferred source.
			if enforce {
				return nil, billing.ErrPreferredSubscriptionInvalid
			}
			return result, nil
		}
		result.Subscription = subscription
		// Restricted plans require the final group for ordinary keys. Composite
		// model listings have no single group, so the handler filters each mapping.
		// Consumption requests supply GroupID after composite selection.
		requiresGroupCoverage := subscription.Plan != nil && len(subscription.Plan.GroupIDs) > 0 && (!input.IsComposite || groupID > 0)
		if requiresGroupCoverage && !billing.SubscriptionAllowsGroup(subscription, groupID) {
			if enforce {
				return nil, billing.ErrPreferredSubscriptionGroup
			}
			return result, nil
		}
		if subscription.IsEffective() {
			// Availability queries use the validation result for display. Consumption
			// admission subsequently performs required maintenance and rechecks the stored record.
			_, validateErr := subscriptionService.ValidateAndCheckLimits(subscription)
			result.Available = validateErr == nil
		}
		return result, nil
	default:
		return nil, apikey.ErrInvalidAPIKeyBillingMode
	}
}

// ResolveFundingFromKey resolves funding from the key's effective billing mode,
// payer, selected group, and preferred subscription. Consuming requests pass the
// authorized view after composite selection; model listings may lack a final
// group. Enforce rejects missing or unreadable preferred subscriptions and
// incompatible groups; consumption admission checks subscription status and quota.
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
