package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

func RedeemCodeFromService(rc *billing.RedeemCode) *RedeemCode {
	if rc == nil {
		return nil
	}
	out := RedeemCodeFromServiceBase(rc)
	return &out
}

// RedeemCodeFromServiceAdmin converts a service RedeemCode to DTO for admin users.
// It exposes all internal notes; user-facing endpoints use RedeemCodeFromService.
func RedeemCodeFromServiceAdmin(rc *billing.RedeemCode) *AdminRedeemCode {
	if rc == nil {
		return nil
	}
	return &AdminRedeemCode{
		RedeemCode: RedeemCodeFromServiceBase(rc),
		Notes:      rc.Notes,
	}
}

func RedeemCodeFromServiceBase(rc *billing.RedeemCode) RedeemCode {
	out := RedeemCode{
		RequiresPayment: rc.RequiresPayment,
		ID:              rc.ID,
		Code:            rc.Code,
		Type:            rc.Type,
		Value:           rc.Value,
		Status:          rc.Status,
		MaxUses:         rc.MaxUses,
		UsedCount:       rc.UsedCount,
		ExpiresAt:       rc.ExpiresAt,
		UsedBy:          rc.UsedBy,
		UsedAt:          rc.UsedAt,
		CreatedAt:       rc.CreatedAt,
		PlanID:          rc.PlanID,
		User:            UserSummaryFromBilling(rc.User),
	}

	// For admin_balance/admin_concurrency types, include notes so users can see
	// why they were charged or credited by admin
	if (rc.Type == "admin_balance" || rc.Type == "admin_concurrency") && rc.Notes != "" {
		out.Notes = &rc.Notes
	}

	return out
}
