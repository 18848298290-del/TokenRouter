package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	DefaultGrokOAuthReconcilePageSize = 50
	MaxGrokOAuthReconcilePageSize     = 500
	MaxGrokOAuthReconcileWindow       = 24 * time.Hour

	GrokOAuthReconcileReasonMissingRefreshToken = "missing_refresh_token"
	GrokOAuthReconcileReasonMissingAccessToken  = "missing_access_token"
	GrokOAuthReconcileReasonMissingExpiry       = "missing_expiry"
	GrokOAuthReconcileReasonInvalidExpiry       = "invalid_expiry"
	GrokOAuthReconcileReasonNearExpiry          = "near_expiry"
	GrokOAuthReconcileReasonCredentialRejected  = "credential_rejected"

	GrokOAuthReconcileActionBlock   = "block_provider"
	GrokOAuthReconcileActionRefresh = "refresh_credentials"

	GrokOAuthReconcileOutcomePlanned = "planned"
	GrokOAuthReconcileOutcomeApplied = "applied"
	GrokOAuthReconcileOutcomeSkipped = "skipped"
	GrokOAuthReconcileOutcomeFailed  = "failed"
	GrokOAuthReconcileOutcomePartial = "partial"
)

var (
	ErrGrokOAuthReconcileMode = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_MODE_INVALID",
		"apply requires dry_run=false and apply=true",
	)
	ErrGrokOAuthReconcileCursor = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_CURSOR_INVALID",
		"after_id must be non-negative",
	)
	ErrGrokOAuthReconcileLimit = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_LIMIT_INVALID",
		"limit is outside the allowed reconciliation page range",
	)
	ErrGrokOAuthReconcileWindow = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_WINDOW_INVALID",
		"refresh_window_seconds is outside the allowed range",
	)
)

// GrokReconciliationOptions supplies storage, refresh execution, invalidation, and timing dependencies.
type GrokReconciliationOptions struct {
	Pager            OAuthRefreshCandidatePager
	Reader           RefreshRepository
	ConditionalError GrokOAuthConditionalErrorWriter
	Execution        func() *RefreshProviderExecution
	PrepareFailure   func(*Record) func(time.Time, string)
	Invalidate       func(context.Context, *Record) error
	Now              func() time.Time
	Skew             time.Duration
}

// GrokReconciliationService classifies paged OAuth records, reports per-provider results, and tracks work for
// shutdown.
type GrokReconciliationService struct {
	options  GrokReconciliationOptions
	activity operationActivity
}

// GrokOAuthReconciler exposes OAuth reconciliation to administrative callers.
type GrokOAuthReconciler interface {
	ReconcileGrokOAuth(ctx context.Context, input GrokOAuthReconcileInput) (*GrokOAuthReconcileResult, error)
}

// GrokOAuthConditionalErrorWriter conditionally marks an active Grok OAuth provider as errored.
// The repository must verify that its complete credentials still match the observed values.
type GrokOAuthConditionalErrorWriter interface {
	SetGrokOAuthErrorIfCredentialsUnchanged(ctx context.Context, id int64, expectedCredentials map[string]any, errorMsg string) (bool, error)
}
type GrokOAuthReconcileInput struct {
	DryRun        bool
	Apply         bool
	AfterID       int64
	Limit         int
	RefreshWindow time.Duration
}

// GrokOAuthReconcileItem exposes provider IDs and classified outcomes; credentials, account identity, and raw
// upstream errors must stay private.
type GrokOAuthReconcileItem struct {
	ProviderID int64  `json:"provider_id"`
	Reason     string `json:"reason"`
	Action     string `json:"action"`
	Outcome    string `json:"outcome"`
}
type GrokOAuthReconcileResult struct {
	DryRun       bool                     `json:"dry_run"`
	Scanned      int                      `json:"scanned"`
	Actionable   int                      `json:"actionable"`
	WouldBlock   int                      `json:"would_block"`
	WouldRefresh int                      `json:"would_refresh"`
	Blocked      int                      `json:"blocked"`
	Refreshed    int                      `json:"refreshed"`
	Skipped      int                      `json:"skipped"`
	Failed       int                      `json:"failed"`
	Partial      int                      `json:"partial"`
	Items        []GrokOAuthReconcileItem `json:"items"`
	NextAfterID  int64                    `json:"next_after_id"`
	HasMore      bool                     `json:"has_more"`
}

