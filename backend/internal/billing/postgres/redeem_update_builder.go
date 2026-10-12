package postgres

import (
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// redeemUpdateBuilder restores redemption snapshot fields for management updates and registration compensation.
func redeemUpdateBuilder(client *dbent.Client, code *billing.RedeemCode) *dbent.RedeemCodeUpdateOne {
	update := client.RedeemCode.UpdateOneID(code.ID).
		SetCode(code.Code).
		SetType(code.Type).
		SetValue(code.Value).
		SetStatus(code.Status).
		SetRequiresPayment(code.RequiresPayment).
		SetMaxUses(code.MaxUses).
		SetUsedCount(code.UsedCount).
		SetNotes(code.Notes)

	if code.UsedBy != nil {
		update.SetUsedBy(*code.UsedBy)
	} else {
		update.ClearUsedBy()
	}
	if code.UsedAt != nil {
		update.SetUsedAt(*code.UsedAt)
	} else {
		update.ClearUsedAt()
	}
	if code.PlanID != nil {
		update.SetPlanID(*code.PlanID)
	} else {
		update.ClearPlanID()
	}
	if code.ExpiresAt != nil {
		update.SetExpiresAt(*code.ExpiresAt)
	} else {
		update.ClearExpiresAt()
	}

	return update
}
