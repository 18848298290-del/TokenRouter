package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

func TestRateLimitServiceAdvancedSchedulerScoreSnapshotUsesSharedRuntimeStats(t *testing.T) {
	weights := policy.ScoreWeights{
		Priority:  1,
		ErrorRate: 2,
		TTFT:      3,
	}
	cfg := &config.Config{}
	cfg.Gateway.AdvancedScheduler.ScoreWeights = config.GatewayAdvancedSchedulerScoreWeights{
		Priority:  weights.Priority,
		ErrorRate: weights.ErrorRate,
		TTFT:      weights.TTFT,
	}
	shared := newScoreFixtureState()
	stats := shared.Feedback
	options := providerScoreOptions(nil, shared, nil, cfg)
	group := &routing.Group{ID: 71, SchedulerType: routing.GroupSchedulerTypeAdvanced}
	providers := []*provider.Record{
		{ID: 7101, Platform: capability.PlatformGemini, Priority: 1},
		{ID: 7102, Platform: capability.PlatformGemini, Priority: 1},
	}

	// With no samples, both shared and nil statistics use a zero error rate.
	neutral := options.Score(context.Background(), (*accessview.GroupConfig)(group), providers, nil)
	neutralCore, _ := scheduler.ScoreCandidates(scoreFixtureProviders(providers), nil, nil, weights, scheduler.ScoreInput{}, time.Now())
	require.Len(t, neutralCore, 2)
	for index, provider := range providers {
		require.InDelta(t, 4.5, neutral[provider.ID].BaseScore, 0.000001)
		require.InDelta(t, neutralCore[index].Score, neutral[provider.ID].BaseScore, 0.000001)
	}

	slowTTFT := 3000
	fastTTFT := 1000
	stats.Report(providers[0].ID, false, &slowTTFT)
	stats.Report(providers[1].ID, true, &fastTTFT)

	observed := options.Score(context.Background(), (*accessview.GroupConfig)(group), providers, nil)
	core, _ := scheduler.ScoreCandidates(scoreFixtureProviders(providers), nil, stats, weights, scheduler.ScoreInput{}, time.Now())
	require.Len(t, core, 2)
	for index, provider := range providers {
		require.InDelta(t, core[index].Score, observed[provider.ID].BaseScore, 0.000001)
	}
	// The first failure moves error-rate EWMA from 0 to 0.2, reducing its score factor to 0.8.
	// The first success leaves that factor at 1.
	require.InDelta(t, 2.6, observed[providers[0].ID].BaseScore, 0.000001)
	require.InDelta(t, 6.0, observed[providers[1].ID].BaseScore, 0.000001)
	require.Less(t, observed[providers[0].ID].BaseScore, neutral[providers[0].ID].BaseScore)
	require.Greater(t, observed[providers[1].ID].BaseScore, neutral[providers[1].ID].BaseScore)
}

func TestAdvancedSchedulerScoreSnapshotUsesPreviousResponseOnlyForOpenAI(t *testing.T) {
	stickyWeighted := true
	previousWeight := 11.0
	sessionWeight := 7.0
	platforms := []string{
		capability.PlatformOpenAI,
		capability.PlatformAnthropic,
		capability.PlatformGemini,
		capability.PlatformAntigravity,
		capability.PlatformQoder,
		capability.PlatformGrok,
	}

	for index, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			group := &routing.Group{
				ID:            int64(7200 + index),
				SchedulerType: routing.GroupSchedulerTypeAdvanced,
				AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
					StickyWeightedEnabled:  &stickyWeighted,
					WeightPreviousResponse: &previousWeight,
					WeightSessionSticky:    &sessionWeight,
				},
			}
			value := &provider.Record{ID: int64(7300 + index), Platform: platform, Priority: 1}
			snapshot := providerScoreOptions(nil, newScoreFixtureState(), nil, nil).Score(context.Background(), (*accessview.GroupConfig)(group), []*provider.Record{value}, nil)[value.ID]

			expectedBonus := sessionWeight
			if platform == capability.PlatformOpenAI {
				expectedBonus += previousWeight
			}
			require.True(t, snapshot.StickyWeightedEnabled)
			require.False(t, snapshot.StickyScoreInfinity)
			require.InDelta(t, snapshot.BaseScore+expectedBonus, snapshot.StickyScore, 0.000001)
		})
	}
}

func TestAdvancedSchedulerScoreSnapshotKeepsHardStickyInfinity(t *testing.T) {
	stickyWeighted := false
	group := &routing.Group{
		ID:            7401,
		SchedulerType: routing.GroupSchedulerTypeAdvanced,
		AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
			StickyWeightedEnabled: &stickyWeighted,
		},
	}
	value := &provider.Record{ID: 7402, Platform: capability.PlatformGemini, Priority: 1}
	snapshot := providerScoreOptions(nil, newScoreFixtureState(), nil, nil).Score(context.Background(), (*accessview.GroupConfig)(group), []*provider.Record{value}, nil)[value.ID]

	require.False(t, snapshot.StickyWeightedEnabled)
	require.True(t, snapshot.StickyScoreInfinity)
	require.Zero(t, snapshot.StickyScore)
}

// newScoreFixtureState isolates each test; production consumers share one state instance.
func newScoreFixtureState() *schedulerSharedState {
	return &schedulerSharedState{Settings: scheduler.NewSettingsRuntime(scheduler.Diagnostics{}), Feedback: scheduler.NewRuntimeStats(time.Now)}
}

func scoreFixtureProviders(values []*provider.Record) []*scheduler.ScoreProvider {
	result := make([]*scheduler.ScoreProvider, len(values))
	for i, v := range values {
		result[i] = &scheduler.ScoreProvider{ID: v.ID, Platform: v.Platform, Priority: v.Priority, SessionWindowEnd: v.SessionWindowEnd}
	}
	return result
}
