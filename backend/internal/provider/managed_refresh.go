package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// managed_refresh.go coordinates managed credential exchange, conditional
// publication, and platform maintenance.
// Managed refresh validates the target before entering the application's
// credential coordinator, then captures expected identity before exchange. A
// successful exchange supplies copied credentials for conditional configuration
// update; a conflict reloads an eligible current record and discards further
// use of the exchange result. Store participation determines each update's
// persistence outcome.
//
// Antigravity can retain a nonblank old project ID, publish a missing-project
// temporary result, or conditionally clear its prior missing-project error
// before credential saving. Earlier recovery can remain applied if later saving
// fails. Token invalidation and privacy follow the selected successful path,
// while an OpenAI exchange error can still invoke privacy maintenance. These
// stages keep their own results and logs; the service does not combine
// exchange, recovery, credential publication and privacy into one transaction.

// ManagedCredentialStore supplies administrative reads, credential publication
// and separate health/Extra cleanup.
type ManagedCredentialStore interface {
	// GetProvider reads a current provider after conditional publication
	// conflicts. Read errors propagate; the service then checks returned
	// credential kind and shadow ownership before exposing that record as a
	// discarded-exchange result.
	GetProvider(context.Context, int64) (*Record, error)
	// UpdateProviderExtra attempts a separate Extra merge for administrative
	// reauthorization. Pass a writable patch: the production writer can remove
	// deprecated keys. Failures are logged by that path after credentials may
	// already have been saved.
	UpdateProviderExtra(context.Context, int64, map[string]any) error
	// UpdateProvider publishes a usable updated record or an error for the
	// selected administrative change. ExpectedCredentials requests
	// observed-identity comparison; ErrRefreshProviderStateChanged selects
	// reloading instead of using the exchange result for later effects.
	UpdateProvider(context.Context, int64, *UpdateProviderInput) (*Record, error)
	// ClearProviderError attempts primary health cleanup and returns the record
	// for subsequent token invalidation. Errors can follow earlier successful
	// cleanup calls; service callers retain that partial outcome and choose their
	// logging or returned failure.
	ClearProviderError(context.Context, int64) (*Record, error)
}

// ManagedCredentialPrivacy supplies platform-specific maintenance used after
// administrative credential handling.
type ManagedCredentialPrivacy interface {
	// EnsureOpenAIPrivacy performs the implementation's applicable OpenAI privacy
	// maintenance and returns its status text. ManagedRefreshService ignores that
	// text, leaving privacy outcomes to this dependency's observation and platform
	// eligibility rules.
	EnsureOpenAIPrivacy(context.Context, *Record) string
	// EnsureAntigravityPrivacy performs applicable Antigravity privacy maintenance
	// and returns its status text. ManagedRefreshService ignores the text after
	// credential handling, so the dependency owns its own eligibility, conditional
	// publication and diagnostics.
	EnsureAntigravityPrivacy(context.Context, *Record) string
}

// ManagedRefreshObservation carries exchanged credentials and the Antigravity
// missing-project classification.
type ManagedRefreshObservation struct {
	// Credentials is copied through CloneValues before publication; unsupported
	// mutable values remain shared and need stable owners until refresh returns.
	// Supply a nonnil object when Antigravity may
	// retain an old project_id, because that branch writes the copied map.
	Credentials map[string]any
	// ProjectIDMissing selects Antigravity's temporary warning path before
	// prior-error recovery, even when a nonblank old project_id has been retained
	// in credentials.
	ProjectIDMissing bool
}

