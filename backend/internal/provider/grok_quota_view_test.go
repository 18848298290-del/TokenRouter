package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// TestGrokProbeResultDoesNotExposeSharedValues checks that shared probe results give each caller independent
// quota, headers, and usage.
func TestGrokProbeResultDoesNotExposeSharedValues(t *testing.T) {
	limit, retry := int64(20), 9
	source := &GrokQuotaProbeResult{Snapshot: &usageview.QuotaSnapshot{
		Tokens: &usageview.QuotaWindow{Limit: &limit}, RetryAfterSeconds: &retry, Headers: map[string]string{"limit": "20"},
	}, LocalUsage24h: &WindowStats{Tokens: 3}}
	svc := NewGrokQuotaService(GrokQuotaOptions{}, &ProbeRuntime{})
	got, err := svc.RunProbeFlight(context.Background(), "copy", func(context.Context) (*GrokQuotaProbeResult, error) { return source, nil })
	require.NoError(t, err)
	*got.Snapshot.Tokens.Limit = 40
	*got.Snapshot.RetryAfterSeconds = 100
	got.Snapshot.Headers["limit"] = "40"
	got.LocalUsage24h.Tokens = 50
	require.Equal(t, int64(20), *source.Snapshot.Tokens.Limit)
	require.Equal(t, 9, *source.Snapshot.RetryAfterSeconds)
	require.Equal(t, "20", source.Snapshot.Headers["limit"])
	require.Equal(t, int64(3), source.LocalUsage24h.Tokens)
}
