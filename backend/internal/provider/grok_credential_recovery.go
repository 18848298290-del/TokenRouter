package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// grok_credential_recovery.go contains failed Grok credential use through
// conditional state writes and runtime-block recovery.
// A failed Grok credential use can race with completed renewal or an
// administrator's provider update. Recovery therefore captures the failed
// credential/proxy snapshot, serializes this provider's mutation attempt, and
// rereads current eligibility before installing a provisional runtime block and
// requesting conditional persistence.
//
// A clean comparison miss rolls back that provisional block and tries to return
// changed valid credentials. Ordinary persistence failure retains the block,
// while cancellation-related errors use a separate health-state confirmation
// read. Unconfirmed writes roll back when the caller is cancelled, but retain
// the block when an inner timeout leaves the caller live. Confirmation checks
// state, reason, and deadline rather than full failed credentials.
// Permanent-state acceptance then attempts detached cache cleanup;
// transient-state acceptance retains its pause without that cleanup. Separate
// context budgets describe each dependency phase.

// Grok recovery budgets cover conditional state writes, cancellation-time
// health confirmation, and detached cache cleanup after accepted permanent state.
const (
	grokCredentialMutationTimeout     = 5 * time.Second
	grokCredentialMutationConfirmWait = 250 * time.Millisecond
	grokCredentialCacheCleanupTimeout = 500 * time.Millisecond
)

// ErrGrokCredentialStateUpdateFailed reports unavailable conditional mutation,
// unconfirmed persistence, or failed cache cleanup while recovery contains a failure.
var ErrGrokCredentialStateUpdateFailed = errors.New("grok oauth provider state update failed")

// GrokCredentialMutation classifies a failed-use snapshot for conditional
// provider containment.
type GrokCredentialMutation struct {
	// Permanent and Transient choose different persistence paths after validation.
	// Permanent wins when both are true, then additionally requests cache
	// invalidation after accepted persistent state.
	Permanent bool // request persistent credential error
	Transient bool // request temporary scheduling pause
	// VerifyMissing requires the reread identity still to lack token text,
	// renewable text, usable expiry, or remaining lifetime before this failed-use
	// mutation is accepted.
	VerifyMissing bool // recheck token completeness failure
	// VerifyProxy requires the selected proxy still to be absent. A restored proxy
	// or removed association makes the earlier missing-proxy failure obsolete.
	VerifyProxy bool   // recheck selected proxy absence
	Reason      string // persisted credential failure reason
	// Snapshot overrides the snapshot captured from value. Its struct is copied
	// shallowly, so the optional proxy pointer stays borrowed and stable through
	// comparison.
	Snapshot *CredentialMutationSnapshot `json:"-"` // optional failed-use identity override
}

// GrokCredentialStateWriter compares failed credential observations before
// accepting provider containment.
type GrokCredentialStateWriter interface {
	// SetGrokCredentialErrorIfMatch compares the failed credential/proxy snapshot
	// before accepting persistent error state. False denotes rejected identity
	// or eligibility; an error can require the caller's separate commit check.
	SetGrokCredentialErrorIfMatch(context.Context, int64, CredentialMutationSnapshot, string) (bool, error)
	// SetGrokCredentialTempUnschedulableIfMatch compares the failed snapshot
	// before accepting a temporary pause. False denotes rejected identity or
	// eligibility; accepted state can retain a longer deadline under storage's
	// comparison policy.
	SetGrokCredentialTempUnschedulableIfMatch(context.Context, int64, CredentialMutationSnapshot, time.Time, string) (bool, error)
}

// GrokCredentialRecovery coordinates snapshot rereads, conditional writes, and
// provisional runtime blocks.
type GrokCredentialRecovery struct {
	// Read supplies validation and confirmation observations. Reached persistence
	// requires it; observed nil rows are handled as missing state, while callback
	// errors remain read diagnostics.
	Read  func(context.Context, int64) (*Record, error) // current provider identity reader
	State GrokCredentialStateWriter                     // conditional credential containment writer
	// Invalidate is required after accepted permanent state. It uses a detached
	// half-second context; failure can be reported after that state has already
	// committed.
	Invalidate func(context.Context, *Record) error // accepted-state token cache cleanup
	// Runtime installs provider-wide provisional deadlines and also checks
	// separately recorded identity-scoped refresh failures. Rollback uses the
	// installed generation; a nil runtime makes its helper calls no-ops.
	Runtime *RuntimeBlockState   // shared provisional scheduling blocks
	Warn    func(string, ...any) // optional recovery warning callback
	// locks retains one RefreshLock per provider ID on normal entries. That lock
	// covers this mutation sequence, while other provider updates remain protected
	// by snapshot comparison.
	locks sync.Map // provider keyed mutation mutexes
}

