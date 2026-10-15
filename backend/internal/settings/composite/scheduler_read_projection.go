package composite

import "github.com/TokenFlux/TokenRouter/internal/scheduler"

// scheduler_read_projection.go copies configured and effective scheduler read
// values into the combined snapshot.
// Administrator scheduler settings show configured text beside the parameters
// that will actually be used after inheritance and normalization. Both sets
// come from one scheduler read result, allowing empty overrides to remain
// visible while the separate effective fields explain their deployment defaults
// and selected runtime values.

// ApplySchedulerAdminReadSettings transfers configured and effective scheduler
// values from the owning reader result.
func (s *Snapshot) ApplySchedulerAdminReadSettings(value *scheduler.AdminReadSettings) {
	s.AdvancedSchedulerEWMAErrorRateAlpha = value.AdvancedSchedulerEWMAErrorRateAlpha
	s.AdvancedSchedulerEWMATTFTAlpha = value.AdvancedSchedulerEWMATTFTAlpha
	s.AdvancedSchedulerEffectiveEWMAErrorRateAlpha = value.AdvancedSchedulerEffectiveEWMAErrorRateAlpha
	s.AdvancedSchedulerEffectiveEWMATTFTAlpha = value.AdvancedSchedulerEffectiveEWMATTFTAlpha
	s.AdvancedSchedulerEffectiveLBTopK = value.AdvancedSchedulerEffectiveLBTopK
	s.AdvancedSchedulerEffectiveStickyEscapeEnabled = value.AdvancedSchedulerEffectiveStickyEscapeEnabled
	s.AdvancedSchedulerEffectiveStickyEscapeErrorRate = value.AdvancedSchedulerEffectiveStickyEscapeErrorRate
	s.AdvancedSchedulerEffectiveStickyEscapeTTFTMs = value.AdvancedSchedulerEffectiveStickyEscapeTTFTMs
	s.AdvancedSchedulerEffectiveWeightErrorRate = value.AdvancedSchedulerEffectiveWeightErrorRate
	s.AdvancedSchedulerEffectiveWeightLoad = value.AdvancedSchedulerEffectiveWeightLoad
	s.AdvancedSchedulerEffectiveWeightPreviousResponse = value.AdvancedSchedulerEffectiveWeightPreviousResponse
	s.AdvancedSchedulerEffectiveWeightPriority = value.AdvancedSchedulerEffectiveWeightPriority
	s.AdvancedSchedulerEffectiveWeightQueue = value.AdvancedSchedulerEffectiveWeightQueue
	s.AdvancedSchedulerEffectiveWeightQuotaHeadroom = value.AdvancedSchedulerEffectiveWeightQuotaHeadroom
	s.AdvancedSchedulerEffectiveWeightReset = value.AdvancedSchedulerEffectiveWeightReset
	s.AdvancedSchedulerEffectiveWeightSessionSticky = value.AdvancedSchedulerEffectiveWeightSessionSticky
	s.AdvancedSchedulerEffectiveWeightTTFT = value.AdvancedSchedulerEffectiveWeightTTFT
	s.AdvancedSchedulerLBTopK = value.AdvancedSchedulerLBTopK
	s.AdvancedSchedulerStickyEscapeEnabled = value.AdvancedSchedulerStickyEscapeEnabled
	s.AdvancedSchedulerStickyEscapeEnabledSet = value.AdvancedSchedulerStickyEscapeEnabledSet
	s.AdvancedSchedulerStickyEscapeErrorRate = value.AdvancedSchedulerStickyEscapeErrorRate
	s.AdvancedSchedulerStickyEscapeTTFTMs = value.AdvancedSchedulerStickyEscapeTTFTMs
	s.AdvancedSchedulerStickyWeightedEnabled = value.AdvancedSchedulerStickyWeightedEnabled
	s.AdvancedSchedulerSubscriptionPriorityEnabled = value.AdvancedSchedulerSubscriptionPriorityEnabled
	s.AdvancedSchedulerWeightErrorRate = value.AdvancedSchedulerWeightErrorRate
	s.AdvancedSchedulerWeightLoad = value.AdvancedSchedulerWeightLoad
	s.AdvancedSchedulerWeightPreviousResponse = value.AdvancedSchedulerWeightPreviousResponse
	s.AdvancedSchedulerWeightPriority = value.AdvancedSchedulerWeightPriority
	s.AdvancedSchedulerWeightQueue = value.AdvancedSchedulerWeightQueue
	s.AdvancedSchedulerWeightQuotaHeadroom = value.AdvancedSchedulerWeightQuotaHeadroom
	s.AdvancedSchedulerWeightReset = value.AdvancedSchedulerWeightReset
	s.AdvancedSchedulerWeightSessionSticky = value.AdvancedSchedulerWeightSessionSticky
	s.AdvancedSchedulerWeightTTFT = value.AdvancedSchedulerWeightTTFT
}