// ManagedRefreshOptions supplies borrowed administrative dependencies,
// credential coordination and diagnostic callbacks.
type ManagedRefreshOptions struct {
	// Store remains borrowed for synchronous reads and writes, including
	// discarded-result reloads; special missing-project cleanup also needs
	// ManagedCredentialRecovery support.
	Store ManagedCredentialStore
	// Privacy receives normal post-save maintenance and selected exchange-error or
	// temporary paths; supply a usable implementation when refresh can reach those
	// calls.
	Privacy ManagedCredentialPrivacy
	// Coordinate owns the application's claim/reload wrapper and supplies the
	// record passed to refresh. An absent callback returns a configuration error
	// before CacheKey is read.
	Coordinate func(context.Context, *Record, string, func(context.Context, *Record) (*Record, string, error)) (*Record, string, error)
	// CacheKey names coordination from the initially validated record. It must be
	// callable when Coordinate is configured; exchange uses the record supplied by
	// that coordinator.
	CacheKey func(*Record) string
	// Exchange returns the credential observation from the coordinated target.
	// Keep its returned credential graph stable while copying and supply the
	// fields required by the selected platform path.
	Exchange func(context.Context, *Record) (ManagedRefreshObservation, error)
	// Invalidate is optional and runs after selected successful credential or
	// error cleanup. Its failure is logged while earlier administrative writes
	// retain their outcome.
	Invalidate func(context.Context, *Record) error
	// Log observes token invalidation failure on Refresh and ClearError;
	// NewManagedRefreshService substitutes a no-op when absent.
	Log func(string, ...any)
	// Warn and Error report separate reauthorization cleanup failures after
	// credentials can be saved. NewManagedRefreshService supplies no-ops for
	// absent callbacks, so diagnostics remain callable through every reached path.
	// Warn: later cleanup diagnostics. Error: Extra merge diagnostics.
	Warn, Error func(string, ...any)
}

// ManagedRefreshService retains coordinated exchange, administrative storage
// and post-save maintenance dependencies.
type ManagedRefreshService struct {
	options ManagedRefreshOptions // retained maintenance dependencies
}

