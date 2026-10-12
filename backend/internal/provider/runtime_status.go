package provider

import (
	"context"
	"time"
)

// RuntimeStatusOptions supplies shared concurrency, session, RPM, and usage readers.
type RuntimeStatusOptions struct {
	RPMBatch      func(context.Context, []int64) (map[int64]int, error)
	Now           func() time.Time
	QuotaSettings func(context.Context) QuotaAutoPauseSettings
	Concurrency   func(context.Context, []int64) (map[int64]int, error)
	WindowCost    func(context.Context, int64, time.Time) (*float64, error)
	Sessions      func(context.Context, []int64, map[int64]time.Duration) (map[int64]int, error)
	RPM           func(context.Context, int64) (int, error)
}
type RuntimeStatus struct {
	SchedulerScore             *ProviderSchedulerScore
	SchedulerScores            []ProviderSchedulerGroupScore
	Record                     *Record
	CurrentConcurrency         int
	CurrentWindowCost          *float64
	ActiveSessions, CurrentRPM *int
}

// RuntimeStatusReader builds display state for one provider using a cloned record.
type RuntimeStatusReader struct{ options RuntimeStatusOptions }

func NewRuntimeStatusReader(options RuntimeStatusOptions) *RuntimeStatusReader {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &RuntimeStatusReader{options: options}
}

func (s *RuntimeStatusReader) Read(ctx context.Context, value *Record) RuntimeStatus {
	v := CloneRecord(value)
	out := RuntimeStatus{Record: v}
	if v == nil {
		return out
	}
	s.applyQuotaAutoPauseState(ctx, v)
	if s.options.Concurrency != nil {
		if counts, err := s.options.Concurrency(ctx, []int64{v.ID}); err == nil {
			out.CurrentConcurrency = counts[v.ID]
		}
	}
	if v.IsAnthropicOAuthOrSetupToken() {
		cfg := RuntimeConfig{Extra: v.Extra, Concurrency: v.Concurrency, SessionWindowStart: v.SessionWindowStart, SessionWindowEnd: v.SessionWindowEnd}
		if s.options.WindowCost != nil && cfg.GetWindowCostLimit() > 0 {
			if cost, err := s.options.WindowCost(ctx, v.ID, cfg.GetCurrentWindowStartTime(s.options.Now())); err == nil {
				out.CurrentWindowCost = cost
			}
		}
		if s.options.Sessions != nil && cfg.GetMaxSessions() > 0 {
			idle := map[int64]time.Duration{v.ID: time.Duration(cfg.GetSessionIdleTimeoutMinutes()) * time.Minute}
			if counts, err := s.options.Sessions(ctx, []int64{v.ID}, idle); err == nil {
				if n, ok := counts[v.ID]; ok {
					out.ActiveSessions = &n
				}
			}
		}
		if s.options.RPM != nil && cfg.GetBaseRPM() > 0 {
			if n, err := s.options.RPM(ctx, v.ID); err == nil {
				out.CurrentRPM = &n
			}
		}
	}
	return out
}

// applyQuotaAutoPauseState reads dynamic settings when building each OpenAI provider's display state.
func (s *RuntimeStatusReader) applyQuotaAutoPauseState(ctx context.Context, v *Record) {
	if v.Platform == PlatformOpenAI {
		var settings QuotaAutoPauseSettings
		if s.options.QuotaSettings != nil {
			settings = s.options.QuotaSettings(ctx)
		}
		v.QuotaAutoPaused, _ = EvaluateQuotaAutoPause(v.Platform, v.Extra, settings, s.options.Now())
	}
}
