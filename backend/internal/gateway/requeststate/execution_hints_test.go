package requeststate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsForceCacheBilling(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		expected bool
	}{
		{
			name:     "context without force cache billing",
			ctx:      context.Background(),
			expected: false,
		},
		{
			name:     "context with force cache billing set to true",
			ctx:      context.WithValue(context.Background(), cacheBillingKey{}, true),
			expected: true,
		},
		{
			name:     "context with force cache billing set to false",
			ctx:      context.WithValue(context.Background(), cacheBillingKey{}, false),
			expected: false,
		},
		{
			name:     "context with wrong type value",
			ctx:      context.WithValue(context.Background(), cacheBillingKey{}, "true"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsForceCacheBilling(tt.ctx)
			if result != tt.expected {
				t.Errorf("IsForceCacheBilling() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestWithForceCacheBilling(t *testing.T) {
	ctx := context.Background()

	// An unmarked context defaults to false.
	if IsForceCacheBilling(ctx) {
		t.Error("original context should not have force cache billing")
	}

	// Set the flag in a derived context.
	newCtx := WithForceCacheBilling(ctx)
	if !IsForceCacheBilling(newCtx) {
		t.Error("new context should have force cache billing")
	}

	// The parent retains its independent flag value.
	if IsForceCacheBilling(ctx) {
		t.Error("original context should still not have force cache billing")
	}
}

func TestIsClaudeCodeClient_Context(t *testing.T) {
	ctx := context.Background()

	require.False(t, IsClaudeCodeClient(ctx))

	ctx = SetClaudeCodeClient(ctx, true)
	require.True(t, IsClaudeCodeClient(ctx))

	ctx = SetClaudeCodeClient(ctx, false)
	require.False(t, IsClaudeCodeClient(ctx))
}

func TestSetGetClaudeCodeVersion(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, "", GetClaudeCodeVersion(ctx), "empty context should return empty string")

	ctx = SetClaudeCodeVersion(ctx, "2.1.63")
	require.Equal(t, "2.1.63", GetClaudeCodeVersion(ctx))
}

func TestRequestMetadataWriteAndRead_NoBridge(t *testing.T) {
	ctx := WithIsMaxTokensOneHaikuRequest(context.Background(), true)
	ctx = WithThinkingEnabled(ctx, true)
	ctx = WithPrefetchedStickySession(ctx, 123, 456)
	ctx = WithSingleProviderRetry(ctx, true)
	ctx = WithProviderSwitchCount(ctx, 2)
	value, present := IsMaxTokensOneHaikuRequestFromContext(ctx)
	require.True(t, value)
	require.True(t, present)
	value, present = ThinkingEnabledFromContext(ctx)
	require.True(t, value)
	require.True(t, present)
	id, present := PrefetchedStickyProviderIDFromContext(ctx)
	require.Equal(t, int64(123), id)
	require.True(t, present)
	id, present = PrefetchedStickyGroupIDFromContext(ctx)
	require.Equal(t, int64(456), id)
	require.True(t, present)
	value, present = SingleProviderRetryFromContext(ctx)
	require.True(t, value)
	require.True(t, present)
	count, present := ProviderSwitchCountFromContext(ctx)
	require.Equal(t, 2, count)
	require.True(t, present)
}

func TestExecutionHintsSnapshotIsolation(t *testing.T) {
	parent := WithThinkingEnabled(context.Background(), true)
	child := WithThinkingEnabled(parent, false)
	value, present := ThinkingEnabledFromContext(child)
	require.False(t, value)
	require.True(t, present)
	value, present = ThinkingEnabledFromContext(parent)
	require.True(t, value)
	require.True(t, present)
	// Mutating a returned snapshot leaves existing contexts and later derived attempts unchanged.
	snapshot := ExecutionHintsFromContext(parent)
	snapshot.ThinkingEnabled.Value = false
	require.True(t, ExecutionHintsFromContext(parent).ThinkingEnabled.Value)
	require.False(t, ExecutionHintsFromContext(WithExecutionHints(parent, snapshot)).ThinkingEnabled.Value)
}

func TestExecutionHintsMissingAndExplicitZero(t *testing.T) {
	value, present := ThinkingEnabledFromContext(context.Background())
	require.False(t, value)
	require.False(t, present)
	ctx := WithProviderSwitchCount(context.Background(), 0)
	count, present := ProviderSwitchCountFromContext(ctx)
	require.Zero(t, count)
	require.True(t, present)
	ctx = WithPrefetchedStickySession(ctx, 0, 0)
	id, present := PrefetchedStickyGroupIDFromContext(ctx)
	require.Zero(t, id)
	require.True(t, present)
	// Nil contexts return empty hints.
	var missing context.Context
	require.Zero(t, ExecutionHintsFromContext(missing))
	require.Nil(t, WithThinkingEnabled(missing, true))
}

func TestIsSingleProviderRetry_True(t *testing.T) {
	ctx := WithSingleProviderRetry(context.Background(), true)
	value, _ := SingleProviderRetryFromContext(ctx)
	require.True(t, value)
}

func TestIsSingleProviderRetry_False_NoValue(t *testing.T) {
	value, _ := SingleProviderRetryFromContext(context.Background())
	require.False(t, value)
}

func TestIsSingleProviderRetry_False_ExplicitFalse(t *testing.T) {
	ctx := WithSingleProviderRetry(context.Background(), false)
	value, _ := SingleProviderRetryFromContext(ctx)
	require.False(t, value)
}

func TestIsSingleProviderRetry_False_WrongType(t *testing.T) {
	// An unrelated context key does not supply ExecutionHints.
	type unrelatedKey struct{}
	ctx := context.WithValue(context.Background(), unrelatedKey{}, "true")
	value, _ := SingleProviderRetryFromContext(ctx)
	require.False(t, value)
}
