package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ManagedCredentialStore interface {
	GetProvider(context.Context, int64) (*Record, error)
	UpdateProviderExtra(context.Context, int64, map[string]any) error
	UpdateProvider(context.Context, int64, *UpdateProviderInput) (*Record, error)
	ClearProviderError(context.Context, int64) (*Record, error)
}
type ManagedCredentialPrivacy interface {
	EnsureOpenAIPrivacy(context.Context, *Record) string
	EnsureAntigravityPrivacy(context.Context, *Record) string
}
type ManagedRefreshObservation struct {
	Credentials      map[string]any
	ProjectIDMissing bool
}
type ManagedRefreshOptions struct {
	Store       ManagedCredentialStore
	Privacy     ManagedCredentialPrivacy
	Coordinate  func(context.Context, *Record, string, func(context.Context, *Record) (*Record, string, error)) (*Record, string, error)
	CacheKey    func(*Record) string
	Exchange    func(context.Context, *Record) (ManagedRefreshObservation, error)
	Invalidate  func(context.Context, *Record) error
	Log         func(string, ...any)
	Warn, Error func(string, ...any)
}

// ManagedRefreshService coordinates credential refresh and separate persistence steps for individual and batch
// administration.
type ManagedRefreshService struct{ options ManagedRefreshOptions }

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

func (s *ManagedRefreshService) Refresh(ctx context.Context, value *Record) (*Record, string, error) {
	if err := ValidateManagedRefreshTarget(value); err != nil {
		return nil, "", err
	}
	if s.options.Coordinate == nil {
		return nil, "", errors.New("managed refresh coordinator is not configured")
	}
	return s.options.Coordinate(ctx, value, s.options.CacheKey(value), s.refresh)
}

func (s *ManagedRefreshService) refresh(ctx context.Context, value *Record) (*Record, string, error) {
	if err := ValidateManagedRefreshTarget(value); err != nil {
		return nil, "", err
	}
	expected := FailureVersion(value).CredentialVersion
	observed, err := s.options.Exchange(ctx, value)
	if err != nil {
		if value.IsOpenAI() {
			s.options.Privacy.EnsureOpenAIPrivacy(ctx, value)
		}
		return nil, "", err
	}
	credentials := CloneValues(observed.Credentials)
	if value.Platform == PlatformAntigravity {
		if project, _ := credentials["project_id"].(string); project == "" {
			if old := strings.TrimSpace(value.GetCredential("project_id")); old != "" {
				credentials["project_id"] = old
			}
		}
		if observed.ProjectIDMissing {
			updated, err := s.options.Store.UpdateProvider(ctx, value.ID, &UpdateProviderInput{Credentials: credentials, ExpectedCredentials: &expected})
			if errors.Is(err, ErrRefreshProviderStateChanged) {
				return s.current(ctx, value.ID)
			}
			if err != nil {
				return nil, "", fmt.Errorf("failed to update credentials: %w", err)
			}
			s.options.Privacy.EnsureAntigravityPrivacy(ctx, updated)
			return updated, "missing_project_id_temporary", nil
		}
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
		return s.current(ctx, value.ID)
	}
	if err != nil {
		return nil, "", err
	}
	if s.options.Invalidate != nil {
		if err := s.options.Invalidate(ctx, updated); err != nil {
			s.options.Log("[WARN] Failed to invalidate token cache for provider %d: %v", updated.ID, err)
		}
	}
	s.options.Privacy.EnsureOpenAIPrivacy(ctx, updated)
	s.options.Privacy.EnsureAntigravityPrivacy(ctx, updated)
	return updated, "", nil
}

// current reloads the provider after a CAS conflict so the discarded exchange result causes no further updates.
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

// ClearError clears provider health state and attempts token-cache invalidation for administrative callers.
func (s *ManagedRefreshService) ClearError(ctx context.Context, id int64) (*Record, error) {
	value, err := s.options.Store.ClearProviderError(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.options.Invalidate != nil {
		if err := s.options.Invalidate(ctx, value); err != nil {
			s.options.Log("[WARN] Failed to invalidate token cache for provider %d: %v", id, err)
		}
	}
	return value, nil
}
