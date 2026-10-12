package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider/usageview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestCNProviderThresholdCandidates reads five-hour and weekly candidates from a monitor snapshot.
func TestCNProviderThresholdCandidates(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	reset5h := observed.Add(3 * time.Hour)
	resetWeekly := observed.Add(4 * 24 * time.Hour)
	used5h, usedWeekly := 90.0, 50.0
	provider := cnCodingTestProvider(capability.PlatformKimi)
	attachCNMonitorLimits(provider, observed, []UpstreamUsageLimit{
		{Name: "5h", Used: &used5h, ResetAt: &reset5h},
		{Name: "7d", Used: &usedWeekly, ResetAt: &resetWeekly},
	})
	cands := CNProviderThresholdCandidates(provider, capability.PlatformKimi)
	require.Len(t, cands, 2)

	// A window with no usage value produces no candidate.
	partial := cnCodingTestProvider(capability.PlatformKimi)
	attachCNMonitorLimits(partial, observed, []UpstreamUsageLimit{{Name: "5h", ResetAt: &reset5h}})
	require.Empty(t, filterNil(CNProviderThresholdCandidates(partial, capability.PlatformKimi)))

	// Changing credentials invalidates the prior snapshot identity.
	provider.Credentials["api_key"] = "sk-changed"
	require.Empty(t, CNProviderThresholdCandidates(provider, capability.PlatformKimi))
}

func filterNil(cands []*SchedulingThresholdCandidate) []*SchedulingThresholdCandidate {
	var out []*SchedulingThresholdCandidate
	for _, c := range cands {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

// TestEvaluateProviderSchedulingThreshold_KimiCodingPlan checks that five-hour usage
// above the threshold pauses a Coding Plan provider until that window resets.
func TestEvaluateProviderSchedulingThreshold_KimiCodingPlan(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	used5h, usedWeekly := 90.0, 30.0
	weeklyReset := now.Add(7 * 24 * time.Hour)
	provider := cnCodingTestProvider(capability.PlatformKimi)
	attachCNMonitorLimits(provider, now, []UpstreamUsageLimit{
		{Name: "5h", Used: &used5h, ResetAt: &reset},
		{Name: "7d", Used: &usedWeekly, ResetAt: &weeklyReset},
	})
	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{capability.PlatformKimi: 80}, now)
	require.True(t, decision.ShouldPause)
	require.Equal(t, capability.PlatformKimi, decision.Platform)
	require.Equal(t, "5h", decision.Window)
	require.InDelta(t, 90.0, decision.UsedPercent, 1e-9)
	require.NotNil(t, decision.Until)
	require.True(t, reset.Equal(*decision.Until))
}

// TestEvaluateProviderSchedulingThreshold_CNWindowResetSkipped checks that expired windows
// and usage below the threshold do not pause scheduling; matching requires a future reset.
func TestEvaluateProviderSchedulingThreshold_CNWindowResetSkipped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	// The reset deadline has elapsed.
	expiredUsed := 99.0
	expiredReset := now.Add(-time.Hour)
	expired := cnCodingTestProvider(capability.PlatformZhipu)
	attachCNMonitorLimits(expired, now, []UpstreamUsageLimit{{Name: "5h", Used: &expiredUsed, ResetAt: &expiredReset}})
	require.False(t, EvaluateProviderSchedulingThreshold(expired, map[string]int{capability.PlatformZhipu: 80}, now).ShouldPause)

	// Usage is below the configured threshold.
	lowUsed := 20.0
	lowReset := now.Add(3 * time.Hour)
	low := cnCodingTestProvider(capability.PlatformZhipu)
	attachCNMonitorLimits(low, now, []UpstreamUsageLimit{{Name: "5h", Used: &lowUsed, ResetAt: &lowReset}})
	require.False(t, EvaluateProviderSchedulingThreshold(low, map[string]int{capability.PlatformZhipu: 80}, now).ShouldPause)
}

func TestEvaluateProviderSchedulingThreshold_OpenAIChoosesLatestResetWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	wantUntil := now.Add(72 * time.Hour)
	provider := &Record{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_5h_used_percent": 90.0,
			"codex_5h_reset_at":     now.Add(2 * time.Hour).Format(time.RFC3339),
			"codex_7d_used_percent": 85.0,
			"codex_7d_reset_at":     wantUntil.Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{
		PlatformOpenAI: 80,
	}, now)

	require.True(t, decision.ShouldPause)
	require.Equal(t, PlatformOpenAI, decision.Platform)
	require.Equal(t, "7d", decision.Window)
	require.Empty(t, decision.Scope)
	require.Equal(t, 85.0, decision.UsedPercent)
	require.NotNil(t, decision.Until)
	require.True(t, wantUntil.Equal(*decision.Until))
}

