package requeststate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsForceCacheBilling supplies absent, true, false, and wrong-type context
// markers. The reader must return the stored boolean when valid and false for
// unavailable billing evidence.
func TestIsForceCacheBilling(t *testing.T) {
	tests := []struct {
		name     string          // case label for diagnostics
		ctx      context.Context // context holding billing marker
		expected bool            // expected forced-cache billing value
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

// TestWithForceCacheBilling derives a marked context from an unmarked parent.
// Reads must enable forced billing on the child while preserving the parent's
// preceding false value.
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

// TestIsClaudeCodeClient_Context reads default classification and then stores
// true followed by false. The hint reader must expose each derived value for
// client-specific execution decisions.
func TestIsClaudeCodeClient_Context(t *testing.T) {
	ctx := context.Background()

	require.False(t, IsClaudeCodeClient(ctx))

	ctx = SetClaudeCodeClient(ctx, true)
	require.True(t, IsClaudeCodeClient(ctx))

	ctx = SetClaudeCodeClient(ctx, false)
	require.False(t, IsClaudeCodeClient(ctx))
}

// TestSetGetClaudeCodeVersion reads a missing version and then stores a
// supplied version string. The getter must return empty initially and the exact
// captured spelling after derivation.
func TestSetGetClaudeCodeVersion(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, "", GetClaudeCodeVersion(ctx), "empty context should return empty string")

	ctx = SetClaudeCodeVersion(ctx, "2.1.63")
	require.Equal(t, "2.1.63", GetClaudeCodeVersion(ctx))
}

// TestRequestMetadataWriteAndRead_NoBridge records probe, thinking, sticky,
// retry and switch observations through context helpers. Each reader must
// return the matching value and true presence from the stored snapshot.
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

// TestExecutionHintsSnapshotIsolation derives different thinking values and
// mutates a returned snapshot. Context reads must retain their captured
// parent/child values, while a new explicit snapshot stores the later mutation
// independently.
func TestExecutionHintsSnapshotIsolation(t *testing.T) {
	parent := WithThinkingEnabled(context.Background(), true)
	child := WithThinkingEnabled(parent, false)
	value, present := ThinkingEnabledFromContext(child)
	require.False(t, value)
	require.True(t, present)
	value, present = ThinkingEnabledFromContext(parent)
	require.True(t, value)
	require.True(t, present)
	// Mutate a returned value to expose accidental snapshot aliasing; parent reads
	// must retain their earlier thinking decision before this value is published elsewhere.
	snapshot := ExecutionHintsFromContext(parent)
	snapshot.ThinkingEnabled.Value = false
	require.True(t, ExecutionHintsFromContext(parent).ThinkingEnabled.Value)
	require.False(t, ExecutionHintsFromContext(WithExecutionHints(parent, snapshot)).ThinkingEnabled.Value)
}

// TestExecutionHintsMissingAndExplicitZero compares absent evidence with stored
// zero switch/sticky identifiers and nil contexts. Presence must survive
// explicit zero, while unsupported nil derivation remains nil.
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

// TestIsSingleProviderRetry_True stores a true retry-state hint in a derived
// context. The reader must expose true for the attempt while its separate
// presence result is outside this assertion.
func TestIsSingleProviderRetry_True(t *testing.T) {
	ctx := WithSingleProviderRetry(context.Background(), true)
	value, _ := SingleProviderRetryFromContext(ctx)
	require.True(t, value)
}

// TestIsSingleProviderRetry_False_NoValue reads an unmarked context through the
// retry-state helper. The value must default to false when this request has no
// captured retry observation.
func TestIsSingleProviderRetry_False_NoValue(t *testing.T) {
	value, _ := SingleProviderRetryFromContext(context.Background())
	require.False(t, value)
}

// TestIsSingleProviderRetry_False_ExplicitFalse records a false retry-state
// hint through the setter. The reader must return false; this assertion
// protects value reading separately from the presence checks elsewhere.
func TestIsSingleProviderRetry_False_ExplicitFalse(t *testing.T) {
	ctx := WithSingleProviderRetry(context.Background(), false)
	value, _ := SingleProviderRetryFromContext(ctx)
	require.False(t, value)
}

// TestIsSingleProviderRetry_False_WrongType stores a string under an unrelated
// private context key. The retry reader must return its missing-evidence false
// value because that key does not supply an ExecutionHints snapshot.
func TestIsSingleProviderRetry_False_WrongType(t *testing.T) {
	// unrelatedKey represents fixture data outside the execution-hints context key.
	type unrelatedKey struct{}
	ctx := context.WithValue(context.Background(), unrelatedKey{}, "true")
	value, _ := SingleProviderRetryFromContext(ctx)
	require.False(t, value)
}
