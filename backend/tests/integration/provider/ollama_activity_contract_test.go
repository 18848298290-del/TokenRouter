package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Ollama Cloud automatic refresh uses provider activity recorded after gateway
// attempts. A deferred-activity recorder receives an Ollama attempt and a non-
// Ollama attempt, then StopContext drains the recorder so the test can check
// which provider ID was recorded.

// TestScheduleOllamaCloudUsageActivityOnlyForOllama submits Ollama and ordinary
// OpenAI attempts to deferred activity handling, including a missing-recorder
// call. After draining the recorder, only the Ollama provider ID has an
// activity entry.
func TestScheduleOllamaCloudUsageActivityOnlyForOllama(t *testing.T) {
	deferred, activity := gatewaytestkit.DeferredActivityRecorder(t)
	ollama := ollamaUsageProvider(1)
	other := ollamaUsageProvider(2)
	other.Record.Credentials["base_url"] = "https://api.openai.com"

	(&provideradapter.TransportHealth{Deferred: deferred}).Attempt(gatewayprovider.ExecutionRecord(ollama))
	(&provideradapter.TransportHealth{Deferred: deferred}).Attempt(gatewayprovider.ExecutionRecord(other))
	(&provideradapter.TransportHealth{}).Attempt(gatewayprovider.ExecutionRecord(ollama))

	// Drain deferred activity before inspecting the recorder so the assertions
	// observe completed writes from the earlier gateway-attempt notifications.
	require.NoError(t, deferred.StopContext(context.Background()))
	_, ok := activity.Load(int64(1))
	require.True(t, ok)
	_, ok = activity.Load(int64(2))
	require.False(t, ok)
}

// ollamaUsageProvider returns an active OpenAI API-key execution provider for
// Ollama Cloud.
func ollamaUsageProvider(id int64) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id, Name: fmt.Sprintf("ollama-%d", id), Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": fmt.Sprintf("key-%d", id)},
			Extra:       map[string]any{}, Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
		},
	}
}
