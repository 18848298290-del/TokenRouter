package httpapi

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Voice endpoints resolve to media API routes even when OAuth text traffic
// uses the CLI proxy. Resource validation rejects unsupported paths before
// credential or transport dependencies are needed, and audio recognition
// requires nonempty payload data in a supported Realtime event.
//
// Relay completion and its final audio flag can become visible at different
// times. The concurrent fixture stores the flag before sending the completion
// error; the waiting helper receives completion before loading that flag.
// This ordering lets the assertion observe the final value alongside EOF,
// which distinguishes a completed relay observation from an earlier snapshot.

// TestBuildGrokVoiceURL_UsesAPIDefaultForCLIProxyBase starts with the official
// CLI proxy endpoint. It checks TTS and Realtime routes resolve to the
// official media API base for that OAuth provider.
func TestBuildGrokVoiceURL_UsesAPIDefaultForCLIProxyBase(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"base_url": xai.DefaultCLIBaseURL,
			},
		},
	}
	url, err := (gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}).Voice(provider, "tts")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/tts", url)

	url, err = (gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}).Voice(provider, "realtime")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/realtime", url)
}

// TestBuildGrokVoiceURL_EmptyBaseFallsBackToAPI supplies OAuth credentials
// with no stored endpoint. It checks the STT route uses the official API
// default when text proxy configuration supplies no media URL.
func TestBuildGrokVoiceURL_EmptyBaseFallsBackToAPI(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Credentials: map[string]any{},
		},
	}
	url, err := (gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}).Voice(provider, "stt")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/stt", url)
}

// TestBuildGrokVoiceURL_RequiresEndpoint supplies a blank Voice resource name
// to route construction. It checks the missing endpoint is rejected before any
// transport or credential acquisition takes place.
func TestBuildGrokVoiceURL_RequiresEndpoint(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	_, err := (gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}).Voice(provider, "  ")
	require.Error(t, err)
}

// TestBuildGrokVoiceURL_EncodesCustomVoicePathSegments checks a custom-voice
// audio path and a traversal input. It protects the valid resource route and
// requires rejection of the dot-dot segment fixture.
func TestBuildGrokVoiceURL_EncodesCustomVoicePathSegments(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	got, err := (gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}).Voice(provider, "custom-voices/nlbqfwie/audio")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/custom-voices/nlbqfwie/audio", got)

	_, err = (gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}).Voice(provider, "custom-voices/../audio")
	require.Error(t, err)
}

// TestForwardGrokVoice_RejectsNonGrok provides an OpenAI platform record to
// Voice execution. It checks platform validation returns a descriptive error
// before the empty executor can reach its transport dependencies.
func TestForwardGrokVoice_RejectsNonGrok(t *testing.T) {
	svc := &GrokExecutor{}
	_, err := svc.ForwardGrokVoice(context.Background(), nil, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI}}, "tts", []byte(`{}`), "application/json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")
}

// TestAwaitGrokRealtimeAudioObservedReadsFlagAfterRelayExits publishes audio
// observation before the relay completion error. It checks the waiting helper
// returns both EOF and the final flag after receiving completion.
func TestAwaitGrokRealtimeAudioObservedReadsFlagAfterRelayExits(t *testing.T) {
	errCh := make(chan error, 1)
	var observed atomic.Bool
	// Publish the audio flag before sending completion, so the receiver tests
	// observation after relay exit rather than an earlier concurrent snapshot.
	go func() {
		observed.Store(true)
		errCh <- io.EOF
	}()
	got, err := xai.AwaitGrokRealtimeAudioObserved(errCh, &observed)
	require.ErrorIs(t, err, io.EOF)
	require.True(t, got, "audioObserved must be read after the relay returns, not before <-errCh")
}

// TestGrokRealtimeEventHasAudio compares session, transcript and empty audio
// events with two nonempty audio shapes. It checks that audio observation
// requires actual payload data in the supported event fields.
func TestGrokRealtimeEventHasAudio(t *testing.T) {
	require.False(t, xai.GrokRealtimeEventHasAudio([]byte(`{"type":"session.created"}`)))
	require.False(t, xai.GrokRealtimeEventHasAudio([]byte(`{"type":"response.audio_transcript.delta","delta":"hi"}`)))
	require.False(t, xai.GrokRealtimeEventHasAudio([]byte(`{"type":"response.audio.delta","delta":""}`)))
	require.True(t, xai.GrokRealtimeEventHasAudio([]byte(`{"type":"response.audio.delta","delta":"abc"}`)))
	require.True(t, xai.GrokRealtimeEventHasAudio([]byte(`{"type":"response.output_audio.delta","audio":"abc"}`)))
}

// TestForwardGrokVoice_RejectsUnknownEndpoint passes an unsupported resource
// to an otherwise Grok provider. It checks validation reports the endpoint
// error before credentials or network execution can begin.
func TestForwardGrokVoice_RejectsUnknownEndpoint(t *testing.T) {
	svc := &GrokExecutor{}
	_, err := svc.ForwardGrokVoice(context.Background(), nil, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok}}, "unknown", []byte(`{}`), "application/json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported")
}
