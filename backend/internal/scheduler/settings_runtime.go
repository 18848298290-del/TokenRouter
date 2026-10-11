package scheduler

import (
	"context"
	"maps"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// 设置键对应数据库中的运行参数记录。
const (
	SettingKeyAdvancedSchedulerStickyWeightedEnabled       = "advanced_scheduler_sticky_weighted_enabled"
	SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled = "advanced_scheduler_subscription_priority_enabled"
	SettingKeyAdvancedSchedulerEWMAErrorRateAlpha          = "advanced_scheduler_ewma_error_rate_alpha"
	SettingKeyAdvancedSchedulerEWMATTFTAlpha               = "advanced_scheduler_ewma_ttft_alpha"
	SettingKeyAdvancedSchedulerStickyEscapeEnabled         = "advanced_scheduler_sticky_escape_enabled"
	SettingKeyAdvancedSchedulerStickyEscapeTTFTMs          = "advanced_scheduler_sticky_escape_ttft_ms"
	SettingKeyAdvancedSchedulerStickyEscapeErrorRate       = "advanced_scheduler_sticky_escape_error_rate"
	SettingKeyAdvancedSchedulerLBTopK                      = "advanced_scheduler_lb_top_k"
	SettingKeyAdvancedSchedulerWeightPriority              = "advanced_scheduler_weight_priority"
	SettingKeyAdvancedSchedulerWeightLoad                  = "advanced_scheduler_weight_load"
	SettingKeyAdvancedSchedulerWeightQueue                 = "advanced_scheduler_weight_queue"
	SettingKeyAdvancedSchedulerWeightErrorRate             = "advanced_scheduler_weight_error_rate"
	SettingKeyAdvancedSchedulerWeightTTFT                  = "advanced_scheduler_weight_ttft"
	SettingKeyAdvancedSchedulerWeightReset                 = "advanced_scheduler_weight_reset"
	SettingKeyAdvancedSchedulerWeightQuotaHeadroom         = "advanced_scheduler_weight_quota_headroom"
	SettingKeyAdvancedSchedulerWeightPreviousResponse      = "advanced_scheduler_weight_previous_response"
	SettingKeyAdvancedSchedulerWeightSessionSticky         = "advanced_scheduler_weight_session_sticky"
	advancedSchedulerSettingCacheTTL                       = 5 * time.Second
	advancedSchedulerSettingDBTimeout                      = 2 * time.Second
)

// RuntimeSettingSource 提供批量读取和逐键读取，批量失败时按键重试。
type RuntimeSettingSource interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
	GetValue(context.Context, string) (string, error)
}

// SettingsRuntime 缓存带 TTL 的设置快照，并用 singleflight 合并并发读取。每次请求合并参数。
type SettingsRuntime struct {
	cache       atomic.Value
	sf          singleflight.Group
	diagnostics Diagnostics
}

type cachedAdvancedSchedulerSetting struct {
	StickyWeightedEnabled       bool
	SubscriptionPriorityEnabled bool
	LbTopKOverride              int
	WeightOverrides             map[string]float64
	EwmaErrorRateAlpha          float64
	EwmaErrorRateAlphaSet       bool
	EwmaTTFTAlpha               float64
	EwmaTTFTAlphaSet            bool
	StickyEscapeEnabled         bool
	StickyEscapeEnabledSet      bool
	StickyEscapeTTFTMs          float64
	StickyEscapeTTFTMsSet       bool
	StickyEscapeErrorRate       float64
	StickyEscapeErrorRateSet    bool
	StickyEscape                policy.StickyEscapeConfig
	expiresAt                   int64
}

// WeightOverrideSpec 保留管理与校验使用的稳定顺序。
type WeightOverrideSpec struct {
	Key  string
	Name string
}

func NewSettingsRuntime(diagnostics Diagnostics) *SettingsRuntime {
	return &SettingsRuntime{diagnostics: diagnostics}
}

