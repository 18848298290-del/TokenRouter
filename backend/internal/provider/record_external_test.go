package provider_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Provider billing multipliers can be absent in decoded records or explicitly
// configured to zero or a negative value. External consumers exercise those
// three representations and check the multiplier returned for provider-side
// billing without reading the package's private implementation.

// TestProvider_BillingRateMultiplier_DefaultsToOneWhenNil decodes a record
// whose JSON omits the multiplier field. The pointer stays nil, and
// BillingRateMultiplier returns one for this older-record representation.
func TestProvider_BillingRateMultiplier_DefaultsToOneWhenNil(t *testing.T) {
	var a provider.Record
	require.NoError(t, json.Unmarshal([]byte(`{"id":1,"name":"acc","status":"active"}`), &a))
	require.Nil(t, a.RateMultiplier)
	require.Equal(t, 1.0, a.BillingRateMultiplier())
}

// TestProvider_BillingRateMultiplier_AllowsZero supplies a pointer containing a
// configured zero multiplier. BillingRateMultiplier returns zero, keeping an
// explicit zero-cost provider distinct from a missing multiplier.
func TestProvider_BillingRateMultiplier_AllowsZero(t *testing.T) {
	v := 0.0
	a := provider.Record{RateMultiplier: &v}
	require.Equal(t, 0.0, a.BillingRateMultiplier())
}

// TestProvider_BillingRateMultiplier_NegativeFallsBackToOne supplies a pointer
// containing a negative multiplier. The billing accessor returns one so this
// invalid configuration cannot produce negative provider-side costs.
func TestProvider_BillingRateMultiplier_NegativeFallsBackToOne(t *testing.T) {
	v := -1.0
	a := provider.Record{RateMultiplier: &v}
	require.Equal(t, 1.0, a.BillingRateMultiplier())
}