func TestEvaluateProviderSchedulingThreshold_OpenAIIgnoresMismatchedCodexSnapshotIdentity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 13, 8, 50, 0, 0, time.UTC)
	provider := &Record{
		Platform: PlatformOpenAI,
		Type:     ProviderTypeOAuth,
		Credentials: map[string]any{
			"email":                "CageLeen9208@outlook.com",
			"chatgpt_account_id":   "1f945aa7-d9a9-4369-9542-0c702ff4adb0",
			"workspace_id":         "org-nU4goUxMmureroyswT5oYPv4",
			"chatgpt_workspace_id": "org-nU4goUxMmureroyswT5oYPv4",
		},
		Extra: map[string]any{
			"email":                 "MasonDobies01@outlook.com",
			"name":                  "Paul Clark",
			"workspace_id":          "org-avRk1G4qdXg7qph3cRIraNKf",
			"codex_7d_used_percent": 100.0,
			"codex_7d_reset_at":     now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{
		PlatformOpenAI: 99,
	}, now)

	require.False(t, decision.ShouldPause)
}

func TestEvaluateProviderSchedulingThreshold_AnthropicIgnoresExpiredFiveHourWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	expiredEnd := now.Add(-30 * time.Minute)
	wantUntil := now.Add(5 * 24 * time.Hour)
	provider := &Record{
		Platform:         PlatformAnthropic,
		SessionWindowEnd: &expiredEnd,
		Extra: map[string]any{
			"session_window_utilization":   0.99,
			"passive_usage_7d_utilization": 0.82,
			"passive_usage_7d_reset":       float64(wantUntil.Unix()),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{
		PlatformAnthropic: 80,
	}, now)

	require.True(t, decision.ShouldPause)
	require.Equal(t, PlatformAnthropic, decision.Platform)
	require.Equal(t, "7d", decision.Window)
	require.Empty(t, decision.Scope)
	require.Equal(t, 82.0, decision.UsedPercent)
	require.NotNil(t, decision.Until)
	require.True(t, wantUntil.Equal(*decision.Until))
}

func TestEvaluateAnthropicFableSchedulingThreshold_UsesProviderOverrideWithoutPausingProvider(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	wantUntil := now.Add(4 * 24 * time.Hour)
	provider := &Record{
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"provider_scheduling_threshold": 60,
		},
		Extra: map[string]any{
			"passive_usage_7d_utilization":    0.40,
			"passive_usage_7d_reset":          float64(now.Add(3 * 24 * time.Hour).Unix()),
			"passive_usage_7d_oi_utilization": 0.61,
			"passive_usage_7d_oi_reset":       float64(wantUntil.Unix()),
		},
	}

	thresholds := map[string]int{
		PlatformAnthropic: 100,
	}

	providerDecision := EvaluateProviderSchedulingThreshold(provider, thresholds, now)
	require.False(t, providerDecision.ShouldPause)

	decision := EvaluateAnthropicFableSchedulingThreshold(provider, thresholds, now)

	require.True(t, decision.ShouldPause)
	require.Equal(t, PlatformAnthropic, decision.Platform)
	require.Equal(t, "7d_oi", decision.Window)
	require.Equal(t, AnthropicFableRateLimitKey, decision.Scope)
	require.Equal(t, 60, decision.ThresholdPercent)
	require.Equal(t, 61.0, decision.UsedPercent)
	require.NotNil(t, decision.Until)
	require.True(t, wantUntil.Equal(*decision.Until))
}

func TestEvaluateProviderSchedulingThreshold_OpenAIPreservesPercentageSemantics(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	openAIUntil := now.Add(24 * time.Hour)
	openAIProvider := &Record{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_5h_used_percent": 1.0,
			"codex_5h_reset_at":     openAIUntil.Format(time.RFC3339),
		},
	}

	candidate := openAIThresholdCandidate(openAIProvider.Extra, "5h", now)
	require.NotNil(t, candidate)
	require.Equal(t, 1.0, candidate.UsedPercent)

	openAIDecision := EvaluateProviderSchedulingThreshold(openAIProvider, map[string]int{
		PlatformOpenAI: 90,
	}, now)
	require.False(t, openAIDecision.ShouldPause)

	openAIProvider.Extra["codex_5h_used_percent"] = 91.0
	openAIDecision = EvaluateProviderSchedulingThreshold(openAIProvider, map[string]int{
		PlatformOpenAI: 90,
	}, now)
	require.True(t, openAIDecision.ShouldPause)
	require.Equal(t, 91.0, openAIDecision.UsedPercent)
}