func (s *SettingsRuntime) Load(ctx context.Context, repo RuntimeSettingSource, defaults policy.RuntimeSettings) policy.RuntimeSettings {
	if cached, ok := s.cache.Load().(*cachedAdvancedSchedulerSetting); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return policy.RuntimeSettings{
				StickyWeightedEnabled:       cached.StickyWeightedEnabled,
				SubscriptionPriorityEnabled: cached.SubscriptionPriorityEnabled,
				LbTopKOverride:              cached.LbTopKOverride,
				WeightOverrides:             CloneAdvancedSchedulerWeightOverrides(cached.WeightOverrides),
				EwmaErrorRateAlpha:          cached.EwmaErrorRateAlpha,
				EwmaErrorRateAlphaSet:       cached.EwmaErrorRateAlphaSet,
				EwmaTTFTAlpha:               cached.EwmaTTFTAlpha,
				EwmaTTFTAlphaSet:            cached.EwmaTTFTAlphaSet,
				StickyEscapeEnabled:         cached.StickyEscapeEnabled,
				StickyEscapeEnabledSet:      cached.StickyEscapeEnabledSet,
				StickyEscapeTTFTMs:          cached.StickyEscapeTTFTMs,
				StickyEscapeTTFTMsSet:       cached.StickyEscapeTTFTMsSet,
				StickyEscapeErrorRate:       cached.StickyEscapeErrorRate,
				StickyEscapeErrorRateSet:    cached.StickyEscapeErrorRateSet,
				StickyEscape:                policy.StickyEscapeConfig{Enabled: cached.StickyEscapeEnabled, TtftMs: cached.StickyEscapeTTFTMs, ErrorRate: cached.StickyEscapeErrorRate},
			}
		}
	}

	result, _, _ := s.sf.Do("advanced_scheduler_settings", func() (any, error) {
		if cached, ok := s.cache.Load().(*cachedAdvancedSchedulerSetting); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return policy.RuntimeSettings{
					StickyWeightedEnabled:       cached.StickyWeightedEnabled,
					SubscriptionPriorityEnabled: cached.SubscriptionPriorityEnabled,
					LbTopKOverride:              cached.LbTopKOverride,
					WeightOverrides:             CloneAdvancedSchedulerWeightOverrides(cached.WeightOverrides),
					EwmaErrorRateAlpha:          cached.EwmaErrorRateAlpha,
					EwmaErrorRateAlphaSet:       cached.EwmaErrorRateAlphaSet,
					EwmaTTFTAlpha:               cached.EwmaTTFTAlpha,
					EwmaTTFTAlphaSet:            cached.EwmaTTFTAlphaSet,
					StickyEscapeEnabled:         cached.StickyEscapeEnabled,
					StickyEscapeEnabledSet:      cached.StickyEscapeEnabledSet,
					StickyEscapeTTFTMs:          cached.StickyEscapeTTFTMs,
					StickyEscapeTTFTMsSet:       cached.StickyEscapeTTFTMsSet,
					StickyEscapeErrorRate:       cached.StickyEscapeErrorRate,
					StickyEscapeErrorRateSet:    cached.StickyEscapeErrorRateSet,
					StickyEscape:                policy.StickyEscapeConfig{Enabled: cached.StickyEscapeEnabled, TtftMs: cached.StickyEscapeTTFTMs, ErrorRate: cached.StickyEscapeErrorRate},
				}, nil
			}
		}

		stickyWeightedEnabled := false
		subscriptionPriorityEnabled := false
		lbTopKOverride := 0
		weightOverrides := map[string]float64{}
		ewmaErrorRateAlpha := defaults.EwmaErrorRateAlpha
		ewmaErrorRateAlphaSet := false
		ewmaTTFTAlpha := defaults.EwmaTTFTAlpha
		ewmaTTFTAlphaSet := false
		stickyEscapeEnabled := defaults.StickyEscape.Enabled
		stickyEscapeEnabledSet := false
		stickyEscapeTTFTMs := defaults.StickyEscape.TtftMs
		stickyEscapeTTFTMsSet := false
		stickyEscapeErrorRate := defaults.StickyEscape.ErrorRate
		stickyEscapeErrorRateSet := false
		if repo != nil {
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), advancedSchedulerSettingDBTimeout)
			defer cancel()

			values, err := repo.GetMultiple(dbCtx, AdvancedSchedulerRuntimeSettingKeys())
			if err != nil {
				// 批量读取失败时逐键读取全部设置，包括 TopK 和权重。
				// 降级结果缓存一个 TTL，日志记录批量读取失败的原因。
				s.diagnostics.event("warn", "advanced_scheduler_settings_batch_load_failed", "error", err)
				values = make(map[string]string)
				for _, key := range AdvancedSchedulerRuntimeSettingKeys() {
					if value, valueErr := repo.GetValue(dbCtx, key); valueErr == nil {
						values[key] = value
					}
				}
			}
			stickyWeightedEnabled = strings.EqualFold(strings.TrimSpace(values[SettingKeyAdvancedSchedulerStickyWeightedEnabled]), "true")
			subscriptionPriorityEnabled = strings.EqualFold(strings.TrimSpace(values[SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled]), "true")
			lbTopKOverride = ParsePositiveIntOverride(values[SettingKeyAdvancedSchedulerLBTopK])
			weightOverrides = ParseAdvancedSchedulerWeightOverrides(values)
			ewmaErrorRateAlpha, ewmaErrorRateAlphaSet = ParseAdvancedSchedulerAlphaOverride(values[SettingKeyAdvancedSchedulerEWMAErrorRateAlpha], defaults.EwmaErrorRateAlpha)
			ewmaTTFTAlpha, ewmaTTFTAlphaSet = ParseAdvancedSchedulerAlphaOverride(values[SettingKeyAdvancedSchedulerEWMATTFTAlpha], defaults.EwmaTTFTAlpha)
			stickyEscapeEnabled, stickyEscapeEnabledSet = ParseAdvancedSchedulerBoolOverride(values[SettingKeyAdvancedSchedulerStickyEscapeEnabled], defaults.StickyEscape.Enabled)
			stickyEscapeTTFTMs, stickyEscapeTTFTMsSet = ParseAdvancedSchedulerPositiveFloatOverride(values[SettingKeyAdvancedSchedulerStickyEscapeTTFTMs], defaults.StickyEscape.TtftMs)
			stickyEscapeErrorRate, stickyEscapeErrorRateSet = ParseAdvancedSchedulerRateOverride(values[SettingKeyAdvancedSchedulerStickyEscapeErrorRate], defaults.StickyEscape.ErrorRate)
		}

		s.cache.Store(&cachedAdvancedSchedulerSetting{
			StickyWeightedEnabled:       stickyWeightedEnabled,
			SubscriptionPriorityEnabled: subscriptionPriorityEnabled,
			LbTopKOverride:              lbTopKOverride,
			WeightOverrides:             CloneAdvancedSchedulerWeightOverrides(weightOverrides),
			EwmaErrorRateAlpha:          ewmaErrorRateAlpha,
			EwmaErrorRateAlphaSet:       ewmaErrorRateAlphaSet,
			EwmaTTFTAlpha:               ewmaTTFTAlpha,
			EwmaTTFTAlphaSet:            ewmaTTFTAlphaSet,
			StickyEscapeEnabled:         stickyEscapeEnabled,
			StickyEscapeEnabledSet:      stickyEscapeEnabledSet,
			StickyEscapeTTFTMs:          stickyEscapeTTFTMs,
			StickyEscapeTTFTMsSet:       stickyEscapeTTFTMsSet,
			StickyEscapeErrorRate:       stickyEscapeErrorRate,
			StickyEscapeErrorRateSet:    stickyEscapeErrorRateSet,
			StickyEscape:                policy.StickyEscapeConfig{Enabled: stickyEscapeEnabled, TtftMs: stickyEscapeTTFTMs, ErrorRate: stickyEscapeErrorRate},
			expiresAt:                   time.Now().Add(advancedSchedulerSettingCacheTTL).UnixNano(),
		})
		return policy.RuntimeSettings{
			StickyWeightedEnabled:       stickyWeightedEnabled,
			SubscriptionPriorityEnabled: subscriptionPriorityEnabled,
			LbTopKOverride:              lbTopKOverride,
			WeightOverrides:             weightOverrides,
			EwmaErrorRateAlpha:          ewmaErrorRateAlpha,
			EwmaErrorRateAlphaSet:       ewmaErrorRateAlphaSet,
			EwmaTTFTAlpha:               ewmaTTFTAlpha,
			EwmaTTFTAlphaSet:            ewmaTTFTAlphaSet,
			StickyEscapeEnabled:         stickyEscapeEnabled,
			StickyEscapeEnabledSet:      stickyEscapeEnabledSet,
			StickyEscapeTTFTMs:          stickyEscapeTTFTMs,
			StickyEscapeTTFTMsSet:       stickyEscapeTTFTMsSet,
			StickyEscapeErrorRate:       stickyEscapeErrorRate,
			StickyEscapeErrorRateSet:    stickyEscapeErrorRateSet,
			StickyEscape:                policy.StickyEscapeConfig{Enabled: stickyEscapeEnabled, TtftMs: stickyEscapeTTFTMs, ErrorRate: stickyEscapeErrorRate},
		}, nil
	})

	settings, _ := result.(policy.RuntimeSettings)
	// 每个请求复制一份权重映射后再应用覆盖值。
	settings.WeightOverrides = CloneAdvancedSchedulerWeightOverrides(settings.WeightOverrides)
	return settings
}

