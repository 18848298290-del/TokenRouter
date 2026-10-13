package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider/usageview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Usage-window pause decisions depend on platform scale, trusted observation
// identity, reset deadlines, and provider overrides. These fixtures exercise
// the evaluator without applying health state, separating provider-wide
// Anthropic windows from Fable scope and Grok header quota from official
// billing observations.

// TestCNProviderThresholdCandidates attaches two monitor limits to a Coding
// Plan provider. It verifies candidate count, omission of usage-free limits,
// and rejection after changing the credential identity.
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

	partial := cnCodingTestProvider(capability.PlatformKimi)
	attachCNMonitorLimits(partial, observed, []UpstreamUsageLimit{{Name: "5h", ResetAt: &reset5h}})
	require.Empty(t, filterNil(CNProviderThresholdCandidates(partial, capability.PlatformKimi)))

	// Keep the stored observation unchanged while replacing its API Key so the
	// validator must reject the previous query identity before creating limits.
	provider.Credentials["api_key"] = "sk-changed"
	require.Empty(t, CNProviderThresholdCandidates(provider, capability.PlatformKimi))
}

// filterNil returns nonnil candidate pointers in their original order,
// preserving shared candidate data. Empty usable input returns a nil slice
// for assertions on omitted monitoring limits.
func filterNil(cands []*SchedulingThresholdCandidate) []*SchedulingThresholdCandidate {
	var out []*SchedulingThresholdCandidate
	for _, c := range cands {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

// TestEvaluateProviderSchedulingThreshold_KimiCodingPlan supplies one reached
// five-hour threshold and a lower weekly observation. It verifies the Kimi
// decision's percentage, window, platform, and future reset deadline.
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

// TestEvaluateProviderSchedulingThreshold_CNWindowResetSkipped supplies an
// expired high-usage observation and a future low-usage observation
// separately. It verifies neither case selects a provider pause.
func TestEvaluateProviderSchedulingThreshold_CNWindowResetSkipped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	expiredUsed := 99.0
	expiredReset := now.Add(-time.Hour)
	expired := cnCodingTestProvider(capability.PlatformZhipu)
	attachCNMonitorLimits(expired, now, []UpstreamUsageLimit{{Name: "5h", Used: &expiredUsed, ResetAt: &expiredReset}})
	require.False(t, EvaluateProviderSchedulingThreshold(expired, map[string]int{capability.PlatformZhipu: 80}, now).ShouldPause)

	lowUsed := 20.0
	lowReset := now.Add(3 * time.Hour)
	low := cnCodingTestProvider(capability.PlatformZhipu)
	attachCNMonitorLimits(low, now, []UpstreamUsageLimit{{Name: "5h", Used: &lowUsed, ResetAt: &lowReset}})
	require.False(t, EvaluateProviderSchedulingThreshold(low, map[string]int{capability.PlatformZhipu: 80}, now).ShouldPause)
}

// TestEvaluateProviderSchedulingThreshold_OpenAIChoosesLatestResetWindow
// supplies two reached Codex window percentages with different future
// deadlines. It verifies the later weekly reset wins together with that
// window's percentage and empty scope.
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

// TestEvaluateProviderSchedulingThreshold_OpenAIIgnoresMismatchedCodexSnapshotIdentity
// gives an OAuth record conflicting credential and observation account
// fields. It verifies high weekly usage from that snapshot produces no pause.
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

// TestEvaluateProviderSchedulingThreshold_AnthropicIgnoresExpiredFiveHourWindow
// combines an expired high five-hour ratio with a future reached weekly
// ratio. It verifies the weekly observation supplies the selected percentage
// and reset.
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

// TestEvaluateAnthropicFableSchedulingThreshold_UsesProviderOverrideWithoutPausingProvider
// lowers the credential threshold below Fable usage while ordinary weekly
// usage stays lower. It verifies provider-wide evaluation remains clear and
// Fable returns its model scope and deadline.
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

// TestEvaluateProviderSchedulingThreshold_OpenAIPreservesPercentageSemantics
// first supplies Codex usage of one percentage point, then ninety-one. It
// verifies the former stays below threshold and the latter selects a pause
// with unchanged percentage scale.
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

// TestEvaluateProviderSchedulingThreshold_OpenAISkipsStaleSnapshot supplies
// exhausted usage observed exactly two hours earlier with a future reset. It
// verifies reaching the staleness cutoff suppresses the pause.
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

// TestEvaluateProviderSchedulingThreshold_OpenAISkipsResetWindow supplies
// freshly observed exhausted five-hour usage whose reset elapsed one second
// earlier. It verifies that the window cannot select a provider pause.
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

// TestEvaluateProviderSchedulingThreshold_OpenAIPausesFreshExhaustedSnapshot
// supplies recent fully used five-hour data with a future reset. It verifies
// the matching window, percentage, and proposed deadline.
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

// TestEvaluateProviderSchedulingThreshold_OpenAIPausesFreshExhaustedSevenDayWindow
// supplies recent weekly usage above the configured threshold. It verifies
// the weekly window's percentage and future reset form the pause decision.
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

// TestEvaluateProviderSchedulingThreshold_AnthropicPreservesFractionalUtilizationSemantics
// supplies a floating ratio of 0.92 with a future session end. It verifies
// the evaluator compares ninety-two percentage points and selects a pause.
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

// TestEvaluateProviderSchedulingThreshold_ProviderOverrideCanLowerOpenAIThreshold
// supplies usage between the lower credential override and higher platform
// value. It verifies the override wins and its reached window supplies the
// selected deadline.
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

// TestEvaluateProviderSchedulingThreshold_ProviderOverrideHundredDisablesOpenAI
// supplies a credential threshold of one hundred despite a lower platform
// value. It verifies the resolved threshold disables the pause.
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

// TestEvaluateProviderSchedulingThreshold_ProviderOverrideRoundsDecimalThreshold
// supplies a credential override of 75.5 percentage points with reached
// usage. It verifies the override rounds to seventy-six before deciding the
// pause and deadline.
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

// TestEvaluateProviderSchedulingThreshold_UnsupportedPlatformsDoNotPause
// gives Gemini, Kiro, and Antigravity high observations and low credential
// overrides. It verifies each unsupported platform returns no pause and no
// resolved threshold.
func TestEvaluateProviderSchedulingThreshold_UnsupportedPlatformsDoNotPause(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string         // table case description
		platform  string         // unsupported provider platform
		threshold int            // configured platform percentage
		extra     map[string]any // stored usage observation
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

// TestEvaluateProviderSchedulingThreshold_GrokUsesConfiguredThresholds
// supplies a reached rolling-header percentage and future deadline. It
// verifies the platform threshold, Grok scope, observed percentage, and reset
// are retained.
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

// TestEvaluateProviderSchedulingThreshold_GrokUsesOnlyHeaderQuotaWindow
// combines high official billing usage with a low rolling-header observation,
// then raises the header value. It verifies only the header window selects
// the pause and deadline.
func TestEvaluateProviderSchedulingThreshold_GrokUsesOnlyHeaderQuotaWindow(t *testing.T) {
	t.Parallel()
	// Keep official billing high while the header window is below threshold,
	// making the first decision distinguish the two independent observations.
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	weeklyEnd := now.Add(3 * time.Hour)
	weeklyPct := 99.0
	headerUntil := now.Add(2 * time.Hour)
	provider := &Record{
		Platform: PlatformGrok,
		Extra: map[string]any{
			"grok_sched_utilization": 50.0,
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
