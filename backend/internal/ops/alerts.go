package ops

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Alert administration reads saved rules and events and writes rule changes,
// silence deadlines, resolution states, and mail markers. Monitoring is checked
// before each operation; missing storage yields empty list responses but makes
// writes and individual event lookups unavailable.

// ListAlertRules returns saved rules after monitoring validation, using an
// allocated empty slice when storage is absent. Available storage owns ordering
// and query errors, which are returned directly.
func (s *OpsService) ListAlertRules(ctx context.Context) ([]*OpsAlertRule, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return []*OpsAlertRule{}, nil
	}
	return s.opsRepo.ListAlertRules(ctx)
}

// CreateAlertRule persists a supplied rule after monitoring and repository
// checks. A nil rule is rejected; other fields are passed through to storage,
// whose PostgreSQL implementation trims text and encodes Filters before
// returning the saved row.
func (s *OpsService) CreateAlertRule(ctx context.Context, rule *OpsAlertRule) (*OpsAlertRule, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if rule == nil {
		return nil, infraerrors.BadRequest("INVALID_RULE", "invalid rule")
	}

	created, err := s.opsRepo.CreateAlertRule(ctx, rule)
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateAlertRule persists an existing rule after monitoring validation and
// requires a positive rule identity. Missing storage is unavailable, and
// repository errors recognized by the injected classifier become the
// administrator rule-not-found error.
func (s *OpsService) UpdateAlertRule(ctx context.Context, rule *OpsAlertRule) (*OpsAlertRule, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if rule == nil || rule.ID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE", "invalid rule")
	}

	updated, err := s.opsRepo.UpdateAlertRule(ctx, rule)
	if err != nil {
		if s.isNotFound(err) {
			return nil, infraerrors.NotFound("OPS_ALERT_RULE_NOT_FOUND", "alert rule not found")
		}
		return nil, err
	}
	return updated, nil
}

// DeleteAlertRule deletes the positive rule identity after monitoring and
// storage checks. Repository errors recognized as missing rows become the
// administrator rule-not-found error; other deletion errors are returned
// directly.
func (s *OpsService) DeleteAlertRule(ctx context.Context, id int64) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if id <= 0 {
		return infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	if err := s.opsRepo.DeleteAlertRule(ctx, id); err != nil {
		if s.isNotFound(err) {
			return infraerrors.NotFound("OPS_ALERT_RULE_NOT_FOUND", "alert rule not found")
		}
		return err
	}
	return nil
}

// ListAlertEvents returns persisted events after monitoring validation,
// accepting a nil filter for repository defaults. Missing storage yields an
// allocated empty slice; event limits, filtering, and cursor ordering belong to
// the repository.
func (s *OpsService) ListAlertEvents(ctx context.Context, filter *OpsAlertEventFilter) ([]*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return []*OpsAlertEvent{}, nil
	}
	return s.opsRepo.ListAlertEvents(ctx, filter)
}

// GetAlertEventByID loads a positive event identity after monitoring and
// repository checks. A classified missing-row error or a nil successful result
// becomes the administrator event-not-found error; other query errors remain
// visible.
func (s *OpsService) GetAlertEventByID(ctx context.Context, eventID int64) (*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if eventID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_EVENT_ID", "invalid event id")
	}
	ev, err := s.opsRepo.GetAlertEventByID(ctx, eventID)
	if err != nil {
		if s.isNotFound(err) {
			return nil, infraerrors.NotFound("OPS_ALERT_EVENT_NOT_FOUND", "alert event not found")
		}
		return nil, err
	}
	if ev == nil {
		return nil, infraerrors.NotFound("OPS_ALERT_EVENT_NOT_FOUND", "alert event not found")
	}
	return ev, nil
}

// GetActiveAlertEvent returns the active event for a positive rule identity
// after monitoring and storage checks. The repository may return nil when none
// is firing, allowing the evaluator to consider a new event.
func (s *OpsService) GetActiveAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if ruleID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	return s.opsRepo.GetActiveAlertEvent(ctx, ruleID)
}