func ParseAdvancedSchedulerAlphaOverride(raw string, fallback float64) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback, false
	}
	return value, true
}

func ParseAdvancedSchedulerBoolOverride(raw string, fallback bool) (bool, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback, false
	}
	return value, true
}

func ParseAdvancedSchedulerPositiveFloatOverride(raw string, fallback float64) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback, false
	}
	return value, true
}

func ParseAdvancedSchedulerRateOverride(raw string, fallback float64) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback, false
	}
	return value, true
}

func AdvancedSchedulerRuntimeSettingKeys() []string {
	keys := []string{
		SettingKeyAdvancedSchedulerStickyWeightedEnabled,
		SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled,
		SettingKeyAdvancedSchedulerLBTopK,
		SettingKeyAdvancedSchedulerEWMAErrorRateAlpha,
		SettingKeyAdvancedSchedulerEWMATTFTAlpha,
		SettingKeyAdvancedSchedulerStickyEscapeEnabled,
		SettingKeyAdvancedSchedulerStickyEscapeTTFTMs,
		SettingKeyAdvancedSchedulerStickyEscapeErrorRate,
	}
	for _, spec := range AdvancedSchedulerWeightOverrideSpecs() {
		keys = append(keys, spec.Key)
	}
	return keys
}

func AdvancedSchedulerWeightOverrideSpecs() []WeightOverrideSpec {
	return []WeightOverrideSpec{
		{Key: SettingKeyAdvancedSchedulerWeightPriority, Name: "priority"},
		{Key: SettingKeyAdvancedSchedulerWeightLoad, Name: "load"},
		{Key: SettingKeyAdvancedSchedulerWeightQueue, Name: "queue"},
		{Key: SettingKeyAdvancedSchedulerWeightErrorRate, Name: "error_rate"},
		{Key: SettingKeyAdvancedSchedulerWeightTTFT, Name: "ttft"},
		{Key: SettingKeyAdvancedSchedulerWeightReset, Name: "reset"},
		{Key: SettingKeyAdvancedSchedulerWeightQuotaHeadroom, Name: "quota_headroom"},
		{Key: SettingKeyAdvancedSchedulerWeightPreviousResponse, Name: "previous_response"},
		{Key: SettingKeyAdvancedSchedulerWeightSessionSticky, Name: "session_sticky"},
	}
}

