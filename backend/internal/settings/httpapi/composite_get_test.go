package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	gatewaydto "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
)

// TestOpenAIFastPolicySettingsFromDTO_NormalizesServiceTier checks normalization before admin settings are
// prepared.
// Empty or whitespace-only values become "all"; nonempty values become lowercase.
func TestOpenAIFastPolicySettingsFromDTO_NormalizesServiceTier(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		require.Nil(t, OpenaiFastPolicySettingsFromDTO(nil))
	})

	t.Run("empty service_tier becomes 'all'", func(t *testing.T) {
		in := &gatewaydto.OpenAIFastPolicySettings{
			Rules: []gatewaydto.OpenAIFastPolicyRule{{
				ServiceTier: "",
				Action:      "filter",
				Scope:       "all",
			}},
		}
		out := OpenaiFastPolicySettingsFromDTO(in)
		require.NotNil(t, out)
		require.Len(t, out.Rules, 1)
		require.Equal(t, tierpolicy.OpenAIFastTierAny, out.Rules[0].ServiceTier)
		require.Equal(t, "all", out.Rules[0].ServiceTier)
	})

	t.Run("whitespace-only service_tier becomes 'all'", func(t *testing.T) {
		in := &gatewaydto.OpenAIFastPolicySettings{
			Rules: []gatewaydto.OpenAIFastPolicyRule{{
				ServiceTier: "   ",
				Action:      "pass",
				Scope:       "all",
			}},
		}
		out := OpenaiFastPolicySettingsFromDTO(in)
		require.Equal(t, tierpolicy.OpenAIFastTierAny, out.Rules[0].ServiceTier)
	})

	t.Run("uppercase service_tier is lowercased", func(t *testing.T) {
		in := &gatewaydto.OpenAIFastPolicySettings{
			Rules: []gatewaydto.OpenAIFastPolicyRule{{
				ServiceTier: "PRIORITY",
				Action:      "filter",
				Scope:       "all",
				UserIDs:     []int64{42},
			}},
		}
		out := OpenaiFastPolicySettingsFromDTO(in)
		require.Equal(t, tierpolicy.OpenAIFastTierPriority, out.Rules[0].ServiceTier)
		require.Equal(t, []int64{42}, out.Rules[0].UserIDs)
	})

	t.Run("non-empty values pass through (lowercased)", func(t *testing.T) {
		in := &gatewaydto.OpenAIFastPolicySettings{
			Rules: []gatewaydto.OpenAIFastPolicyRule{
				{ServiceTier: "priority", Action: "filter", Scope: "all"},
				{ServiceTier: "flex", Action: "block", Scope: "oauth"},
				{ServiceTier: "ultrafast", Action: "pass", Scope: "all"},
				{ServiceTier: "all", Action: "pass", Scope: "apikey"},
			},
		}
		out := OpenaiFastPolicySettingsFromDTO(in)
		require.Len(t, out.Rules, 4)
		require.Equal(t, tierpolicy.OpenAIFastTierPriority, out.Rules[0].ServiceTier)
		require.Equal(t, tierpolicy.OpenAIFastTierFlex, out.Rules[1].ServiceTier)
		require.Equal(t, tierpolicy.OpenAIFastTierUltrafast, out.Rules[2].ServiceTier)
		require.Equal(t, tierpolicy.OpenAIFastTierAny, out.Rules[3].ServiceTier)
	})
}
