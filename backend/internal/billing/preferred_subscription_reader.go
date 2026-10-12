package billing

import "context"

// PreferredSubscriptionReader finds a user's subscription applicable to a group.
type PreferredSubscriptionReader interface {
	ResolvePreferredSubscriptionForGroup(context.Context, int64, int64, int64) (*UserSubscription, error)
}

// ResolvePreferredSubscription returns nil for invalid input, read errors, or a missing subscription.
func ResolvePreferredSubscription(ctx context.Context, reader PreferredSubscriptionReader, userID, subscriptionID int64, groupID *int64) *UserSubscription {
	if reader == nil || userID <= 0 || subscriptionID <= 0 || groupID == nil || *groupID <= 0 {
		return nil
	}
	value, err := reader.ResolvePreferredSubscriptionForGroup(ctx, userID, subscriptionID, *groupID)
	if err != nil {
		return nil
	}
	return value
}
