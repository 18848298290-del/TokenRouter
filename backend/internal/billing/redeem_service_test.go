package billing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// redeemAuthInvalidatorStub records users whose authentication cache is invalidated after an entitlement change.
type redeemAuthInvalidatorStub struct{ userIDs []int64 }

func TestRedeemService_InvalidateRedeemCaches_AuthCache(t *testing.T) {
	invalidator := &redeemAuthInvalidatorStub{}
	svc := &RedeemService{authCacheInvalidator: invalidator}

	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeBalance})
	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeConcurrency})
	planID := int64(3)
	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeSubscription, PlanID: &planID})

	require.Equal(t, []int64{11, 11, 11}, invalidator.userIDs)
}

func (s *redeemAuthInvalidatorStub) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	s.userIDs = append(s.userIDs, id)
}
