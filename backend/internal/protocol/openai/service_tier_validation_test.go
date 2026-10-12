package openai

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateOpenAIServiceTierField supplies supported, aliased, absent, null,
// empty, nonstring, and oversized tier fields. Validation must return canonical
// labels or typed errors whose descriptions contain the bounded value or the
// nonstring placeholder asserted by each case.
func TestValidateOpenAIServiceTierField(t *testing.T) {
	t.Parallel()

	t.Run("fast normalizes to priority", func(t *testing.T) {
		norm, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","service_tier":"fast"}`))
		require.NoError(t, err)
		require.Equal(t, "priority", norm)
	})

	t.Run("priority passes through", func(t *testing.T) {
		norm, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","service_tier":"priority"}`))
		require.NoError(t, err)
		require.Equal(t, "priority", norm)
	})

	t.Run("case and whitespace insensitive", func(t *testing.T) {
		norm, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","service_tier":"  FAST "}`))
		require.NoError(t, err)
		require.Equal(t, "priority", norm)
	})

	t.Run("official tiers pass through", func(t *testing.T) {
		for _, tier := range []string{"flex", "auto", "default", "scale", "ultrafast"} {
			norm, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`))
			require.NoError(t, err, "tier %q must be accepted", tier)
			require.Equal(t, tier, norm)
		}
	})

	t.Run("invalid tier rejected", func(t *testing.T) {
		_, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","service_tier":"turbo"}`))
		require.Error(t, err)
		var invalid *InvalidServiceTierError
		require.True(t, errors.As(err, &invalid))
		require.Equal(t, "turbo", invalid.Value)
		require.Contains(t, err.Error(), "invalid service_tier")
		require.Contains(t, err.Error(), "fast", "allowed-value hint must mention fast")
	})

	t.Run("omitted field stays valid", func(t *testing.T) {
		norm, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","input":"hi"}`))
		require.NoError(t, err)
		require.Empty(t, norm)
	})

	t.Run("null value keeps omission semantics", func(t *testing.T) {
		norm, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","service_tier":null}`))
		require.NoError(t, err)
		require.Empty(t, norm)
	})

	t.Run("explicit empty string rejected as invalid enum value", func(t *testing.T) {
		_, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","service_tier":""}`))
		require.Error(t, err)
		var invalid *InvalidServiceTierError
		require.True(t, errors.As(err, &invalid))
	})

	t.Run("non-string service_tier rejected", func(t *testing.T) {
		// Numbers, booleans, objects, and arrays are invalid service_tier values.
		for _, raw := range []string{
			`{"model":"gpt-5.5","service_tier":123}`,
			`{"model":"gpt-5.5","service_tier":true}`,
			`{"model":"gpt-5.5","service_tier":{}}`,
			`{"model":"gpt-5.5","service_tier":["priority"]}`,
		} {
			_, err := ValidateServiceTierField([]byte(raw))
			require.Error(t, err, "raw=%s must be rejected", raw)
			var invalid *InvalidServiceTierError
			require.True(t, errors.As(err, &invalid), "raw=%s", raw)
			require.Equal(t, "<non-string>", invalid.Value, "raw=%s", raw)
			require.Contains(t, err.Error(), "invalid service_tier")
		}
	})

	t.Run("oversized unknown string is truncated", func(t *testing.T) {
		blob := strings.Repeat("z", 4096)
		_, err := ValidateServiceTierField([]byte(`{"model":"gpt-5.5","service_tier":"` + blob + `"}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid service_tier")
		require.NotContains(t, err.Error(), blob)
		require.Less(t, len(err.Error()), 200)
		var invalid *InvalidServiceTierError
		require.True(t, errors.As(err, &invalid))
		require.Equal(t, strings.Repeat("z", 64)+"...", invalid.Value)
	})

	t.Run("non-string large object/array is not echoed", func(t *testing.T) {
		blob := strings.Repeat("x", 4096)
		payloads := []string{
			`{"model":"gpt-5.5","service_tier":{"blob":"` + blob + `"}}`,
			`{"model":"gpt-5.5","service_tier":["` + blob + `"]}`,
		}
		for _, raw := range payloads {
			_, err := ValidateServiceTierField([]byte(raw))
			require.Error(t, err)
			require.Contains(t, err.Error(), "invalid service_tier")
			require.NotContains(t, err.Error(), blob)
			require.Less(t, len(err.Error()), 200)
			var invalid *InvalidServiceTierError
			require.True(t, errors.As(err, &invalid))
			require.Equal(t, "<non-string>", invalid.Value)
		}
	})
}

// TestNormalizeOpenAIServiceTier checks the fast alias, supported local labels,
// and unknown strings. The alias must return priority, supported labels retain
// their canonical value, and unknown labels return nil.
func TestNormalizeOpenAIServiceTier(t *testing.T) {
	t.Run("fast maps to priority", func(t *testing.T) {
		got := NormalizeServiceTier(" fast ")
		require.NotNil(t, got)
		require.Equal(t, "priority", *got)
	})

	t.Run("openai official tiers preserved", func(t *testing.T) {
		// The local label vocabulary includes priority, flex, and ultrafast.
		for _, tier := range []string{"priority", "flex", "auto", "default", "scale", "ultrafast"} {
			got := NormalizeServiceTier(tier)
			require.NotNil(t, got, "tier %q should not be normalized to nil", tier)
			require.Equal(t, tier, *got)
		}
	})

	t.Run("invalid ignored", func(t *testing.T) {
		require.Nil(t, NormalizeServiceTier("turbo"))
		require.Nil(t, NormalizeServiceTier("xxx"))
	})
}
