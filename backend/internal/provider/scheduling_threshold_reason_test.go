package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Threshold reason text carries a source marker alongside readable
// administration details. These cases verify marker recognition, blank and
// partial stored reasons, the system-rule sentinel, and detailed JSON fields
// so decoding older data keeps a usable message and storage deadline.

// TestBuildProviderSchedulingThresholdReason_UsesSourceAndFallbackMessage
// builds a reason from whitespace text. It verifies JSON includes the
// threshold source and default message and remains recognizable as a
// threshold reason.
func TestBuildProviderSchedulingThresholdReason_UsesSourceAndFallbackMessage(t *testing.T) {
	raw := BuildProviderSchedulingThresholdReason(" \t ")

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	require.Equal(t, ProviderSchedulingThresholdReasonSource, payload["source"])
	require.Equal(t, defaultProviderSchedulingThresholdErrorMessage, payload["error_message"])
	require.True(t, IsProviderSchedulingThresholdReason(raw))
}

// TestIsProviderSchedulingThresholdReason compares a marked threshold payload
// with an unmarked payload and plain text. It verifies recognition follows
// the structured source marker.
func TestIsProviderSchedulingThresholdReason(t *testing.T) {
	require.True(t, IsProviderSchedulingThresholdReason(BuildProviderSchedulingThresholdReason("threshold reached")))
	require.False(t, IsProviderSchedulingThresholdReason(BuildTempUnschedReasonPayload("", "temporary block")))
	require.False(t, IsProviderSchedulingThresholdReason("plain text reason"))
}

// TestTempUnschedStateFromStoredReason_EmptyReasonUsesFallbackErrorMessage
// restores state from whitespace and a storage deadline. It verifies the
// state retains that deadline and supplies the built-in temporary-block
// message.
func TestTempUnschedStateFromStoredReason_EmptyReasonUsesFallbackErrorMessage(t *testing.T) {
	state := tempUnschedStateFromStoredReason(" \n ", 1735689600)

	require.NotNil(t, state)
	require.Equal(t, int64(1735689600), state.UntilUnix)
	require.Equal(t, defaultTempUnschedReasonErrorMessage, state.ErrorMessage)
}

// TestTempUnschedStateFromStoredReason_MissingRuleIndexIsSystemRule decodes a
// stored message whose JSON omits rule_index. It verifies the initialized
// minus-one sentinel remains selected for the system rule.
func TestTempUnschedStateFromStoredReason_MissingRuleIndexIsSystemRule(t *testing.T) {
	state := tempUnschedStateFromStoredReason(`{"error_message":"system cooldown"}`, 123)
	require.Equal(t, -1, state.RuleIndex)
}

// TestTempUnschedStateFromStoredReason_SchedulingThresholdJSONWithoutMessageUsesThresholdFallback
// restores a recognized source payload with no readable text. It verifies the
// threshold-specific message and the storage-provided deadline remain
// available.
func TestTempUnschedStateFromStoredReason_SchedulingThresholdJSONWithoutMessageUsesThresholdFallback(t *testing.T) {
	raw := `{"source":"` + ProviderSchedulingThresholdReasonSource + `"}`

	state := tempUnschedStateFromStoredReason(raw, 1735689600)

	require.NotNil(t, state)
	require.Equal(t, int64(1735689600), state.UntilUnix)
	require.Equal(t, defaultProviderSchedulingThresholdErrorMessage, state.ErrorMessage)
}

// TestBuildDetailedProviderSchedulingThresholdReason_IncludesReadableFields
// builds a weekly OpenAI reason at fixed trigger and reset times. It verifies
// source, window, percentages, Unix timestamps, and readable percentage
// comparison in the JSON.
func TestBuildDetailedProviderSchedulingThresholdReason_IncludesReadableFields(t *testing.T) {
	now := time.Unix(1735689600, 0).UTC()
	until := now.Add(5 * time.Hour)

	raw := BuildDetailedProviderSchedulingThresholdReason(ProviderSchedulingThresholdReasonInput{
		Platform:         PlatformOpenAI,
		Window:           "7d",
		ThresholdPercent: 90,
		UsedPercent:      92.5,
		Until:            until,
		Now:              now,
	})

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	require.Equal(t, ProviderSchedulingThresholdReasonSource, payload["source"])
	require.Equal(t, PlatformOpenAI, payload["platform"])
	require.Equal(t, "7d", payload["window"])
	require.Equal(t, float64(90), payload["threshold_percent"])
	require.Equal(t, float64(92.5), payload["used_percent"])
	require.Equal(t, float64(until.Unix()), payload["until_unix"])
	require.Equal(t, float64(now.Unix()), payload["triggered_at_unix"])
	require.Contains(t, payload["error_message"], "openai scheduling threshold reached")
	require.Contains(t, payload["error_message"], "92.5% used >= 90%")
}