// Apply conditionally contains a failed credential identity or returns a
// concurrently changed valid token for retry. It serializes normal
// provider-keyed attempts, snapshots the supplied record unless overridden, and
// validates the reread state before mutation.
//
// Permanent wins over Transient. Runtime blocking precedes conditional
// persistence; a clean comparison miss rolls it back, ordinary write failures
// retain it, and cancellation-related errors can confirm matching health state
// through a separate read. When confirmation fails, cancelled callers roll back
// while live callers retain containment after an inner timeout. Confirmation
// compares health fields, not the full credential identity. Permanent acceptance
// then attempts detached cache invalidation; transient acceptance keeps its pause.
//
// Caller cancellation can be returned after accepted state or cleanup effects.
// Nil receiver/value with a live nonnil context returns that context's current
// error, which can be nil; nil context returns Canceled. Dependencies receive
// phase deadlines and remain responsible for responding to them.
// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
func (s *GrokCredentialRecovery) Apply(ctx context.Context, value *Record, class GrokCredentialMutation) (string, error) {
	if s == nil || value == nil || ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return "", ctx.Err()
		}
		return "", context.Canceled
	}
	mutationMu := s.mutationLock(value.ID)
	if err := mutationMu.Lock(ctx); err != nil {
		return "", err
	}
	defer mutationMu.Unlock()
	stateRepo, hasConditionalStateRepo := s.State, s.State != nil
	snapshot := GrokCredentialMutationSnapshot(CloneRecord(value))
	if class.Snapshot != nil {
		snapshot = *class.Snapshot
	}
	if token, err := s.validateCurrent(ctx, value.ID, snapshot, class); err != nil || token != "" {
		return token, err
	}

	if class.Permanent {
		if token, ok := s.concurrentlyRefreshedToken(ctx, value.ID, snapshot); ok {
			return token, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Block request admission before the conditional write. Rollback keeps
		// newer runtime generations intact and remains deferred unless this path
		// decides containment should survive.
		rollbackRuntime := s.blockRuntime(value, time.Time{}, class.Reason)
		keepRuntimeBlock := false
		runtimeRollbackDone := false
		defer func() {
			if !keepRuntimeBlock && !runtimeRollbackDone {
				rollbackRuntime()
			}
		}()
		if s.Read == nil {
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: provider repository is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		if !hasConditionalStateRepo {
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: conditional provider repository is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		stateCtx, cancel := context.WithTimeout(ctx, grokCredentialMutationTimeout)
		if err := ctx.Err(); err != nil {
			cancel()
			return "", err
		}
		updated, err := stateRepo.SetGrokCredentialErrorIfMatch(stateCtx, value.ID, snapshot, class.Reason)
		requestErr := ctx.Err()
		cancel()
		if err != nil {
			s.warn("grok_credential_failure.set_error_failed", "provider_id", value.ID, "reason", class.Reason, "error", err)
			if requestErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// The state call can commit before returning cancellation. Confirm
				// its health fields independently before choosing retained
				// containment over this attempt's rollback.
				if s.mutationCommitted(value.ID, class, time.Time{}) {
					updated = true
				} else if requestErr != nil {
					return "", requestErr
				} else {
					keepRuntimeBlock = true
					return "", fmt.Errorf("%w: permanent state commit could not be confirmed: %v", ErrGrokCredentialStateUpdateFailed, err)
				}
			} else {
				keepRuntimeBlock = true
				return "", fmt.Errorf("%w: persist permanent state: %v", ErrGrokCredentialStateUpdateFailed, err)
			}
		}
		// A rejected comparison leaves this attempt without accepted state.
		// Roll back its provisional generation before looking for changed
		// credentials so the newer provider stays eligible for its own handling.
		if !updated {
			rollbackRuntime()
			runtimeRollbackDone = true
			return s.resolveCASMiss(ctx, value.ID, snapshot)
		}
		// Accepted permanent state still excludes this identity. Keep its
		// provisional block through cache cleanup so deferred rollback cannot
		// reopen request admission after persistence.
		keepRuntimeBlock = true
		if s.Invalidate == nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("%w: token provider is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		// Permanent state is already accepted. Detach caller cancellation for
		// bounded cache cleanup so that state can outlive a disconnected request;
		// cleanup failure still reports partial completion.
		invalidateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grokCredentialCacheCleanupTimeout)
		err = s.Invalidate(invalidateCtx, CloneRecord(value))
		cancel()
		if err != nil {
			s.warn("grok_credential_failure.invalidate_token_failed", "provider_id", value.ID, "reason", class.Reason, "error", err)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("%w: invalidate cached credential: %v", ErrGrokCredentialStateUpdateFailed, err)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", nil
	}

	if class.Transient {
		// Capture one deadline for the conditional pause and its confirmation
		// read, so both phases compare the same attempted scheduling window.
		until := time.Now().Add(TokenRefreshTempUnschedDuration)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Publish temporary request containment before persistence, then retain
		// or roll back its generation according to the write outcome rather than
		// allowing a stale attempt to clear newer work.
		rollbackRuntime := s.blockRuntime(value, until, class.Reason)
		keepRuntimeBlock := false
		runtimeRollbackDone := false
		defer func() {
			if !keepRuntimeBlock && !runtimeRollbackDone {
				rollbackRuntime()
			}
		}()
		stateCtx, cancel := context.WithTimeout(ctx, grokCredentialMutationTimeout)
		if s.Read == nil {
			cancel()
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: provider repository is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		if !hasConditionalStateRepo {
			cancel()
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: conditional provider repository is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		if err := ctx.Err(); err != nil {
			cancel()
			return "", err
		}
		updated, err := stateRepo.SetGrokCredentialTempUnschedulableIfMatch(stateCtx, value.ID, snapshot, until, class.Reason)
		requestErr := ctx.Err()
		cancel()
		if err != nil {
			s.warn("grok_credential_failure.set_temp_unschedulable_failed", "provider_id", value.ID, "reason", class.Reason, "error", err)
			if requestErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// A timed-out pause write can already be visible. Confirm reason
				// and sufficient deadline before retaining runtime containment;
				// credentials are outside this confirmation read.
				if s.mutationCommitted(value.ID, class, until) {
					updated = true
				} else if requestErr != nil {
					return "", requestErr
				} else {
					keepRuntimeBlock = true
					return "", fmt.Errorf("%w: transient state commit could not be confirmed: %v", ErrGrokCredentialStateUpdateFailed, err)
				}
			} else {
				keepRuntimeBlock = true
				return "", fmt.Errorf("%w: persist transient state: %v", ErrGrokCredentialStateUpdateFailed, err)
			}
		}
		// A rejected comparison leaves this attempt without accepted state.
		// Roll back its provisional generation before looking for changed
		// credentials so the newer provider stays eligible for its own handling.
		if !updated {
			rollbackRuntime()
			runtimeRollbackDone = true
			return s.resolveCASMiss(ctx, value.ID, snapshot)
		}
		// Accepted temporary state retains this deadline for scheduling. Keep
		// the provisional block so deferred cleanup cannot reopen admission
		// while that persisted pause remains active.
		keepRuntimeBlock = true
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", nil
	}

	return "", nil
}

// validateCurrent compares reread scheduling, credential JSON, proxy identity,
// and optional missing-token/proxy conditions against the failed snapshot. It
// uses a one-second context and distinguishes missing/changed state from read
// failure.
//
// Changed credentials can return a newly valid token through
// concurrentlyRefreshedToken. A restored selected proxy defeats VerifyProxy,
// while ordinary recovery needs a configured proxy loaded. VerifyMissing keeps
// the prior failure actionable only while completeness or expiry remains
// unusable. Callers supply a usable reader, positive ID, and context; early
// guards can return its current nil error when those prerequisites are missing.
func (s *GrokCredentialRecovery) validateCurrent(
	ctx context.Context,
	providerID int64,
	snapshot CredentialMutationSnapshot,
	class GrokCredentialMutation,
) (string, error) {
	if s == nil || s.Read == nil || providerID <= 0 || ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return "", ctx.Err()
		}
		return "", context.Canceled
	}
	// Give current-identity validation a separate read budget before installing
	// any provisional block. Read failure then returns its repository diagnostic
	// before this attempt reaches conditional persistence.
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	latest, err := s.Read(checkCtx, providerID)
	if err != nil {
		if errors.Is(err, ErrProviderNotFound) {
			return "", ErrRefreshProviderStateChanged
		}
		return "", fmt.Errorf("%w: %v", ErrRefreshProviderRereadFailed, err)
	}
	if latest == nil || !latest.IsGrokOAuth() || !latest.IsSchedulable() || s.blocked(latest) {
		return "", ErrRefreshProviderStateChanged
	}
	latestSnapshot := GrokCredentialMutationSnapshot(latest)
	if latestSnapshot.CredentialsJSON != snapshot.CredentialsJSON ||
		!GrokCredentialProxyIDsEqual(latestSnapshot.ProxyID, snapshot.ProxyID) {
		if token, ok := s.concurrentlyRefreshedToken(ctx, providerID, snapshot); ok {
			return token, nil
		}
		return "", ErrRefreshProviderStateChanged
	}

	// The loaded proxy object is outside the provider-row CAS comparison.
	// Recheck it so recovery of the same proxy ID takes precedence over a stale missing-proxy failure.
	if class.VerifyProxy {
		if latest.ProxyID == nil || latest.Proxy != nil {
			return "", ErrRefreshProviderStateChanged
		}
	} else if latest.ProxyID != nil && latest.Proxy == nil {
		return "", ErrRefreshProviderStateChanged
	}

	if class.VerifyMissing {
		expiresAt := latest.GetCredentialAsTime("expires_at")
		credentialsStillMissing := strings.TrimSpace(latest.GetGrokAccessToken()) == "" ||
			strings.TrimSpace(latest.GetGrokRefreshToken()) == "" || expiresAt == nil || !time.Now().Before(*expiresAt)
		if !credentialsStillMissing {
			return "", ErrRefreshProviderStateChanged
		}
	}
	return "", nil
}

// mutationLock returns the retained cancellation-aware provider lock for a
// normal entry. An incompatible stored value is replaced with a fresh lock; the
// private registry is expected to contain RefreshLock pointers.
func (s *GrokCredentialRecovery) mutationLock(providerID int64) *RefreshLock {
	actual, _ := s.locks.LoadOrStore(providerID, NewRefreshLock())
	mu, ok := actual.(*RefreshLock)
	if !ok {
		// Normal entries are RefreshLock pointers. Replace an unexpected value
		// so this operation receives a usable cancellation-aware mutation lock.
		mu = NewRefreshLock()
		s.locks.Store(providerID, mu)
	}
	return mu
}

// mutationCommitted checks a background quarter-second reread for matching
// containment health fields after a cancellation-related write error. Nil
// dependencies, invalid ID, failed read, or absent row returns false.
//
// Permanent confirmation compares errored status, disabled scheduling, and
// reason. Transient confirmation compares reason and a deadline at least as
// long as until. It does not compare failed credentials or proxy identity, so
// callers use this result as health-write confirmation separately from the
// pre-write snapshot gate.
func (s *GrokCredentialRecovery) mutationCommitted(providerID int64, class GrokCredentialMutation, until time.Time) bool {
	if s == nil || s.Read == nil || providerID <= 0 {
		return false
	}
	// Persistence may have committed before cancellation reached its caller.
	// Read health state with an independent budget; credentials remain outside
	// this confirmation predicate.
	confirmCtx, cancel := context.WithTimeout(context.Background(), grokCredentialMutationConfirmWait)
	defer cancel()
	latest, err := s.Read(confirmCtx, providerID)
	// A failed read cannot establish accepted state. Return false so the
	// mutation caller uses its unconfirmed-write handling rather than trusting
	// unavailable health evidence.
	if err != nil || latest == nil {
		return false
	}
	if class.Permanent {
		return latest.Status == StatusError && !latest.Schedulable && latest.ErrorMessage == class.Reason
	}
	if class.Transient {
		return latest.TempUnschedulableUntil != nil && !latest.TempUnschedulableUntil.Before(until) &&
			latest.TempUnschedulableReason == class.Reason
	}
	return false
}

// resolveCASMiss checks caller cancellation and then searches for a changed
// valid token after comparison rejection. A qualifying token permits retry;
// otherwise changed-state containment is returned so the caller leaves the
// newer row intact.
func (s *GrokCredentialRecovery) resolveCASMiss(ctx context.Context, providerID int64, snapshot CredentialMutationSnapshot) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if token, ok := s.concurrentlyRefreshedToken(ctx, providerID, snapshot); ok {
		return token, nil
	}
	return "", ErrRefreshProviderStateChanged
}

