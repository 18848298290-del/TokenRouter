package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingdto "github.com/TokenFlux/TokenRouter/internal/billing/httpapi/dto"
)

type RedeemCode = billingdto.RedeemCode

type AdminRedeemCode = billingdto.AdminRedeemCode

type BatchUpdateRedeemCodeFields = billingdto.BatchUpdateRedeemCodeFields

type BatchUpdateRedeemCodesRequest = billingdto.BatchUpdateRedeemCodesRequest

// RedeemCodeFromService converts a redemption record for user responses.
func RedeemCodeFromService(rc *billing.RedeemCode) *RedeemCode {
	return billingdto.RedeemCodeFromService(rc)
}

// RedeemCodeFromServiceAdmin converts a redemption record for administrator responses.
func RedeemCodeFromServiceAdmin(rc *billing.RedeemCode) *AdminRedeemCode {
	return billingdto.RedeemCodeFromServiceAdmin(rc)
}