func ParsePositiveIntOverride(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func ParseAdvancedSchedulerWeightOverrides(values map[string]string) map[string]float64 {
	overrides := map[string]float64{}
	for _, spec := range AdvancedSchedulerWeightOverrideSpecs() {
		raw := strings.TrimSpace(values[spec.Key])
		if raw == "" {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		overrides[spec.Name] = value
	}
	return overrides
}

func CloneAdvancedSchedulerWeightOverrides(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	maps.Copy(out, in)
	return out
}

// Store 替换缓存值并更新 TTL，调用方在设置写入成功后刷新缓存时调用。
func (s *SettingsRuntime) Store(value policy.RuntimeSettings) {
	s.sf.Forget("advanced_scheduler_settings")
	s.cache.Store(&cachedAdvancedSchedulerSetting{
		StickyWeightedEnabled:       value.StickyWeightedEnabled,
		SubscriptionPriorityEnabled: value.SubscriptionPriorityEnabled,
		LbTopKOverride:              value.LbTopKOverride,
		WeightOverrides:             CloneAdvancedSchedulerWeightOverrides(value.WeightOverrides),
		EwmaErrorRateAlpha:          value.EwmaErrorRateAlpha,
		EwmaErrorRateAlphaSet:       value.EwmaErrorRateAlphaSet,
		EwmaTTFTAlpha:               value.EwmaTTFTAlpha,
		EwmaTTFTAlphaSet:            value.EwmaTTFTAlphaSet,
		StickyEscapeEnabled:         value.StickyEscapeEnabled,
		StickyEscapeEnabledSet:      value.StickyEscapeEnabledSet,
		StickyEscapeTTFTMs:          value.StickyEscapeTTFTMs,
		StickyEscapeTTFTMsSet:       value.StickyEscapeTTFTMsSet,
		StickyEscapeErrorRate:       value.StickyEscapeErrorRate,
		StickyEscapeErrorRateSet:    value.StickyEscapeErrorRateSet,
		StickyEscape:                value.StickyEscape,
		expiresAt:                   time.Now().Add(advancedSchedulerSettingCacheTTL).UnixNano(),
	})
}
