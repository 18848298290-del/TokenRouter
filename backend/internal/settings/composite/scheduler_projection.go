package composite

import (
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// scheduler_projection.go transfers configured scheduler overrides and their
// presence flag through the snapshot.
// Advanced scheduler parameters are stored as optional numeric text alongside
// switches for selection behavior. The combined Snapshot passes that configured
// text to scheduler normalization with its separate presence flag, then receives
// the normalized update values; effective inherited values come from the
// scheduler read-result conversion.

// SchedulerAdminSettings returns configured scheduler text, switches, and the
// sticky-escape presence flag.
func (s *Snapshot) SchedulerAdminSettings() scheduler.AdminSettings {
	return scheduler.AdminSettings{
		AdvancedSchedulerEWMAErrorRateAlpha:          s.AdvancedSchedulerEWMAErrorRateAlpha,
		AdvancedSchedulerEWMATTFTAlpha:               s.AdvancedSchedulerEWMATTFTAlpha,
		AdvancedSchedulerLBTopK:                      s.AdvancedSchedulerLBTopK,
		AdvancedSchedulerStickyEscapeEnabled:         s.AdvancedSchedulerStickyEscapeEnabled,
		AdvancedSchedulerStickyEscapeEnabledSet:      s.AdvancedSchedulerStickyEscapeEnabledSet,
		AdvancedSchedulerStickyEscapeErrorRate:       s.AdvancedSchedulerStickyEscapeErrorRate,
		AdvancedSchedulerStickyEscapeTTFTMs:          s.AdvancedSchedulerStickyEscapeTTFTMs,
		AdvancedSchedulerStickyWeightedEnabled:       s.AdvancedSchedulerStickyWeightedEnabled,
		AdvancedSchedulerSubscriptionPriorityEnabled: s.AdvancedSchedulerSubscriptionPriorityEnabled,
		AdvancedSchedulerWeightErrorRate:             s.AdvancedSchedulerWeightErrorRate,
		AdvancedSchedulerWeightLoad:                  s.AdvancedSchedulerWeightLoad,
		AdvancedSchedulerWeightPreviousResponse:      s.AdvancedSchedulerWeightPreviousResponse,
		AdvancedSchedulerWeightPriority:              s.AdvancedSchedulerWeightPriority,
		AdvancedSchedulerWeightQueue:                 s.AdvancedSchedulerWeightQueue,
		AdvancedSchedulerWeightQuotaHeadroom:         s.AdvancedSchedulerWeightQuotaHeadroom,
		AdvancedSchedulerWeightReset:                 s.AdvancedSchedulerWeightReset,
		AdvancedSchedulerWeightSessionSticky:         s.AdvancedSchedulerWeightSessionSticky,
		AdvancedSchedulerWeightTTFT:                  s.AdvancedSchedulerWeightTTFT,
	}
}

// ApplySchedulerAdminSettings assigns normalized scheduler text, switches, and
// presence metadata to the Snapshot.
func (s *Snapshot) ApplySchedulerAdminSettings(value scheduler.AdminSettings) {
	s.AdvancedSchedulerEWMAErrorRateAlpha = value.AdvancedSchedulerEWMAErrorRateAlpha
	s.AdvancedSchedulerEWMATTFTAlpha = value.AdvancedSchedulerEWMATTFTAlpha
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
