package billing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// redeemAuthInvalidatorStub captures requested user authentication
// invalidations from redemption cache effects.
type redeemAuthInvalidatorStub struct {
	userIDs []int64 // requested user invalidation sequence
}

// TestRedeemService_InvalidateRedeemCaches_AuthCache applies balance,
// concurrency and subscription cache effects for one user. It checks all three
// paths request that user's authentication invalidation with the supplied
// narrow dependencies.
func TestRedeemService_InvalidateRedeemCaches_AuthCache(t *testing.T) {
	invalidator := &redeemAuthInvalidatorStub{}
	svc := &RedeemService{authCacheInvalidator: invalidator}

	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeBalance})
	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeConcurrency})
	planID := int64(3)
	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeSubscription, PlanID: &planID})

	require.Equal(t, []int64{11, 11, 11}, invalidator.userIDs)
}

// InvalidateAuthCacheByUserID appends the requested user ID for cache-effect
// assertions. Context is ignored, and the synchronous test observes request
// order through the retained slice; this stub performs no cache operation.
func (s *redeemAuthInvalidatorStub) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	s.userIDs = append(s.userIDs, id)
}
