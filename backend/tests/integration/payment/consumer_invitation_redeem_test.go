package payment_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Invitation codes establish registration access and cannot issue benefits
// through the ordinary redemption endpoint. The fixture supplies an invitation
// and a typed nil user dependency, then checks the unsupported-type error
// before any recorded code use or owner assignment occurs.

// TestRedeemRejectsInvitationCodeBeforeGrantingBenefits supplies an unused
// invitation with a typed nil user fixture. Redemption returns the specified
// BadRequest reason and message while retaining unused code state and an empty
// use-call record.
func TestRedeemRejectsInvitationCodeBeforeGrantingBenefits(t *testing.T) {
	ctx := context.Background()
	// A typed nil user fixture would panic if its GetByID override were
	// reached, so the observed unsupported-type result checks rejection
	// before that dependent lookup.
	redeemRepo := &paymentOrderLifecycleRedeemRepo{
		codesByCode: map[string]*billing.RedeemCode{
			"INVITE-001": {
				ID:        1,
				Code:      "INVITE-001",
				Type:      billing.RedeemTypeInvitation,
				Status:    billing.StatusUnused,
				MaxUses:   1,
				UsedCount: 0,
			},
		},
	}
	redeemService := newFulfillmentRedeemService(redeemRepo, nil, newPaymentOrderLifecycleTestClient(t))

	got, err := redeemService.Redeem(ctx, 2, "INVITE-001")

	require.Nil(t, got)
	require.Error(t, err)
	require.True(t, apperror.IsBadRequest(err))
	require.Equal(t, "REDEEM_CODE_UNSUPPORTED_TYPE", apperror.Reason(err))
	require.Equal(t, "invitation codes can only be used during registration", apperror.Message(err))
	require.Empty(t, redeemRepo.useCalls)
	require.Equal(t, billing.StatusUnused, redeemRepo.codesByCode["INVITE-001"].Status)
	require.Nil(t, redeemRepo.codesByCode["INVITE-001"].UsedBy)
}
