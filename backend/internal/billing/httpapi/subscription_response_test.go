package httpapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// TestUserSubscriptionFromService_MapsRevokedAt supplies a revoked grant with
// a soft-deletion timestamp. It checks the response exposes that same
// timestamp as RevokedAt for user consumption.
func TestUserSubscriptionFromService_MapsRevokedAt(t *testing.T) {
	ts := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)

	sub := UserSubscriptionFromService(&billing.UserSubscription{
		ID:        1,
		UserID:    2,
		PlanID:    3,
		Status:    billing.SubscriptionStatusRevoked,
		DeletedAt: &ts,
	})

	require.NotNil(t, sub.RevokedAt)
	require.Equal(t, ts, *sub.RevokedAt)
}
