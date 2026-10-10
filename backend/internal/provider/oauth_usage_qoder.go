package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	QoderUsageQuotaSnapshotExtraKey  = "qoder_quota_snapshot"
	QoderUsageQuotaUpdatedAtExtraKey = "qoder_quota_updated_at"
)

// qoderUsageFlightResult 让同 key 的等待者核对本轮身份，不共享可变结果。
type qoderUsageFlightResult struct {
	Usage    *UsageInfo
	Identity string
}

// QoderUsageOptions 只提供供应商请求、响应归一化和故障诊断；缓存与提供商写入由核心拥有。
type QoderUsageOptions struct {
	Fetch   func(context.Context, *Record, func() time.Time) (*UsageInfo, error)
	Degrade func(error, *Record, time.Time) *UsageInfo
	Enrich  func(*UsageInfo, *Record)
}

func (s *OAuthUsageService) GetQoderUsage(ctx context.Context, provider *Record, force bool) (*UsageInfo, error) {
	now := s.options.Now()
	if provider == nil {
		return &UsageInfo{UpdatedAt: &now}, nil
	}
	if s.cache == nil {
		s.cache = NewOAuthUsageCache()
	}

	if !force {
		if cache, ok := s.cache.LoadQoder(provider.ID); ok && QoderUsageCacheUsable(provider, cache, s.options.Now()) {
			return cache.UsageInfo, nil
		}
	}

	identity := UsageCacheIdentity(provider)
	flightKey := fmt.Sprintf("qoder-usage:%d", provider.ID)
	result, flightErr, _ := s.cache.DoQoder(flightKey, func() (any, error) {
		if !force {
			if cache, ok := s.cache.LoadQoder(provider.ID); ok && QoderUsageCacheUsable(provider, cache, s.options.Now()) {
				return qoderUsageFlightResult{cache.UsageInfo, cache.Identity}, nil
			}
		}

		fetchCtx, cancel, beginErr := s.BeginDetached(ctx, 30*time.Second)
		if beginErr != nil {
			return nil, beginErr
		}
		defer cancel()

		usage, err := s.options.Qoder.Fetch(fetchCtx, provider, s.options.Now)
		if fetchCtx.Err() == context.Canceled {
			return nil, fetchCtx.Err()
		}
		if err != nil {
			degraded := s.options.Qoder.Degrade(err, provider, s.options.Now())
			s.options.Qoder.Enrich(degraded, provider)
			s.cache.StoreQoder(provider.ID, &OAuthQoderUsageCache{Identity: identity, UsageInfo: CloneUsageInfo(degraded), Timestamp: s.options.Now()})
			return qoderUsageFlightResult{CloneUsageInfo(degraded), identity}, nil
		}

		s.options.Qoder.Enrich(usage, provider)
		version := ObserveUsageVersion(provider)
		applied, persistErr := s.persistQoderQuotaSnapshot(fetchCtx, version, usage.QoderQuota)
		if persistErr == nil && !applied {
			return nil, ErrUsageObservationChanged
		}
		s.applyQoderQuotaSchedulingSignal(fetchCtx, provider, usage.QoderQuota)
		s.cache.StoreQoder(provider.ID, &OAuthQoderUsageCache{Identity: identity, UsageInfo: CloneUsageInfo(usage), Timestamp: s.options.Now()})
		return qoderUsageFlightResult{CloneUsageInfo(usage), identity}, nil
	})
	if flightErr != nil {
		return nil, flightErr
	}
	completed, ok := result.(qoderUsageFlightResult)
	if !ok || completed.Usage == nil {
		return &UsageInfo{UpdatedAt: &now}, nil
	}
	if completed.Identity != identity {
		return nil, ErrUsageObservationChanged
	}
	return CloneUsageInfo(completed.Usage), nil
}

func (s *OAuthUsageService) persistQoderQuotaSnapshot(ctx context.Context, version UsageObservationVersion, quota *QoderQuotaInfo) (bool, error) {
	if s == nil || s.providerRepo == nil || quota == nil {
		return true, nil
	}
	raw, err := json.Marshal(quota)
	if err != nil {
		return true, nil
	}
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return true, nil
	}
	writer, ok := s.providerRepo.(UsageObservationWriter)
	if !ok {
		return false, ErrUsageObservationWriterMissing
	}
	applied, err := writer.UpdateUsageExtraIfUnchanged(ctx, version, map[string]any{
		QoderUsageQuotaSnapshotExtraKey:  snapshot,
		QoderUsageQuotaUpdatedAtExtraKey: s.options.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		s.options.Warn("failed to persist qoder quota snapshot", "provider_id", version.ID, "error", err)
	}
	return applied, err
}

func (s *OAuthUsageService) applyQoderQuotaSchedulingSignal(ctx context.Context, provider *Record, quota *QoderQuotaInfo) {
	if s == nil || s.providerRepo == nil || provider == nil {
		return
	}
	writer, ok := s.providerRepo.(UsageObservationWriter)
	if !ok {
		s.options.Warn("qoder usage conditional writer is missing")
		return
	}
	version := ObserveUsageVersion(provider)
	now := s.options.Now()
	resetAt, ok := QoderQuotaRateLimitResetAt(quota, now)
	if !ok {
		if QoderQuotaShouldClearRateLimit(provider, quota, now) {
			if _, err := writer.ClearUsageRateLimitIfUnchanged(ctx, version); err != nil {
				s.options.Warn("failed to clear qoder quota rate limit", "provider_id", provider.ID, "error", err)
			}
		}
		return
	}
	if _, err := writer.SetUsageRateLimitIfUnchanged(ctx, version, resetAt); err != nil {
		s.options.Warn("failed to apply qoder quota rate limit", "provider_id", provider.ID, "error", err)
	}
}
