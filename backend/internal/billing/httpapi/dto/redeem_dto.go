package dto

import (
	"bytes"
	"encoding/json"
	"time"
)

type RedeemCode struct {
	RequiresPayment bool       `json:"requires_payment"` // Requires a qualifying payment record.
	ID              int64      `json:"id"`
	Code            string     `json:"code"`
	Type            string     `json:"type"`
	Value           float64    `json:"value"`
	Status          string     `json:"status"`
	MaxUses         int        `json:"max_uses"`
	UsedCount       int        `json:"used_count"`
	ExpiresAt       *time.Time `json:"expires_at"`
	UsedBy          *int64     `json:"used_by"` // User of the most recent successful redemption.
	UsedAt          *time.Time `json:"used_at"` // Time of the most recent successful redemption.
	CreatedAt       time.Time  `json:"created_at"`

	PlanID *int64 `json:"plan_id"`

	// Notes is only populated for admin_balance/admin_concurrency types
	// so users can see why they were charged or credited
	Notes *string `json:"notes,omitempty"`

	User *UserSummaryResponse `json:"user,omitempty"`
}

// AdminRedeemCode exposes all redemption notes to administrators.
// User endpoints use RedeemCode, which limits notes to administrator balance and concurrency adjustments.
type AdminRedeemCode struct {
	RedeemCode

	Notes string `json:"notes"`
}

// NullableTimeField distinguishes an omitted JSON field, null, and a timestamp.
type NullableTimeField struct {
	Set   bool
	Value *time.Time
}

type BatchUpdateRedeemCodeFields struct {
	Status    *string           `json:"status,omitempty"`
	ExpiresAt NullableTimeField `json:"expires_at,omitempty"`
	Notes     *string           `json:"notes,omitempty"`

	Type    *string  `json:"type,omitempty"`
	Value   *float64 `json:"value,omitempty"`
	MaxUses *int     `json:"max_uses,omitempty"`
	PlanID  *int64   `json:"plan_id,omitempty"`
}

type BatchUpdateRedeemCodesRequest struct {
	IDs    []int64                     `json:"ids" binding:"required,min=1"`
	Fields BatchUpdateRedeemCodeFields `json:"fields" binding:"required"`
}

func (f *NullableTimeField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = nil
		return nil
	}
	var value time.Time
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}
