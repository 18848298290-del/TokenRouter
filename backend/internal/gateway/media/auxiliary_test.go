package media

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAuxiliaryCompletionRules exercises audio units, text-field
// precedence, Alpha status policy and image completion eligibility. It
// checks blank-first-string behavior, null/nonstring fallback, optional
// model trimming and operation-specific completion decisions.
func TestAuxiliaryCompletionRules(t *testing.T) {
	require.Nil(t, RealtimeAudioUsage(time.Minute, false))
	require.Nil(t, RealtimeAudioUsage(0, true))
	require.Equal(t, 1.5, RealtimeAudioUsage(90*time.Second, true).DurationOrUnits)
	require.Equal(t, "", TTSInputText([]byte(`{"input":"  ","text":"fallback"}`)))
	require.Equal(t, "fallback", TTSInputText([]byte(`{"input":null,"text":" fallback "}`)))
	require.Equal(t, "last", TTSInputText([]byte(`{"input":3,"prompt":"last"}`)))
	require.False(t, AlphaProviderErrorSideEffects(401))
	require.False(t, AlphaProviderErrorSideEffects(404))
	require.True(t, AlphaProviderErrorSideEffects(429))
	require.True(t, AlphaEndpointUnsupported(true, 405))
	require.False(t, AlphaEndpointUnsupported(false, 405))
	raw, ok := RequiredModel([]byte(`{"model":" my-model "}`), false)
	require.True(t, ok)
	require.Equal(t, " my-model ", raw)
	trimmed, ok := RequiredModel([]byte(`{"model":" my-model "}`), true)
	require.True(t, ok)
	require.Equal(t, "my-model", trimmed)
	require.True(t, RecordImmediateImages("images_generations", "m", 1))
	require.False(t, RecordImmediateImages("videos_generations", "m", 1))
	require.False(t, RecordImmediateImages("video_status", "m", 1))
	require.False(t, RecordImmediateImages("images_generations", "m", 0))
}