// CreateAlertSilence persists an exact alert scope after monitoring and storage
// checks. Input requires a positive rule identity, nonblank platform, and
// nonzero deadline; this wrapper passes the supplied scope and deadline through
// to repository validation.
func (s *OpsService) CreateAlertSilence(ctx context.Context, input *OpsAlertSilence) (*OpsAlertSilence, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if input == nil {
		return nil, infraerrors.BadRequest("INVALID_SILENCE", "invalid silence")
	}
	if input.RuleID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	if strings.TrimSpace(input.Platform) == "" {
		return nil, infraerrors.BadRequest("INVALID_PLATFORM", "invalid platform")
	}
	if input.Until.IsZero() {
		return nil, infraerrors.BadRequest("INVALID_UNTIL", "invalid until")
	}

	created, err := s.opsRepo.CreateAlertSilence(ctx, input)
	if err != nil {
		return nil, err
	}
	return created, nil
}

// IsAlertSilenced checks a saved scope at now after monitoring, storage, and
// positive-rule validation. A blank platform returns false without a query; the
// repository owns exact nullable group/region matching and deadline comparison.
func (s *OpsService) IsAlertSilenced(ctx context.Context, ruleID int64, platform string, groupID *int64, region *string, now time.Time) (bool, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return false, err
	}
	if s.opsRepo == nil {
		return false, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if ruleID <= 0 {
		return false, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	if strings.TrimSpace(platform) == "" {
		return false, nil
	}
	return s.opsRepo.IsAlertSilenced(ctx, ruleID, platform, groupID, region, now)
}

// GetLatestAlertEvent returns the latest stored event for a positive rule
// identity after monitoring and storage checks. The evaluator uses its FiredAt
// timestamp for cooldown; an absent history can be returned as nil.
func (s *OpsService) GetLatestAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if ruleID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RULE_ID", "invalid rule id")
	}
	return s.opsRepo.GetLatestAlertEvent(ctx, ruleID)
}

// CreateAlertEvent persists a supplied firing observation after monitoring and
// repository checks. A nil event is rejected; the observation, comparison
// values, and mail marker are passed to storage and its successful insert
// returns the saved row.
func (s *OpsService) CreateAlertEvent(ctx context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if event == nil {
		return nil, infraerrors.BadRequest("INVALID_EVENT", "invalid event")
	}

	created, err := s.opsRepo.CreateAlertEvent(ctx, event)
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateAlertEventStatus persists a resolution state for a positive event
// identity after monitoring and storage checks. Trimmed status must be resolved
// or manual_resolved; resolvedAt is passed through, and PostgreSQL stores SQL
// NULL when that pointer is nil or zero-valued.
func (s *OpsService) UpdateAlertEventStatus(ctx context.Context, eventID int64, status string, resolvedAt *time.Time) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if eventID <= 0 {
		return infraerrors.BadRequest("INVALID_EVENT_ID", "invalid event id")
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return infraerrors.BadRequest("INVALID_STATUS", "invalid status")
	}
	if status != OpsAlertStatusResolved && status != OpsAlertStatusManualResolved {
		return infraerrors.BadRequest("INVALID_STATUS", "invalid status")
	}
	// The repository writes the resolution status and timestamp together. This
	// call records the administrator action and does not update evaluation breach
	// counters.
	return s.opsRepo.UpdateAlertEventStatus(ctx, eventID, status, resolvedAt)
}

// UpdateAlertEventEmailSent writes the stored mail-attempt marker for a
// positive event identity after monitoring and repository checks. The update is
// separate from mail callback execution, so its error reports marker
// persistence rather than SMTP delivery.
func (s *OpsService) UpdateAlertEventEmailSent(ctx context.Context, eventID int64, emailSent bool) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if eventID <= 0 {
		return infraerrors.BadRequest("INVALID_EVENT_ID", "invalid event id")
	}
	// Store the marker in its own update after callback handling; a storage
	// failure can follow a callback that already accepted the email request.
	return s.opsRepo.UpdateAlertEventEmailSent(ctx, eventID, emailSent)
}