// TestEvaluateProviderSchedulingThreshold_OpenAISkipsStaleSnapshot checks that stale usage cannot pause scheduling.
func TestEvaluateProviderSchedulingThreshold_OpenAISkipsStaleSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	provider := &Record{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-2 * time.Hour).Format(time.RFC3339),
			"codex_5h_used_percent":  100.0,
			"codex_5h_reset_at":      now.Add(3 * time.Hour).Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{PlatformOpenAI: 90}, now)

	require.False(t, decision.ShouldPause)
}

// TestEvaluateProviderSchedulingThreshold_OpenAISkipsResetWindow checks that reset windows no longer contribute usage.
func TestEvaluateProviderSchedulingThreshold_OpenAISkipsResetWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	provider := &Record{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
			"codex_5h_used_percent":  100.0,
			"codex_5h_reset_at":      now.Add(-time.Second).Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{PlatformOpenAI: 90}, now)

	require.False(t, decision.ShouldPause)
}

// TestEvaluateProviderSchedulingThreshold_OpenAIPausesFreshExhaustedSnapshot checks a fresh five-hour snapshot
// above threshold.
func TestEvaluateProviderSchedulingThreshold_OpenAIPausesFreshExhaustedSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(3 * time.Hour)
	provider := &Record{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
			"codex_5h_used_percent":  100.0,
			"codex_5h_reset_at":      resetAt.Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{PlatformOpenAI: 90}, now)

	require.True(t, decision.ShouldPause)
	require.Equal(t, "5h", decision.Window)
	require.Equal(t, 100.0, decision.UsedPercent)
	require.NotNil(t, decision.Until)
	require.True(t, resetAt.Equal(*decision.Until))
}

// TestEvaluateProviderSchedulingThreshold_OpenAIPausesFreshExhaustedSevenDayWindow checks a fresh seven-day
// snapshot above threshold.
func TestEvaluateProviderSchedulingThreshold_OpenAIPausesFreshExhaustedSevenDayWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * 24 * time.Hour)
	provider := &Record{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
			"codex_7d_used_percent":  95.0,
			"codex_7d_reset_at":      resetAt.Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{PlatformOpenAI: 90}, now)

	require.True(t, decision.ShouldPause)
	require.Equal(t, "7d", decision.Window)
	require.Equal(t, 95.0, decision.UsedPercent)
	require.NotNil(t, decision.Until)
	require.True(t, resetAt.Equal(*decision.Until))
}

func TestEvaluateProviderSchedulingThreshold_AnthropicPreservesFractionalUtilizationSemantics(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	anthropicUntil := now.Add(5 * time.Hour)
	anthropicProvider := &Record{
		Platform:         PlatformAnthropic,
		SessionWindowEnd: &anthropicUntil,
		Extra: map[string]any{
			"session_window_utilization": 0.92,
		},
	}

	anthropicDecision := EvaluateProviderSchedulingThreshold(anthropicProvider, map[string]int{
		PlatformAnthropic: 90,
	}, now)

	require.True(t, anthropicDecision.ShouldPause)
	require.Equal(t, 92.0, anthropicDecision.UsedPercent)
}

func TestEvaluateProviderSchedulingThreshold_ProviderOverrideCanLowerOpenAIThreshold(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	wantUntil := now.Add(12 * time.Hour)
	provider := &Record{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"provider_scheduling_threshold": 80,
		},
		Extra: map[string]any{
			"codex_7d_used_percent": 85.0,
			"codex_7d_reset_at":     wantUntil.Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{
		PlatformOpenAI: 90,
	}, now)

	require.True(t, decision.ShouldPause)
	require.Equal(t, PlatformOpenAI, decision.Platform)
	require.Equal(t, 80, decision.ThresholdPercent)
	require.Equal(t, "7d", decision.Window)
	require.Empty(t, decision.Scope)
	require.Equal(t, 85.0, decision.UsedPercent)
	require.NotNil(t, decision.Until)
	require.True(t, wantUntil.Equal(*decision.Until))
}

func TestEvaluateProviderSchedulingThreshold_ProviderOverrideHundredDisablesOpenAI(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	provider := &Record{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"provider_scheduling_threshold": 100,
		},
		Extra: map[string]any{
			"codex_7d_used_percent": 99.0,
			"codex_7d_reset_at":     now.Add(24 * time.Hour).Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{
		PlatformOpenAI: 80,
	}, now)

	require.False(t, decision.ShouldPause)
	require.Equal(t, 100, decision.ThresholdPercent)
}

