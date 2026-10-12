package billing

import "context"

// Pinned task funding needs the selected subscription belonging to the
// payer and applicable to the final group. Invalid identities and read
// failures produce an unavailable subscription for the batch submission
// caller, which then rejects the pinned choice rather than selecting
// another entitlement.

// PreferredSubscriptionReader resolves one selected entitlement for a payer and group.
type PreferredSubscriptionReader interface {
	// ResolvePreferredSubscriptionForGroup reads the selected user subscription
	// for the supplied payer and group. The integer arguments are user ID,
	// subscription ID, then group ID; unavailable or inapplicable
	// entitlements return an error or no usable value.
	ResolvePreferredSubscriptionForGroup(context.Context, int64, int64, int64) (*UserSubscription, error)
}

// ResolvePreferredSubscription returns the selected entitlement from
// reader or nil when a required ID, group, or reader is absent. Read
// errors also become nil; the batch caller treats that result as an
// unavailable pinned funding choice.
func ResolvePreferredSubscription(ctx context.Context, reader PreferredSubscriptionReader, userID, subscriptionID int64, groupID *int64) *UserSubscription {
	if reader == nil || userID <= 0 || subscriptionID <= 0 || groupID == nil || *groupID <= 0 {
		return nil
	}
	value, err := reader.ResolvePreferredSubscriptionForGroup(ctx, userID, subscriptionID, *groupID)
	// A failed pinned lookup cannot establish usable funding for submission.
	// Return the unavailable result so the batch caller rejects that selected
	// subscription before creating the task.
	if err != nil {
		return nil
	}
	return value
}
