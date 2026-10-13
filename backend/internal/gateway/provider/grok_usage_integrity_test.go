package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestRequiresBillableGrokChatUsage covers Grok platform traffic and
// compatible providers using plain, mapped, or namespaced Grok model IDs. Each
// requires usage evidence, while an ordinary OpenAI model does not.
func TestRequiresBillableGrokChatUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string // model recognition case label
		// provider platform under evaluation
		provider *ExecutionProvider
		// candidate billing model identities
		models []string
		// expected usage evidence requirement
		want bool
	}{
		{name: "grok platform", provider: &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok}}, models: []string{"alias"}, want: true},
		{name: "compatible Grok model", provider: &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI}}, models: []string{"grok-4.5"}, want: true},
		{name: "mapped Grok model", provider: &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI}}, models: []string{"alias", "grok-4.5"}, want: true},
		{name: "namespaced Grok model", provider: &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI}}, models: []string{"x-ai/grok-4.5"}, want: true},
		{name: "ordinary OpenAI model", provider: &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI}}, models: []string{"gpt-5.4"}, want: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, testCase.want, RequiresBillableGrokChatUsage(testCase.provider, testCase.models...))
		})
	}
}

// TestHasBillableGrokChatUsageRequiresAggregateToken supplies empty,
// image-detail-only, and individually positive aggregate token buckets. Detail
// fields alone return false, while each input, output, cache-write, or
// cache-read aggregate establishes billable usage.
func TestHasBillableGrokChatUsageRequiresAggregateToken(t *testing.T) {
	t.Parallel()

	require.False(t, HasBillableGrokChatUsage(openai.ForwardUsage{}))
	require.False(t, HasBillableGrokChatUsage(openai.ForwardUsage{ImageInputTokens: 2, ImageOutputTokens: 1}))
	require.True(t, HasBillableGrokChatUsage(openai.ForwardUsage{InputTokens: 1}))
	require.True(t, HasBillableGrokChatUsage(openai.ForwardUsage{OutputTokens: 1}))
	require.True(t, HasBillableGrokChatUsage(openai.ForwardUsage{CacheCreationInputTokens: 1}))
	require.True(t, HasBillableGrokChatUsage(openai.ForwardUsage{CacheReadInputTokens: 1}))
}