// blockRuntime installs a provider-wide provisional deadline through Runtime
// and returns its guarded rollback. Nil receiver or record returns a no-op.
// Runtime uses RuntimeBlockFallback for zero or expired deadlines and retains
// any longer existing provider deadline.
func (s *GrokCredentialRecovery) blockRuntime(value *Record, until time.Time, reason string) func() {
	if s == nil || value == nil {
		return func() {}
	}
	return s.Runtime.BlockRollback(value.ID, until, reason)
}

// concurrentlyRefreshedToken searches a one-second reread for changed
// credential JSON, unchanged proxy ID, schedulability, no runtime block,
// complete token text, and future expiry. Failed or absent reads return false.
//
// A qualifying access token lets the caller retry after losing its mutation
// comparison. This helper does not independently repeat Grok OAuth kind
// validation; Apply's earlier read supplies that check at its observation.
// Callers stabilize the baseline proxy pointer and treat this later repository
// result as another observation, not an atomic guarantee across concurrent
// updates.
func (s *GrokCredentialRecovery) concurrentlyRefreshedToken(ctx context.Context, providerID int64, baseline CredentialMutationSnapshot) (string, bool) {
	if s == nil || s.Read == nil || providerID <= 0 || ctx == nil || ctx.Err() != nil {
		return "", false
	}
	// A lost comparison can leave newer credentials available. Give retry-token
	// rereading a separate budget; failed observation contributes no token and
	// leaves containment or changed-state handling with its caller.
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	latest, err := s.Read(checkCtx, providerID)
	// A failed read supplies no retry token. Return false so the mutation
	// caller uses containment or changed-state handling rather than borrowing
	// unavailable credential evidence.
	if err != nil || latest == nil {
		return "", false
	}
	latestSnapshot := GrokCredentialMutationSnapshot(latest)
	if !GrokCredentialProxyIDsEqual(latestSnapshot.ProxyID, baseline.ProxyID) ||
		latestSnapshot.CredentialsJSON == baseline.CredentialsJSON || !latest.IsSchedulable() ||
		(latest.ProxyID != nil && latest.Proxy == nil) || s.blocked(latest) {
		return "", false
	}
	latestToken := strings.TrimSpace(latest.GetGrokAccessToken())
	if latestToken == "" || strings.TrimSpace(latest.GetGrokRefreshToken()) == "" {
		return "", false
	}
	expiresAt := latest.GetCredentialAsTime("expires_at")
	if expiresAt == nil || !time.Now().Before(*expiresAt) {
		return "", false
	}
	return latestToken, true
}

// blocked asks shared runtime state about accepted OpenAI or Grok records using
// their refresh credential identity. Nil receiver, record, or other platforms
// return false; the runtime decides timed and identity-scoped blocking.
func (s *GrokCredentialRecovery) blocked(value *Record) bool {
	if s == nil || value == nil || (value.Platform != PlatformOpenAI && value.Platform != PlatformGrok) {
		return false
	}
	return s.Runtime.Blocked(value.ID, func() string { return RefreshCredentialIdentity(value) })
}

// warn forwards credential-recovery diagnostics through the optional Warn
// callback. A missing callback leaves the mutation result and retained runtime
// block decisions unchanged for the caller.
func (s *GrokCredentialRecovery) warn(message string, values ...any) {
	if s.Warn != nil {
		s.Warn(message, values...)
	}
}
