package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingdto "github.com/TokenFlux/TokenRouter/internal/billing/httpapi/dto"
)

// redeem_response.go exposes redemption response aliases and delegates user
// and administrator mappings.
// User and administrator redemption endpoints share DTO mappings with distinct
// note visibility. These response aliases keep handler call sites using the
// same JSON structures, while conversion wrappers delegate nil handling and
// record mapping to the DTO package.

// RedeemCode aliases the user redemption response with restricted
// adjustment-note visibility.
type RedeemCode = billingdto.RedeemCode

// AdminRedeemCode aliases the redemption response containing full
// administrator notes.
type AdminRedeemCode = billingdto.AdminRedeemCode

// BatchUpdateRedeemCodeFields aliases presence-aware common changes supplied
// for batch code updates.
type BatchUpdateRedeemCodeFields = billingdto.BatchUpdateRedeemCodeFields

// BatchUpdateRedeemCodesRequest aliases target code IDs and their supplied
// common update fields.
type BatchUpdateRedeemCodesRequest = billingdto.BatchUpdateRedeemCodesRequest

// RedeemCodeFromService delegates user redemption response mapping and returns
// nil for nil input. It exposes notes for nonempty administrator adjustments;
// optional ID, timestamp and visible reason pointers follow the DTO mapper's
// read-only sharing rules.
func RedeemCodeFromService(rc *billing.RedeemCode) *RedeemCode {
	return billingdto.RedeemCodeFromService(rc)
}

// RedeemCodeFromServiceAdmin delegates administrator redemption response
// mapping and returns nil for nil input. The outer Notes string is copied by
// value; the common mapping retains optional ID, date and visible adjustment
// reason pointers for read-only response use.
func RedeemCodeFromServiceAdmin(rc *billing.RedeemCode) *AdminRedeemCode {
	return billingdto.RedeemCodeFromServiceAdmin(rc)
}
