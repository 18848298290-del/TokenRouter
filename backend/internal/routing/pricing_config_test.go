package routing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Exact configuration lookup returns selected cards without expanding wildcard
// patterns, while copying separates editable group and model collections. The
// cases exercise known and missing names, case differences, nil values, stored
// activity flags, and specific slice or map mutations.

// TestGetModelPricing probes case variations, missing IDs, and two billing
// modes in exact lookup. It asserts the selected card IDs or nil for the
// supplied model-name cases.
func TestGetModelPricing(t *testing.T) {
	ch := &PricingConfig{
		ModelPricing: []ModelPricingEntry{
			{ID: 1, Models: []string{"claude-sonnet-4"}, BillingMode: BillingModeToken, InputPrice: new(float64(3e-6))},
			{ID: 3, Models: []string{"gpt-5.1"}, BillingMode: BillingModePerRequest},
		},
	}

	tests := []struct {
		name    string // subtest display name
		model   string // requested model name under test
		wantID  int64  // expected selected card identity
		wantNil bool   // expected absent result flag
	}{
		{"exact match", "claude-sonnet-4", 1, false},
		{"case insensitive", "Claude-Sonnet-4", 1, false},
		{"not found", "gemini-3.1-pro", 0, true},
		{"wildcard pattern not matched", "claude-opus-4-20250514", 0, true},
		{"per_request model", "gpt-5.1", 3, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ch.GetModelPricing(tt.model)
			if tt.wantNil {
				require.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			require.Equal(t, tt.wantID, result.ID)
		})
	}
}

// TestGetModelPricing_ReturnsCopy appends a model name to a returned card. It
// checks that the source card still reports one name, protecting the returned
// collection header from direct source mutation.
func TestGetModelPricing_ReturnsCopy(t *testing.T) {
	ch := &PricingConfig{
		ModelPricing: []ModelPricingEntry{
			{ID: 1, Models: []string{"claude-sonnet-4"}, InputPrice: new(float64(3e-6))},
		},
	}

	result := ch.GetModelPricing("claude-sonnet-4")
	require.NotNil(t, result)

	result.Models = append(result.Models, "hacked")

	require.Equal(t, 1, len(ch.ModelPricing[0].Models))
}

// TestGetModelPricing_EmptyPricing queries configurations with nil and non-nil
// empty card lists. It checks that both representations return nil for the
// supplied model name.
func TestGetModelPricing_EmptyPricing(t *testing.T) {
	ch := &PricingConfig{ModelPricing: nil}
	require.Nil(t, ch.GetModelPricing("any-model"))

	ch2 := &PricingConfig{ModelPricing: []ModelPricingEntry{}}
	require.Nil(t, ch2.GetModelPricing("any-model"))
}

// TestPricingConfigClone modifies a cloned group-ID element and a cloned
// model-name element. It checks retained scalar identity and verifies that
// those two source collections remain unchanged.
func TestPricingConfigClone(t *testing.T) {
	original := &PricingConfig{
		ID:       1,
		Name:     "test",
		GroupIDs: []int64{10, 20},
		ModelPricing: []ModelPricingEntry{
			{
				ID:         100,
				Models:     []string{"model-a"},
				InputPrice: new(float64(5e-6)),
			},
		},
	}

	cloned := original.Clone()
	require.NotNil(t, cloned)
	require.Equal(t, original.ID, cloned.ID)
	require.Equal(t, original.Name, cloned.Name)

	cloned.GroupIDs[0] = 999
	require.Equal(t, int64(10), original.GroupIDs[0])

	cloned.ModelPricing[0].Models[0] = "hacked"
	require.Equal(t, "model-a", original.ModelPricing[0].Models[0])
}

// TestPricingConfigClone_Nil invokes configuration copying through a nil
// receiver. It checks that the operation returns nil, allowing callers to
// forward an absent configuration safely.
func TestPricingConfigClone_Nil(t *testing.T) {
	var ch *PricingConfig
	require.Nil(t, ch.Clone())
}

// TestPricingConfigIsActive supplies active, disabled, and empty stored status
// strings. It checks that the predicate accepts the active literal and rejects
// the other supplied values.
func TestPricingConfigIsActive(t *testing.T) {
	tests := []struct {
		name   string // subtest display name
		status string // stored configuration activity status
		want   bool   // expected operation result
	}{
		{"active", "active", true},
		{"disabled", "disabled", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &PricingConfig{Status: tt.status}
			require.Equal(t, tt.want, ch.IsActive())
		})
	}
}

// TestPricingConfigClone_EdgeCases checks nil mappings, nil price collections,
// and one cloned model map. It verifies that nil remains nil and a copied
// mapping mutation leaves its source target unchanged.
func TestPricingConfigClone_EdgeCases(t *testing.T) {
	t.Run("nil model mapping", func(t *testing.T) {
		original := &GroupRoutingPolicy{ModelMapping: nil}
		cloned := original.Clone()
		require.Nil(t, cloned.ModelMapping)
	})

	t.Run("nil model pricing", func(t *testing.T) {
		original := &PricingConfig{ID: 1, ModelPricing: nil}
		cloned := original.Clone()
		require.Nil(t, cloned.ModelPricing)
	})

	t.Run("deep copy model mapping", func(t *testing.T) {
		original := &GroupRoutingPolicy{
			ModelMapping: map[string]string{"gpt-4": "gpt-4-turbo"},
		}
		cloned := original.Clone()

		cloned.ModelMapping["gpt-4"] = "hacked"

		require.Equal(t, "gpt-4-turbo", original.ModelMapping["gpt-4"])
	})
}
