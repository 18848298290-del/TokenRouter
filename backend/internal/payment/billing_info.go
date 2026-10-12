package payment

import (
	"net/mail"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Stripe invoice checkout requires buyer identity and an email, and
// tax identity is accepted as a type/value pair. Input normalization
// trims each field, uppercases country text, and represents an empty
// address or complete empty record as absent before sparse snapshot
// construction and validation.

// NilIfEmpty returns nil for an exactly empty string and a pointer
// to the supplied value otherwise. Whitespace is retained, so callers
// that need blank-value removal trim their input before using this
// helper.
func NilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// TrimBillingInfo returns a new normalized billing record and optional
// address, or nil when every normalized field is empty. Strings are
// trimmed and country text is uppercased; the returned address is
// separately allocated when it contains a retained value.
func TrimBillingInfo(info *BillingInfo) *BillingInfo {
	if info == nil {
		return nil
	}
	trimmed := &BillingInfo{
		Name:      strings.TrimSpace(info.Name),
		Email:     strings.TrimSpace(info.Email),
		TaxIDType: strings.TrimSpace(info.TaxIDType),
		TaxID:     strings.TrimSpace(info.TaxID),
	}
	if info.Address != nil {
		addr := &BillingAddress{
			Country:    strings.ToUpper(strings.TrimSpace(info.Address.Country)),
			Line1:      strings.TrimSpace(info.Address.Line1),
			Line2:      strings.TrimSpace(info.Address.Line2),
			City:       strings.TrimSpace(info.Address.City),
			State:      strings.TrimSpace(info.Address.State),
			PostalCode: strings.TrimSpace(info.Address.PostalCode),
		}
		// Keep an address only when normalization retains one of its fields,
		// so BillingInfoSnapshot can omit a blank address object from the
		// persisted checkout data.
		if addr.Country != "" || addr.Line1 != "" || addr.Line2 != "" || addr.City != "" || addr.State != "" || addr.PostalCode != "" {
			trimmed.Address = addr
		}
	}
	if trimmed.Name == "" && trimmed.Email == "" && trimmed.Address == nil && trimmed.TaxIDType == "" && trimmed.TaxID == "" {
		return nil
	}
	return trimmed
}

// BillingInfoSnapshot returns fresh sparse maps containing normalized
// nonempty billing values, or nil for an empty record. It omits
// blank address fields and the absent address object; callers apply
// ValidateBillingInfo separately when checkout requires valid identity
// information.
func BillingInfoSnapshot(info *BillingInfo) map[string]any {
	info = TrimBillingInfo(info)
	if info == nil {
		return nil
	}
	snapshot := map[string]any{}
	if info.Name != "" {
		snapshot["name"] = info.Name
	}
	if info.Email != "" {
		snapshot["email"] = info.Email
	}
	if info.TaxIDType != "" {
		snapshot["tax_id_type"] = info.TaxIDType
	}
	if info.TaxID != "" {
		snapshot["tax_id"] = info.TaxID
	}
	if info.Address != nil {
		address := map[string]any{}
		if info.Address.Country != "" {
			address["country"] = info.Address.Country
		}
		if info.Address.Line1 != "" {
			address["line1"] = info.Address.Line1
		}
		if info.Address.Line2 != "" {
			address["line2"] = info.Address.Line2
		}
		if info.Address.City != "" {
			address["city"] = info.Address.City
		}
		if info.Address.State != "" {
			address["state"] = info.Address.State
		}
		if info.Address.PostalCode != "" {
			address["postal_code"] = info.Address.PostalCode
		}
		if len(address) > 0 {
			snapshot["address"] = address
		}
	}
	return snapshot
}

// ValidateBillingInfo checks a normalized record for a name, an email
// accepted by mail.ParseAddress, and paired tax-ID type/value fields.
// An empty supplied email uses the trimmed fallback email. The function
// returns the first structured error and leaves the supplied record
// unchanged; it does not validate address or tax-number formats.
func ValidateBillingInfo(info *BillingInfo, fallbackEmail string) error {
	info = TrimBillingInfo(info)
	if info == nil {
		return infraerrors.BadRequest("BILLING_INFO_REQUIRED", "billing info is required for Stripe invoice")
	}
	if strings.TrimSpace(info.Name) == "" {
		return infraerrors.BadRequest("BILLING_NAME_REQUIRED", "billing name is required")
	}
	email := strings.TrimSpace(info.Email)
	if email == "" {
		email = strings.TrimSpace(fallbackEmail)
	}
	if email == "" {
		return infraerrors.BadRequest("BILLING_EMAIL_REQUIRED", "billing email is required")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return infraerrors.BadRequest("BILLING_EMAIL_INVALID", "billing email is invalid")
	}
	if (strings.TrimSpace(info.TaxIDType) == "") != (strings.TrimSpace(info.TaxID) == "") {
		return infraerrors.BadRequest("BILLING_TAX_ID_INVALID", "billing tax id type and value must be provided together")
	}
	return nil
}
