package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
)

const (
	ManagedRecoveryError ManagedRecoveryStep = iota
	ManagedRecoveryRateLimit
	ManagedRecoveryQuotaScopes
	ManagedRecoveryModelLimits
	ManagedRecoveryTemporary
)

var ErrManagedRecoveryUnavailable = errors.New("managed refresh conditional recovery is not configured")

// ManagedRecoveryStep identifies one of five independently committed recovery updates.
type ManagedRecoveryStep uint8

// ManagedRecoveryVersion captures credential identity and health state for conditional recovery writes.
type ManagedRecoveryVersion struct {
	UsageObservationVersion
	ErrorMessage string         `json:"-"`
	Until        *time.Time     `json:"-"`
	Reason       string         `json:"-"`
	Extra        map[string]any `json:"-"`
}

type ManagedRecoveryWriter interface {
	ApplyManagedRecoveryStep(context.Context, ManagedRecoveryStep, ManagedRecoveryVersion) (bool, error)
}

// ManagedRecoveryUnblocker uses a runtime generation to prevent late recovery from clearing a newer block.
type ManagedRecoveryUnblocker interface {
	ManagedRecoveryFence(int64) uint64
	ClearProviderSchedulingBlockIfFence(int64, uint64) bool
}

type ManagedCredentialRecovery interface {
	ClearManagedRefreshError(context.Context, *Record) (*Record, bool, error)
}

func ObserveManagedRecovery(value *Record) ManagedRecoveryVersion {
	return ManagedRecoveryVersion{
		UsageObservationVersion: ObserveUsageVersion(value), ErrorMessage: value.ErrorMessage,
		Until: clonePointer(value.TempUnschedulableUntil), Reason: value.TempUnschedulableReason, Extra: CloneValues(value.Extra),
	}
}

// Advance updates the expected state after the corresponding step commits successfully.
func (v *ManagedRecoveryVersion) Advance(step ManagedRecoveryStep) {
	switch step {
	case ManagedRecoveryError:
		v.Status, v.ErrorMessage = StatusActive, ""
	case ManagedRecoveryRateLimit:
		v.RateLimitedAt, v.RateLimitResetAt, v.OverloadUntil = nil, nil, nil
	case ManagedRecoveryQuotaScopes:
		delete(v.Extra, "antigravity_quota_scopes")
	case ManagedRecoveryModelLimits:
		delete(v.Extra, "model_rate_limits")
	case ManagedRecoveryTemporary:
		v.Until, v.Reason = nil, ""
	}
}

func (v ManagedRecoveryVersion) Matches(value *Record) bool {
	if value == nil || !MatchesCredentialVersion(value, v.CredentialVersion) {
		return false
	}
	current := ObserveManagedRecovery(value)
	// Compare the two extra keys owned by recovery so unrelated configuration can change concurrently.
	for _, key := range []string{"antigravity_quota_scopes", "model_rate_limits"} {
		want, hasWant := v.Extra[key]
		got, hasGot := current.Extra[key]
		if hasWant != hasGot || !reflect.DeepEqual(want, got) {
			return false
		}
	}
	current.Extra, v.Extra = nil, nil
	return reflect.DeepEqual(current, v)
}

// ClearManagedRefreshError clears an Antigravity missing-project error through conditional steps, stopping on
// conflict or failure.
func (s *Admin) ClearManagedRefreshError(ctx context.Context, value *Record) (*Record, bool, error) {
	if value == nil || value.Platform != PlatformAntigravity || value.Status != StatusError || !strings.Contains(value.ErrorMessage, "missing_project_id:") {
		return nil, false, nil
	}
	writer, ok := s.providerRepo.(ManagedRecoveryWriter)
	if !ok {
		return nil, false, ErrManagedRecoveryUnavailable
	}
	var blocker ManagedRecoveryUnblocker
	var fence uint64
	if s.options.RuntimeBlocker != nil {
		blocker, ok = s.options.RuntimeBlocker.(ManagedRecoveryUnblocker)
		if !ok {
			return nil, false, ErrManagedRecoveryUnavailable
		}
		fence = blocker.ManagedRecoveryFence(value.ID)
	}
	expected := ObserveManagedRecovery(value)
	for step := ManagedRecoveryError; step <= ManagedRecoveryTemporary; step++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		applied, err := writer.ApplyManagedRecoveryStep(ctx, step, expected)
		if err != nil || !applied {
			return nil, false, err
		}
		expected.Advance(step)
	}
	current, err := s.providerRepo.GetByID(ctx, value.ID)
	if err != nil || !expected.Matches(current) {
		return current, false, err
	}
	if blocker != nil && !blocker.ClearProviderSchedulingBlockIfFence(value.ID, fence) {
		return current, false, nil
	}
	return current, true, nil
}