func TestEvaluateProviderSchedulingThreshold_ProviderOverrideRoundsDecimalThreshold(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	wantUntil := now.Add(12 * time.Hour)
	provider := &Record{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"provider_scheduling_threshold": 75.5,
		},
		Extra: map[string]any{
			"codex_7d_used_percent": 80.0,
			"codex_7d_reset_at":     wantUntil.Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{
		PlatformOpenAI: 90,
	}, now)

	require.True(t, decision.ShouldPause)
	require.Equal(t, 76, decision.ThresholdPercent)
	require.Equal(t, 80.0, decision.UsedPercent)
	require.NotNil(t, decision.Until)
	require.True(t, wantUntil.Equal(*decision.Until))
}

func TestEvaluateProviderSchedulingThreshold_UnsupportedPlatformsDoNotPause(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		platform  string
		threshold int
		extra     map[string]any
	}{
		{
			name:      "gemini",
			platform:  PlatformGemini,
			threshold: 80,
			extra: map[string]any{
				"gemini_usage_raw": map[string]any{
					"buckets": []any{
						map[string]any{
							"modelId":           "gemini-2.5-pro",
							"remainingFraction": 0.05,
							"resetTime":         now.Add(2 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
		},
		{
			name:      "kiro",
			platform:  "kiro",
			threshold: 90,
			extra: map[string]any{
				"kiro_sched_utilization": 99.0,
				"kiro_sched_reset_at":    now.Add(24 * time.Hour).Format(time.RFC3339),
			},
		},
		{
			name:      "antigravity",
			platform:  PlatformAntigravity,
			threshold: 90,
			extra: map[string]any{
				"antigravity_sched_utilization": 92.0,
				"antigravity_sched_reset_at":    now.Add(48 * time.Hour).Format(time.RFC3339),
				"antigravity_sched_scope":       "gemini",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &Record{
				Platform: tc.platform,
				Credentials: map[string]any{
					"provider_scheduling_threshold": 1,
				},
				Extra: tc.extra,
			}

			decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{
				tc.platform: tc.threshold,
			}, now)

			require.False(t, decision.ShouldPause)
			require.Zero(t, decision.ThresholdPercent)
		})
	}
}

func TestEvaluateProviderSchedulingThreshold_GrokUsesConfiguredThresholds(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	wantUntil := now.Add(2 * time.Hour)
	provider := &Record{
		Platform: PlatformGrok,
		Extra: map[string]any{
			"grok_sched_utilization": 92.0,
			"grok_sched_reset_at":    wantUntil.Format(time.RFC3339),
		},
	}

	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{
		PlatformGrok: 90,
	}, now)

	require.True(t, decision.ShouldPause)
	require.Equal(t, PlatformGrok, decision.Platform)
	require.Equal(t, 90, decision.ThresholdPercent)
	require.Equal(t, "grok", decision.Scope)
	require.Equal(t, 92.0, decision.UsedPercent)
	require.NotNil(t, decision.Until)
	require.True(t, wantUntil.Equal(*decision.Until))
}

func TestEvaluateProviderSchedulingThreshold_GrokUsesOnlyHeaderQuotaWindow(t *testing.T) {
	t.Parallel()
	// Billing usage cannot trigger the pause; the grok_sched_* header window determines eligibility.
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	weeklyEnd := now.Add(3 * time.Hour)
	weeklyPct := 99.0
	headerUntil := now.Add(2 * time.Hour)
	provider := &Record{
		Platform: PlatformGrok,
		Extra: map[string]any{
			"grok_sched_utilization": 50.0, // below threshold
			"grok_sched_reset_at":    headerUntil.Format(time.RFC3339),
			GrokUsageBillingExtraKey: &usageview.BillingSummary{
				UsagePercent: &weeklyPct,
				PeriodEnd:    weeklyEnd.Format(time.RFC3339),
			},
		},
	}
	decision := EvaluateProviderSchedulingThreshold(provider, map[string]int{PlatformGrok: 90}, now)
	require.False(t, decision.ShouldPause, "high billing % alone must not pause under scheduling windows")

	provider.Extra["grok_sched_utilization"] = 95.0
	decision = EvaluateProviderSchedulingThreshold(provider, map[string]int{PlatformGrok: 90}, now)
	require.True(t, decision.ShouldPause)
	require.Equal(t, "grok", decision.Scope)
	require.Equal(t, "quota", decision.Window)
	require.NotNil(t, decision.Until)
	require.True(t, headerUntil.Equal(*decision.Until))
}
