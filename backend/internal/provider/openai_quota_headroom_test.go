package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOpenAIQuotaHeadroomFactor_PrimaryUsedPercent supplies fresh primary
// usage with a future reset. Twenty-percent utilization must produce the
// remaining fraction of 0.8 as the scheduling factor.
func TestOpenAIQuotaHeadroomFactor_PrimaryUsedPercent(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	provider := &Record{
		Extra: map[string]any{
			"codex_primary_used_percent": 20.0,
			"codex_primary_reset_at":     now.Add(24 * time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at":     now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.InDelta(t, 0.8, OpenAIQuotaHeadroomFactor(provider, now), 0.0001)
}

// TestOpenAIQuotaHeadroomFactor_PrimaryMissingIsNeutral supplies a fresh
// timestamp with no primary measurement. Scoring must return the configured
// neutral factor for this incomplete observation.
func TestOpenAIQuotaHeadroomFactor_PrimaryMissingIsNeutral(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	provider := &Record{
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.Equal(t, openAIQuotaHeadroomNeutralFactor, OpenAIQuotaHeadroomFactor(provider, now))
}

// TestOpenAIQuotaHeadroomFactor_PrimaryResetExpiredIsNeutral supplies fresh
// utilization whose primary reset has passed. The expired window must
// contribute the neutral scheduling factor despite its earlier measurement.
func TestOpenAIQuotaHeadroomFactor_PrimaryResetExpiredIsNeutral(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	provider := &Record{
		Extra: map[string]any{
			"codex_primary_used_percent": 20.0,
			"codex_primary_reset_at":     now.Add(-time.Minute).Format(time.RFC3339),
			"codex_usage_updated_at":     now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.Equal(t, openAIQuotaHeadroomNeutralFactor, OpenAIQuotaHeadroomFactor(provider, now))
}

// TestOpenAIQuotaHeadroomFactor_SecondaryLowHeadroomDiscountsPrimary combines
// primary remaining quota with live secondary exhaustion pressure. The
// secondary five-percent remainder must halve the primary 0.8 factor to 0.4.
func TestOpenAIQuotaHeadroomFactor_SecondaryLowHeadroomDiscountsPrimary(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	provider := &Record{
		Extra: map[string]any{
			"codex_primary_used_percent":   20.0,
			"codex_primary_reset_at":       now.Add(24 * time.Hour).Format(time.RFC3339),
			"codex_secondary_used_percent": 95.0,
			"codex_secondary_reset_at":     now.Add(time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at":       now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.InDelta(t, 0.4, OpenAIQuotaHeadroomFactor(provider, now), 0.0001)
}
