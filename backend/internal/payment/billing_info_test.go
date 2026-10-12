package payment

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// TestPSValidateBillingInfo supplies missing identity fields, malformed
// email, an incomplete tax pair, and accepted email/tax alternatives.
// It checks the first structured error reason or successful validation
// for each listed request.
func TestPSValidateBillingInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string       // validation scenario label
		info         *BillingInfo // supplied billing identity
		fallbackMail string       // fallback buyer email
		wantReason   string       // expected structured error reason
	}{
		{
			name:         "requires billing info",
			info:         nil,
			fallbackMail: "buyer@example.com",
			wantReason:   "BILLING_INFO_REQUIRED",
		},
		{
			name: "requires name",
			info: &BillingInfo{
				Email: "buyer@example.com",
			},
			wantReason: "BILLING_NAME_REQUIRED",
		},
		{
			name: "requires email when fallback is empty",
			info: &BillingInfo{
				Name: "Buyer",
			},
			wantReason: "BILLING_EMAIL_REQUIRED",
		},
		{
			name: "rejects invalid email",
			info: &BillingInfo{
				Name:  "Buyer",
				Email: "not-an-email",
			},
			wantReason: "BILLING_EMAIL_INVALID",
		},
		{
			name: "rejects tax id without type",
			info: &BillingInfo{
				Name:  "Buyer",
				Email: "buyer@example.com",
				TaxID: "123456",
			},
			wantReason: "BILLING_TAX_ID_INVALID",
		},
		{
			name: "accepts fallback email",
			info: &BillingInfo{
				Name: "Buyer",
			},
			fallbackMail: "buyer@example.com",
		},
		{
			name: "accepts tax id pair",
			info: &BillingInfo{
				Name:      "Buyer",
				Email:     "buyer@example.com",
				TaxIDType: "eu_vat",
				TaxID:     "DE123456789",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateBillingInfo(tt.info, tt.fallbackMail)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("validate billing info: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %s error, got nil", tt.wantReason)
			}
			appErr := apperror.FromError(err)
			if appErr.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", appErr.Reason, tt.wantReason)
			}
		})
	}
}

// TestPSBillingInfoSnapshotTrimsAndOmitsEmptyValues builds a snapshot
// from padded identity and address text. It checks trimmed values,
// uppercase country text, nested address shape, and omission of the
// blank second address line.
func TestPSBillingInfoSnapshotTrimsAndOmitsEmptyValues(t *testing.T) {
	t.Parallel()

	snapshot := BillingInfoSnapshot(&BillingInfo{
		Name:      "  Buyer Inc  ",
		Email:     "  billing@example.com  ",
		TaxIDType: "  eu_vat  ",
		TaxID:     "  DE123456789  ",
		Address: &BillingAddress{
			Country:    " de ",
			Line1:      "  Main Street 1  ",
			Line2:      "  ",
			City:       "  Berlin  ",
			State:      "  BE  ",
			PostalCode: " 10115 ",
		},
	})

	if snapshot["name"] != "Buyer Inc" {
		t.Fatalf("name = %#v", snapshot["name"])
	}
	if snapshot["email"] != "billing@example.com" {
		t.Fatalf("email = %#v", snapshot["email"])
	}
	if snapshot["tax_id_type"] != "eu_vat" {
		t.Fatalf("tax_id_type = %#v", snapshot["tax_id_type"])
	}
	if snapshot["tax_id"] != "DE123456789" {
		t.Fatalf("tax_id = %#v", snapshot["tax_id"])
	}
	address, ok := snapshot["address"].(map[string]any)
	if !ok {
		t.Fatalf("address type = %T, want map[string]any", snapshot["address"])
	}
	if address["country"] != "DE" {
		t.Fatalf("country = %#v", address["country"])
	}
	if address["line1"] != "Main Street 1" {
		t.Fatalf("line1 = %#v", address["line1"])
	}
	if _, ok := address["line2"]; ok {
		t.Fatalf("line2 should be omitted: %#v", address)
	}
	if address["city"] != "Berlin" {
		t.Fatalf("city = %#v", address["city"])
	}
	if address["state"] != "BE" {
		t.Fatalf("state = %#v", address["state"])
	}
	if address["postal_code"] != "10115" {
		t.Fatalf("postal_code = %#v", address["postal_code"])
	}
}

// TestPSBillingInfoSnapshotReturnsNilForEmptyInfo supplies whitespace
// identity and address text, then nil input. It checks that neither
// case produces a sparse map for an empty normalized billing record.
func TestPSBillingInfoSnapshotReturnsNilForEmptyInfo(t *testing.T) {
	t.Parallel()

	if got := BillingInfoSnapshot(&BillingInfo{
		Name:    " ",
		Address: &BillingAddress{Line1: " "},
	}); got != nil {
		t.Fatalf("snapshot = %#v, want nil", got)
	}
	if got := BillingInfoSnapshot(nil); got != nil {
		t.Fatalf("snapshot = %#v, want nil", got)
	}
}
