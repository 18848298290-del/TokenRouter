package provider

import (
	"context"
	"fmt"
	"time"
)

// OpenAIUsageOptions supplies regular and shadow-provider quota probes.
type OpenAIUsageOptions struct {
	Probe  func(context.Context, *Record) (map[string]any, error)
	Shadow func(context.Context, int64, time.Time) (map[string]any, error)
}

func (s *OAuthUsageService) GetOpenAIUsage(ctx context.Context, provider *Record, force bool) (*UsageInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	now := s.options.Now()
	usage := &UsageInfo{UpdatedAt: &now}

	if provider == nil {
		return usage, nil
	}

	ApplyExtraToUsage(usage, provider.Extra, now, s.options.Now)

	if (force || ShouldRefreshOpenAICodexSnapshot(provider, usage, now)) && s.ShouldProbeOpenAICodexSnapshot(provider.ID, now, force) {
		if provider.IsShadow() {
			// Shadow probes use parent credentials, while the snapshot belongs to the shadow row.
			if s.options.OpenAI.Shadow != nil {
				if updates, err := s.options.OpenAI.Shadow(ctx, provider.ID, now); err == nil && len(updates) > 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					provider.Extra = MergeUsageExtra(provider.Extra, updates)
					s.PersistOpenAICodexProbeSnapshot(provider, updates)
					if usage.UpdatedAt == nil {
						usage.UpdatedAt = &now
					}
					ApplyExtraToUsage(usage, provider.Extra, now, s.options.Now)
				}
			}
		} else {
			if updates, err := s.ProbeOpenAICodexSnapshot(ctx, provider); err == nil && len(updates) > 0 {
				provider.Extra = MergeUsageExtra(provider.Extra, updates)
				if usage.UpdatedAt == nil {
					usage.UpdatedAt = &now
				}
				ApplyExtraToUsage(usage, provider.Extra, now, s.options.Now)
			}
		}
	}

	if s.stats == nil || s.stats.usageLogRepo == nil {
		return usage, nil
	}

	fiveHour, sevenDay := s.stats.GetWindowPair(ctx, provider.ID,
		CodexWindowStatsStart(usage.FiveHour, 5*time.Hour, now),
		CodexWindowStatsStart(usage.SevenDay, 7*24*time.Hour, now))
	if fiveHour != nil {
		if usage.FiveHour == nil {
			usage.FiveHour = &UsageProgress{Utilization: 0}
		}
		usage.FiveHour.WindowStats = fiveHour
	}

	if sevenDay != nil {
		if usage.SevenDay == nil {
			usage.SevenDay = &UsageProgress{Utilization: 0}
		}
		usage.SevenDay.WindowStats = sevenDay
	}

	return usage, nil
}

func ShouldRefreshOpenAICodexSnapshot(provider *Record, usage *UsageInfo, now time.Time) bool {
	if provider == nil {
		return false
	}
	if usage == nil {
		return true
	}
	if usage.FiveHour == nil || usage.SevenDay == nil {
		return true
	}
	if provider.IsRateLimited() {
		return true
	}
	return isOpenAICodexSnapshotStale(provider, now)
}

func isOpenAICodexSnapshotStale(provider *Record, now time.Time) bool {
	if provider == nil || !provider.IsOpenAIOAuth() {
		return false
	}
	// Regular providers refresh Codex usage through Responses WS and require that upstream capability.
	// Spark shadows read codex_bengalfox from /wham/usage and use codex_usage_updated_at for freshness.
	// ShouldProbeOpenAICodexSnapshot separately limits probe frequency for both paths.
	if !provider.IsShadow() && !provider.SupportsResponsesWS() {
		return false
	}
	if provider.Extra == nil {
		return true
	}
	raw, ok := provider.Extra["codex_usage_updated_at"]
	if !ok {
		return true
	}
	ts, err := ParseUsageTime(fmt.Sprint(raw))
	if err != nil {
		return true
	}
	return now.Sub(ts) >= OAuthUsageOpenAIProbeCacheTTL
}

func (s *OAuthUsageService) ShouldProbeOpenAICodexSnapshot(providerID int64, now time.Time, force ...bool) bool {
	if s == nil || s.cache == nil || providerID <= 0 {
		return true
	}
	forceProbe := len(force) > 0 && force[0]
	if !forceProbe {
		if cached, ok := s.cache.LoadOpenAIProbe(providerID); ok {
			if ts, ok := cached.(time.Time); ok && now.Sub(ts) < OAuthUsageOpenAIProbeCacheTTL {
				return false
			}
		}
	}
	s.cache.StoreOpenAIProbe(providerID, now)
	return true
}

// ProbeOpenAICodexSnapshot fetches display usage and schedules a best-effort snapshot write.
func (s *OAuthUsageService) ProbeOpenAICodexSnapshot(ctx context.Context, value *Record) (map[string]any, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	if s.options.OpenAI.Probe == nil {
		return nil, nil
	}
	updates, err := s.options.OpenAI.Probe(ctx, value)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(updates) > 0 {
		s.PersistOpenAICodexProbeSnapshot(value, updates)
	}
	return CloneValues(updates), nil
}

// PersistOpenAICodexProbeSnapshot registers a detached five-second write before dispatch so shutdown can track
// it.
func (s *OAuthUsageService) PersistOpenAICodexProbeSnapshot(value *Record, updates map[string]any) {
	if s == nil || s.providerRepo == nil || value == nil || value.ID <= 0 || len(updates) == 0 {
		return
	}
	version := ObserveUsageVersion(value)
	updates = CloneValues(updates)
	writer, ok := s.providerRepo.(UsageExtraWriter)
	if !ok {
		s.options.Warn("openai usage conditional writer is missing")
		return
	}
	ctx, finish, err := s.BeginDetached(context.Background(), 5*time.Second)
	if err != nil {
		return
	}
	go func() {
		defer finish()
		if ctx.Err() == nil {
			_, _ = writer.UpdateUsageExtraIfUnchanged(ctx, version, updates)
		}
	}()
}
