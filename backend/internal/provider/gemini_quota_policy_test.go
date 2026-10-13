package provider

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGeminiQuotaPolicyPrecedenceTTLAndSnapshots varies static pointer input,
// policy JSON, dynamic settings, age, and malformed text. It checks captured
// input isolation, dynamic precedence, TTL reuse/reload, and fallback to
// static JSON after invalid dynamic data.
func TestGeminiQuotaPolicyPrecedenceTTLAndSnapshots(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	rpd := int64(10)
	raw := `{"tiers":{"aistudio_free":{"pro_rpd":30}}}`
	reads := 0
	core := NewGeminiQuotaService(GeminiQuotaOptions{Now: func() time.Time { return now }, StaticTiers: map[string]GeminiTierQuotaOverride{"aistudio_free": {ProRPD: &rpd}}, StaticPolicy: `{"quota_rules":{"aistudio_free":{"gemini_pro":{"rpd":20}}}}`, LoadPolicy: func(context.Context) (string, error) { reads++; return raw, nil }})
	staticOnly := NewGeminiQuotaService(GeminiQuotaOptions{StaticTiers: map[string]GeminiTierQuotaOverride{"aistudio_free": {ProRPD: &rpd}}})
	rpd = 99
	staticQuota, _ := staticOnly.Policy(context.Background()).QuotaForTier("aistudio_free")
	require.Equal(t, int64(10), staticQuota.ProRPD)
	first, ok := core.Policy(context.Background()).QuotaForTier("aistudio_free")
	require.True(t, ok)
	require.Equal(t, int64(30), first.ProRPD)
	require.Equal(t, 1, reads)
	raw = `{"quota_rules":{"aistudio_free":{"gemini_pro":{"rpd":40}}}}`
	next, _ := core.Policy(context.Background()).QuotaForTier("aistudio_free")
	require.Equal(t, int64(30), next.ProRPD)
	require.Equal(t, 1, reads)
	now = now.Add(time.Minute)
	next, _ = core.Policy(context.Background()).QuotaForTier("aistudio_free")
	require.Equal(t, int64(40), next.ProRPD)
	require.Equal(t, 2, reads)
	raw = "invalid-json"
	now = now.Add(time.Minute)
	next, _ = core.Policy(context.Background()).QuotaForTier("aistudio_free")
	require.Equal(t, int64(20), next.ProRPD, "设置损坏沿用原静态策略回退")
}

// TestGeminiQuotaPolicyConcurrentReaderIsolation warms the policy cache before
// concurrent callers modify their returned maps. It checks unchanged cached
// default quotas and one dynamic loader invocation for this configured run.
func TestGeminiQuotaPolicyConcurrentReaderIsolation(t *testing.T) {
	var reads atomic.Int64
	core := NewGeminiQuotaService(GeminiQuotaOptions{LoadPolicy: func(context.Context) (string, error) { reads.Add(1); return "", nil }})
	core.Policy(context.Background())
	var group sync.WaitGroup
	var corrupted atomic.Bool
	// Give each worker its own cloned policy before mutation, then compare
	// fresh service reads against the warmed cache. The group join below fixes
	// the assertion point after every worker has completed.
	for range 20 {
		group.Go(func() {
			for j := range 20 {
				value := int64(j + 1)
				policy := core.Policy(context.Background())
				policy.ApplyOverrides(map[string]GeminiTierQuotaOverride{"aistudio_free": {ProRPD: &value}})
				next, _ := core.Policy(context.Background()).QuotaForTier("aistudio_free")
				if next.ProRPD != 50 {
					corrupted.Store(true)
				}
			}
		})
	}
	// Join every mutation/read loop before observing the atomic corruption
	// flag and load count, so the assertions describe completed concurrent
	// activity.
	group.Wait()
	require.False(t, corrupted.Load(), "返回策略不能污染缓存")
	require.Equal(t, int64(1), reads.Load())
}

// TestGeminiQuotaPolicyReturnCannotMutateCachedPolicy applies a limit override
// to a returned policy copy. It checks that a subsequent service read retains
// the configured default quota in its cached policy.
func TestGeminiQuotaPolicyReturnCannotMutateCachedPolicy(t *testing.T) {
	s := NewGeminiQuotaService(GeminiQuotaOptions{})
	policy := s.Policy(context.Background())
	limited := int64(1)
	policy.ApplyOverrides(map[string]GeminiTierQuotaOverride{"aistudio_free": {ProRPD: &limited}})
	next, ok := s.Policy(context.Background()).QuotaForTier("aistudio_free")
	require.True(t, ok)
	require.Equal(t, int64(50), next.ProRPD)
}
