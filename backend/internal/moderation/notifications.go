package moderation

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
)

// notifications.go converts moderation records and policies into risk
// notification inputs.
// Risk notices consume thresholds and observations selected by moderation.
// Ordinary notices are prepared before a new audit log receives its database
// ID, while Cyber notices receive the committed warning ID for notification
// source handling.

// riskPolicy copies the ordinary and Cyber thresholds into notification input.
// A nil configuration returns nil, allowing the notification variable builders
// to apply their own missing-policy display values.
func riskPolicy(c *ContentModerationConfig) *contract.RiskPolicy {
	if c == nil {
		return nil
	}
	return &contract.RiskPolicy{BanThreshold: c.BanThreshold, CyberBanThreshold: c.CyberBanThreshold}
}

// riskLog copies the observations used to address and format an ordinary risk
// notice. Nil input returns nil; UserID remains the supplied pointer, so
// consumers keep its value stable while notification delivery reads it.
func riskLog(v *ContentModerationLog) *contract.RiskLog {
	if v == nil {
		return nil
	}
	return &contract.RiskLog{ID: v.ID, UserID: v.UserID, UserEmail: v.UserEmail, GroupName: v.GroupName, HighestCategory: v.HighestCategory, HighestScore: v.HighestScore, ViolationCount: v.ViolationCount, AutoBanned: v.AutoBanned, CreatedAt: v.CreatedAt}
}

// riskWarning copies the recipient and source observations for a Cyber
// disabled-account notice. Nil input returns nil; the resulting UserID still
// shares the warning pointer and stays stable during delivery.
func riskWarning(v *ContentModerationCyberWarning) *contract.RiskWarning {
	if v == nil {
		return nil
	}
	return &contract.RiskWarning{ID: v.ID, UserID: v.UserID, UserEmail: v.UserEmail, GroupName: v.GroupName, ProviderName: v.ProviderName, ViolationCount: v.ViolationCount, CreatedAt: v.CreatedAt}
}

// sendViolationEmail delegates an ordinary violation notice using converted
// policy and log observations. The caller supplies a bound emailService and a
// nonnil log; delivery errors return to the side-effect handler that records
// them.
func (s *ContentModerationService) sendViolationEmail(ctx context.Context, c *ContentModerationConfig, v *ContentModerationLog) error {
	return s.emailService.SendViolationEmail(ctx, riskPolicy(c), riskLog(v))
}

// sendAccountDisabledEmail delegates the notice for an ordinary status change
// already applied by moderation. The caller supplies a bound emailService and a
// nonnil log; this call returns the notification error for diagnostic handling.
func (s *ContentModerationService) sendAccountDisabledEmail(ctx context.Context, c *ContentModerationConfig, v *ContentModerationLog) error {
	return s.emailService.SendAccountDisabledEmail(ctx, riskPolicy(c), riskLog(v))
}

// sendCyberAccountDisabledEmail delegates the notice for a committed Cyber
// status change. The caller supplies a bound emailService and a nonnil warning
// whose stored ID gives notification delivery its Cyber source identity.
func (s *ContentModerationService) sendCyberAccountDisabledEmail(ctx context.Context, c *ContentModerationConfig, v *ContentModerationCyberWarning) error {
	return s.emailService.SendCyberAccountDisabledEmail(ctx, riskPolicy(c), riskWarning(v))
}