func NewGrokReconciliationService(options GrokReconciliationOptions) *GrokReconciliationService {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &GrokReconciliationService{options: options}
}

func (s *GrokReconciliationService) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "Grok provider reconciliation")
}

func (s *GrokReconciliationService) ReconcileGrokOAuth(ctx context.Context, input GrokOAuthReconcileInput) (*GrokOAuthReconcileResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, finish, err := s.activity.begin(ctx, ErrRefreshStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	if input.Apply && input.DryRun {
		return nil, ErrGrokOAuthReconcileMode
	}
	if input.AfterID < 0 {
		return nil, ErrGrokOAuthReconcileCursor
	}
	limit := input.Limit
	maxPageSize := MaxGrokOAuthReconcilePageSize
	if limit == 0 {
		limit = min(DefaultGrokOAuthReconcilePageSize, maxPageSize)
	}
	if limit < 1 || limit > maxPageSize {
		return nil, ErrGrokOAuthReconcileLimit
	}
	refreshWindow := input.RefreshWindow
	if refreshWindow == 0 {
		refreshWindow = s.options.Skew
	}
	if refreshWindow < 0 || refreshWindow > MaxGrokOAuthReconcileWindow {
		return nil, ErrGrokOAuthReconcileWindow
	}
	if refreshWindow < s.options.Skew {
		refreshWindow = s.options.Skew
	}
	dryRun := !input.Apply

	pager := s.options.Pager
	if pager == nil {
		return nil, errors.New("OAuth refresh candidate pager is not configured")
	}
	page, err := pager.ListOAuthRefreshCandidatePage(ctx, OAuthRefreshPageOptions{
		Platforms: []string{PlatformGrok},

		AfterID: input.AfterID,

		Limit: limit,

		ActiveOnly: true,

		// Include OAuth records with missing refresh tokens so reconciliation can detect incomplete credentials.
		IncludeSetupToken: false,

		RequireRefreshToken: false,
	})
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, errors.New("OAuth reconciliation repository returned a nil cursor page")
	}
	providers := page.Providers
	if !IsStrictlyIncreasingProviderPage(providers, input.AfterID) {
		return nil, errors.New("OAuth reconciliation repository returned an invalid cursor page")
	}

	result := &GrokOAuthReconcileResult{
		DryRun:  dryRun,
		Scanned: len(providers),
		Items:   make([]GrokOAuthReconcileItem, 0, len(providers)),
		HasMore: page.HasMore,
	}
	if result.HasMore {
		if page.NextAfterID <= input.AfterID {
			return nil, errors.New("OAuth reconciliation repository returned invalid cursor metadata")
		}
		result.NextAfterID = page.NextAfterID
	}

	execution := s.options.Execution()
	if execution == nil {
		return nil, errors.New("grok OAuth refresher is not registered")
	}
	conditionalErrorRepo := s.options.ConditionalError
	if input.Apply && conditionalErrorRepo == nil {
		return nil, errors.New("grok OAuth conditional error mutation is not configured")
	}
	providerState := execution.State

	for i := range providers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		provider := &providers[i]
		reason, action, actionable := ClassifyGrokOAuthReconcileProvider(provider, refreshWindow, s.options.Now)
		if !actionable {
			result.Skipped++
			continue
		}
		result.Actionable++
		item := GrokOAuthReconcileItem{
			ProviderID: provider.ID,
			Reason:     reason,
			Action:     action,
			Outcome:    GrokOAuthReconcileOutcomePlanned,
		}
		if action == GrokOAuthReconcileActionBlock {
			result.WouldBlock++
		} else {
			result.WouldRefresh++
		}
		if dryRun {
			result.Items = append(result.Items, item)
			continue
		}

		switch action {
		case GrokOAuthReconcileActionBlock:
			latest, err := s.options.Reader.GetByID(ctx, provider.ID)
			if err != nil || latest == nil {
				item.Outcome = GrokOAuthReconcileOutcomeFailed
				result.Failed++
				break
			}
			latestReason, latestAction, stillActionable := ClassifyGrokOAuthReconcileProvider(latest, refreshWindow, s.options.Now)
			if !stillActionable || latestAction != GrokOAuthReconcileActionBlock {
				// Skip the action when the provider changed after the page was loaded.
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
				break
			}
			provider = latest
			item.Reason = latestReason
			publishFailure := s.options.PrepareFailure(provider)
			applied, err := conditionalErrorRepo.SetGrokOAuthErrorIfCredentialsUnchanged(
				ctx,
				provider.ID,
				provider.Credentials,
				"Grok OAuth credential reconciliation: missing refresh token",
			)
			if err != nil {
				item.Outcome = GrokOAuthReconcileOutcomeFailed
				result.Failed++
				break
			}
			if !applied {
				// A concurrent update won the CAS. Install runtime blocks only after a successful write.
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
				break
			}
			publishFailure(time.Time{}, "grok_oauth_reconcile_invalid")
			provider.Status = StatusError
			provider.Schedulable = false
			cacheInvalidationFailed := s.options.Invalidate == nil
			if s.options.Invalidate != nil {
				if err := s.options.Invalidate(ctx, provider); err != nil {
					cacheInvalidationFailed = true
				}
			}
			result.Blocked++
			if cacheInvalidationFailed {
				item.Outcome = GrokOAuthReconcileOutcomePartial
				result.Partial++
			} else {
				item.Outcome = GrokOAuthReconcileOutcomeApplied
			}
		case GrokOAuthReconcileActionRefresh:
			if providerState.IsTripped() {
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
				break
			}
			if providerState.IsTripped() {
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
				break
			}
			refreshErr := execution.Execute(ctx, provider, refreshWindow, providerState)
			providerState.RecordResult(refreshErr)
			var permanentErr *ProviderPermanentRefreshError
			switch {
			case refreshErr == nil:
				item.Outcome = GrokOAuthReconcileOutcomeApplied
				result.Refreshed++
			case errors.Is(refreshErr, ErrRefreshSkipped):
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
			case errors.As(refreshErr, &permanentErr) && permanentErr.PersistentlyBlocked:
				item.Reason = GrokOAuthReconcileReasonCredentialRejected
				item.Action = GrokOAuthReconcileActionBlock
				result.Blocked++
				if permanentErr.CacheInvalidationFailed {
					item.Outcome = GrokOAuthReconcileOutcomePartial
					result.Partial++
				} else {
					item.Outcome = GrokOAuthReconcileOutcomeApplied
				}
			default:
				item.Outcome = GrokOAuthReconcileOutcomeFailed
				result.Failed++
			}
		default:
			return nil, fmt.Errorf("unsupported Grok OAuth reconciliation action")
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func ClassifyGrokOAuthReconcileProvider(provider *Record, refreshWindow time.Duration, now func() time.Time) (reason, action string, actionable bool) {
	if provider == nil || !provider.IsGrokOAuth() || provider.Status != StatusActive {
		return "", "", false
	}
	if strings.TrimSpace(provider.GetGrokRefreshToken()) == "" {
		return GrokOAuthReconcileReasonMissingRefreshToken, GrokOAuthReconcileActionBlock, true
	}
	if strings.TrimSpace(provider.GetGrokAccessToken()) == "" {
		return GrokOAuthReconcileReasonMissingAccessToken, GrokOAuthReconcileActionRefresh, true
	}
	rawExpiry := strings.TrimSpace(provider.GetCredential("expires_at"))
	if rawExpiry == "" {
		return GrokOAuthReconcileReasonMissingExpiry, GrokOAuthReconcileActionRefresh, true
	}
	expiresAt := provider.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return GrokOAuthReconcileReasonInvalidExpiry, GrokOAuthReconcileActionRefresh, true
	}
	if expiresAt.Sub(now()) <= refreshWindow {
		return GrokOAuthReconcileReasonNearExpiry, GrokOAuthReconcileActionRefresh, true
	}
	return "", "", false
}