// NewManagedRefreshService retains options and supplies no-op diagnostic
// functions when absent. It performs no exchange or validation; configure
// callable coordination/key/exchange dependencies and usable store/privacy
// services for paths reached by administrative methods.
func NewManagedRefreshService(options ManagedRefreshOptions) *ManagedRefreshService {
	if options.Log == nil {
		options.Log = func(string, ...any) {}
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	if options.Error == nil {
		options.Error = func(string, ...any) {}
	}
	return &ManagedRefreshService{options: options}
}

// Refresh validates an administrative OAuth, Setup Token or Qoder COSY target
// before delegation. Credential shadows are rejected. Configure Coordinate and
// CacheKey; the coordinator owns claim/reload behavior and invokes the internal
// refresh callback with its chosen record.
func (s *ManagedRefreshService) Refresh(ctx context.Context, value *Record) (*Record, string, error) {
	if err := ValidateManagedRefreshTarget(value); err != nil {
		return nil, "", err
	}
	if s.options.Coordinate == nil {
		return nil, "", errors.New("managed refresh coordinator is not configured")
	}
	return s.options.Coordinate(ctx, value, s.options.CacheKey(value), s.refresh)
}

// refresh exchanges a validated coordinated target and conditionally saves
// credentials against the captured identity. Keep the record and unsupported
// exchanged values stable during this call. An OpenAI exchange failure still
// attempts its privacy maintenance before returning the exchange error.
//
// Antigravity can retain an old project ID, save a temporary missing-project
// result, or apply conditional prior-error recovery before its normal
// credential update. A conflict selects current and skips remaining use of the
// exchanged result. Successful normal saving then attempts optional
// invalidation and both platform privacy methods. Logged invalidation failure
// leaves saving intact; earlier recovery can also remain applied if the later
// credential update fails.
func (s *ManagedRefreshService) refresh(ctx context.Context, value *Record) (*Record, string, error) {
	if err := ValidateManagedRefreshTarget(value); err != nil {
		return nil, "", err
	}
	// Capture expected identity before exchange so the later configuration write
	// can reject an administrator change made while the supplier request was in
	// progress.
	expected := ObserveCredentialVersion(value)
	observed, err := s.options.Exchange(ctx, value)
	// Keep the OpenAI privacy attempt before propagating exchange failure; it uses
	// the existing coordinated record and retains its own maintenance status and
	// diagnostic behavior.
	if err != nil {
		if value.IsOpenAI() {
			s.options.Privacy.EnsureOpenAIPrivacy(ctx, value)
		}
		return nil, "", err
	}
	credentials := CloneValues(observed.Credentials)
	if value.Platform == PlatformAntigravity {
		// An empty exchange project can reuse a nonblank project from the coordinated
		// record, keeping existing routing identity available when the supplier
		// observation omits it.
		if project, _ := credentials["project_id"].(string); project == "" {
			if old := strings.TrimSpace(value.GetCredential("project_id")); old != "" {
				credentials["project_id"] = old
			}
		}
		// The missing-project classification returns through its own credential-save
		// path before prior-error recovery. Its warning remains selected even when
		// the copied map retained an old project ID.
		if observed.ProjectIDMissing {
			updated, err := s.options.Store.UpdateProvider(ctx, value.ID, &UpdateProviderInput{Credentials: credentials, ExpectedCredentials: &expected})
			if errors.Is(err, ErrRefreshProviderStateChanged) {
				// Reload after a publication conflict before privacy sees the temporary
				// result. Publication rejected the observed version, so this path discards
				// its warning and post-save maintenance.
				return s.current(ctx, value.ID)
			}
			if err != nil {
				return nil, "", fmt.Errorf("failed to update credentials: %w", err)
			}
			s.options.Privacy.EnsureAntigravityPrivacy(ctx, updated)
			return updated, "missing_project_id_temporary", nil
		}
		// Conditional missing-project recovery precedes credential publication and
		// can retain successful primary steps. Advance expected status only after
		// recovery confirms completion, so the next write matches that recovered
		// state.
		if value.Status == StatusError && strings.Contains(value.ErrorMessage, "missing_project_id:") {
			recovery, ok := s.options.Store.(ManagedCredentialRecovery)
			if !ok {
				return nil, "", ErrManagedRecoveryUnavailable
			}
			_, applied, err := recovery.ClearManagedRefreshError(ctx, value)
			if err != nil {
				return nil, "", fmt.Errorf("failed to clear provider error: %w", err)
			}
			if !applied {
				return s.current(ctx, value.ID)
			}
			expected.Status = StatusActive
		}
	}
	updated, err := s.options.Store.UpdateProvider(ctx, value.ID, &UpdateProviderInput{Credentials: credentials, ExpectedCredentials: &expected})
	if errors.Is(err, ErrRefreshProviderStateChanged) {
		// Reload after a publication conflict before invalidation or privacy can use
		// the discarded exchange. Current eligibility and read errors determine this
		// request's returned record.
		return s.current(ctx, value.ID)
	}
	if err != nil {
		return nil, "", err
	}
	// Credential publication is already complete before invalidation and privacy.
	// Log an invalidation failure and continue maintenance while retaining the
	// successful configuration result.
	if s.options.Invalidate != nil {
		if err := s.options.Invalidate(ctx, updated); err != nil {
			s.options.Log("[WARN] Failed to invalidate token cache for provider %d: %v", updated.ID, err)
		}
	}
	s.options.Privacy.EnsureOpenAIPrivacy(ctx, updated)
	s.options.Privacy.EnsureAntigravityPrivacy(ctx, updated)
	return updated, "", nil
}

// current reloads the administrative record after a discarded exchange result.
// Nil, non-OAuth/non-Qoder or credential-shadow records return
// ErrRefreshProviderStateChanged; otherwise it returns the current record with
// empty warning, including its current status and platform.
func (s *ManagedRefreshService) current(ctx context.Context, id int64) (*Record, string, error) {
	current, err := s.options.Store.GetProvider(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if current == nil || (!current.IsOAuth() && !current.IsQoderCosy()) || current.IsCredentialShadow() {
		return nil, "", ErrRefreshProviderStateChanged
	}
	return current, "", nil
}

// ClearError delegates administrative health cleanup and then attempts optional
// token invalidation. A store error can follow applied primary cleanup or a
// failed final read; invalidation failures are logged and leave the successful
// returned store record available.
func (s *ManagedRefreshService) ClearError(ctx context.Context, id int64) (*Record, error) {
	value, err := s.options.Store.ClearProviderError(ctx, id)
	if err != nil {
		return nil, err
	}
	// Primary health cleanup has returned its record before this token-cache
	// attempt. A logged invalidation failure leaves those successful cleanup
	// outcomes in place for the caller.
	if s.options.Invalidate != nil {
		if err := s.options.Invalidate(ctx, value); err != nil {
			s.options.Log("[WARN] Failed to invalidate token cache for provider %d: %v", id, err)
		}
	}
	return value, nil
}
