package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Grok OAuth reconciliation pages active providers in increasing ID order and
// separates planned actions from apply outcomes. Missing renewal credentials
// choose blocking, while incomplete access data or near expiry chooses refresh.
// Dry-run reports that classification, and apply rereads blocking candidates
// before conditionally writing their credentials' error state.
//
// Accepted blocking publishes failure state only after its comparison write and
// then attempts cache invalidation. Cleanup failure becomes Partial within the
// already counted Blocked result. Refresh can also become permanent
// containment, changing the item's Action while its earlier WouldRefresh count
// remains planned. Cancellation can discard the partial response while
// preceding writes stay accepted. The service tracks the actual page operation
// for shutdown, retaining the first drain result when dependencies ignore
// cancellation.

const (
	// Reconciliation page and timing limits bound submitted administrative
	// inputs before platform skew is applied as an effective window floor.
	DefaultGrokOAuthReconcilePageSize = 50
	MaxGrokOAuthReconcilePageSize     = 500
	MaxGrokOAuthReconcileWindow       = 24 * time.Hour

	// Reconciliation reasons classify the first actionable credential
	// deficiency and remain public labels rather than raw upstream diagnostics.
	GrokOAuthReconcileReasonMissingRefreshToken = "missing_refresh_token" // no renewable token text
	GrokOAuthReconcileReasonMissingAccessToken  = "missing_access_token"  // no stored access text
	GrokOAuthReconcileReasonMissingExpiry       = "missing_expiry"        // no stored expiry text
	GrokOAuthReconcileReasonInvalidExpiry       = "invalid_expiry"        // unparseable stored expiry value
	GrokOAuthReconcileReasonNearExpiry          = "near_expiry"           // expiry within renewal window
	GrokOAuthReconcileReasonCredentialRejected  = "credential_rejected"   // renewal committed credential rejection

	// Reconciliation actions distinguish persistent missing-token containment
	// from renewal execution before the final per-provider outcome is known.
	GrokOAuthReconcileActionBlock   = "block_provider"      // persist provider error state
	GrokOAuthReconcileActionRefresh = "refresh_credentials" // execute credential renewal attempt

	// Reconciliation outcomes record dry-run planning, applied effects, skipped
	// comparison, execution failure, or accepted blocking with failed cache cleanup.
	GrokOAuthReconcileOutcomePlanned = "planned" // dry-run action remains planned
	GrokOAuthReconcileOutcomeApplied = "applied" // requested effect was accepted
	GrokOAuthReconcileOutcomeSkipped = "skipped" // comparison or execution skipped
	GrokOAuthReconcileOutcomeFailed  = "failed"  // apply action reported failure
	GrokOAuthReconcileOutcomePartial = "partial" // block accepted cleanup incomplete
)

// Reconciliation input errors reject incompatible apply mode, invalid cursors,
// unsupported page sizes, and out-of-range submitted refresh windows before paging.
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

// GrokReconciliationOptions supplies paging, conditional containment, refresh
// execution, and timing.
type GrokReconciliationOptions struct {
	Pager OAuthRefreshCandidatePager // active Grok OAuth page reader
	// Reader is reached for apply blocking after page classification. That reread
	// is reclassified before conditional containment so a newer renewable row can
	// skip the old planned action.
	Reader RefreshRepository // blocking candidate reread storage
	// ConditionalError is required whenever Apply is true, including empty pages.
	// False write results skip publication and preserve the row that rejected
	// observed credentials.
	ConditionalError GrokOAuthConditionalErrorWriter // complete-credential containment comparison writer
	// Execution is required even for dry-run and empty pages. It supplies the
	// registered refresher and its per-round state before actionable entries are
	// processed.
	Execution func() *RefreshProviderExecution // registered refresh execution supplier
	// PrepareFailure captures publication inputs before conditional persistence.
	// The returned callback is invoked after an accepted block so a clean
	// comparison miss installs no failure notice.
	PrepareFailure func(*Record) func(time.Time, string) // deferred failure publication preparer
	// Invalidate runs after accepted blocking and publication. Missing callback or
	// cleanup error produces Partial, with Blocked still counting the already
	// accepted persisted effect.
	Invalidate func(context.Context, *Record) error // optional post-block cache cleanup
	Now        func() time.Time                     // credential expiry comparison clock
	// Skew defaults a zero submitted window and also raises a validated nonzero
	// window. Only Now is defaulted by construction; final skew raising follows
	// submitted range validation.
	Skew time.Duration // effective refresh window floor
}

// GrokReconciliationService tracks administrative page classification and
// independently accepted provider effects.
type GrokReconciliationService struct {
	options GrokReconciliationOptions // retained page and execution dependencies
	// activity tracks paging and every provider action until ReconcileGrokOAuth
	// actually returns. Stop rejects admission, cancels active contexts, and
	// retains its first completion or unfinished result.
	activity operationActivity // page admission and completion owner
}

// GrokOAuthReconciler exposes administrative dry-run and apply reconciliation
// with lifecycle ownership.
type GrokOAuthReconciler interface {
	// ReconcileGrokOAuth returns one planned or applied provider page. Effective
	// dry-run is the absence of Apply, and incompatible mode or page inputs fail
	// before actions. Apply effects are accepted independently per provider, so
	// cancellation can return no response after earlier writes remain. Callers use
	// per-item outcomes and separate planned counts to interpret partial cleanup
	// or permanent refresh rejection; stopping waits for actual admitted page
	// completion.
	ReconcileGrokOAuth(ctx context.Context, input GrokOAuthReconcileInput) (*GrokOAuthReconcileResult, error)
}

// GrokOAuthConditionalErrorWriter compares observed credentials before
// accepting active OAuth containment.
type GrokOAuthConditionalErrorWriter interface {
	// SetGrokOAuthErrorIfCredentialsUnchanged compares the observed complete
	// credential map before accepting active Grok OAuth containment. False reports
	// a rejected comparison or eligibility, and errors are returned; callers keep
	// expected mutable credential values stable during the write.
	SetGrokOAuthErrorIfCredentialsUnchanged(ctx context.Context, id int64, expectedCredentials map[string]any, errorMsg string) (bool, error)
}

// GrokOAuthReconcileInput selects page continuation, planning or apply, and
// renewal timing.
type GrokOAuthReconcileInput struct {
	// DryRun and Apply cannot both be true. Effective planning is the absence of
	// Apply, so a zero-value input still performs dry-run after defaults are
	// selected.
	DryRun  bool  // submitted planning mode flag
	Apply   bool  // request accepted provider writes
	AfterID int64 // exclusive page starting identity
	// Limit zero selects fifty, while one through five hundred is accepted. The
	// pager returns strictly increasing IDs and separate continuation metadata
	// that the service validates.
	Limit int // submitted maximum page count
	// RefreshWindow zero uses Skew. A nonzero value is range-checked before it is
	// raised to Skew, so the configured floor can exceed the submitted input
	// limit.
	RefreshWindow time.Duration // submitted renewal timing duration
}

// GrokOAuthReconcileItem contains public provider identity, classified action,
// and execution outcome.
type GrokOAuthReconcileItem struct {
	ProviderID int64  `json:"provider_id"` // classified stored provider identity
	Reason     string `json:"reason"`      // public credential deficiency label
	// Action begins with classification but can become block_provider after
	// permanent refresh rejection. Planned WouldRefresh remains unchanged in that
	// case, while final Blocked and Outcome record the effect.
	Action  string `json:"action"`  // planned or resulting action
	Outcome string `json:"outcome"` // public per-provider execution result
}

// GrokOAuthReconcileResult retains planned counts separately from apply
// outcomes and page continuation.
type GrokOAuthReconcileResult struct {
	DryRun  bool `json:"dry_run"` // effective planning mode flag
	Scanned int  `json:"scanned"` // loaded provider page count
	// Actionable, WouldBlock, and WouldRefresh describe initial page
	// classification. Blocked and Refreshed describe accepted execution, so
	// permanent renewal rejection can change the final action without rewriting
	// planned counts.
	Actionable   int `json:"actionable"`    // initially classified action count
	WouldBlock   int `json:"would_block"`   // initially planned blocking count
	WouldRefresh int `json:"would_refresh"` // initially planned renewal count
	Blocked      int `json:"blocked"`       // accepted persistent containment count
	Refreshed    int `json:"refreshed"`     // successful refresh execution count
	Skipped      int `json:"skipped"`       // nonactionable or skipped action count
	Failed       int `json:"failed"`        // failed apply execution count
	// Partial is a subset of Blocked because cache cleanup follows accepted
	// containment. Failed describes other apply errors; Skipped also includes
	// nonactionable rows omitted from Items.
	Partial int `json:"partial"` // blocked outcomes with cleanup failure
	// Items contains actionable entries in page order, including later skipped or
	// failed actions. Context cancellation inside the loop returns nil result even
	// when previous apply effects remain committed.
	Items []GrokOAuthReconcileItem `json:"items"` // actionable entries in page order
	// NextAfterID is populated only when HasMore is true and the pager supplied a
	// value beyond the input cursor. It can remain zero on the final page.
	NextAfterID int64 `json:"next_after_id"` // validated next page starting identity
	HasMore     bool  `json:"has_more"`      // pager reports another page
}

// NewGrokReconciliationService retains reconciliation dependencies and supplies
// time.Now when its clock is absent. Construction begins no page work; callers
// configure reached storage and execution callbacks before invoking
// reconciliation.
func NewGrokReconciliationService(options GrokReconciliationOptions) *GrokReconciliationService {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &GrokReconciliationService{options: options}
}

// StopContext rejects new reconciliation and cancels accepted page operations,
// waiting within a nonnil caller budget. The first result, including unfinished
// work after timeout, remains retained for subsequent stops.
func (s *GrokReconciliationService) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "Grok provider reconciliation")
}

// ReconcileGrokOAuth returns planned or applied results for one validated
// active Grok OAuth page. Nil context selects background; effective dry-run is
// the absence of Apply, while Apply plus DryRun is rejected. Execution is
// required even for dry-run or empty pages.
//
// Blocking candidates are reread and reclassified, then credentials are
// conditionally written before failure publication and cache cleanup. A clean
// comparison miss skips the action, while missing/failed cleanup yields Partial
// within Blocked. Refresh errors can become accepted permanent containment and
// change item action while preserving initial planned counts.
//
// Nonactionable rows increment Skipped but stay outside Items. Cancellation
// inside iteration returns nil/error and drops the accumulated response even if
// earlier effects remain. Submitted window validation precedes Skew raising,
// and callbacks receive lifecycle cancellation while staying tracked until
// actual return.
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
	// Keep the platform's minimum renewal lead time after validating the
	// submitted range, so this administrative pass does not choose a shorter
	// window than ordinary background maintenance.
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

		// Missing renewable credentials are the blocking case, so include those
		// active rows in this page instead of applying normal refresh-token
		// candidate admission.
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
				// A reread can reveal renewed credentials after page selection.
				// Skip obsolete blocking so that this planned action cannot disable
				// a now-renewable or otherwise ineligible row.
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
				break
			}
			provider = latest
			item.Reason = latestReason
			// Capture publication before comparison, but invoke it only after the
			// error write accepts these credentials. A rejected or failed write
			// must leave its pending notice unpublished.
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
				// A concurrent credential or eligibility update rejected this write.
				// Keep its row and runtime state intact by skipping the prepared
				// failure notice.
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
				break
			}
			publishFailure(time.Time{}, "grok_oauth_reconcile_invalid")
			provider.Status = StatusError
			provider.Schedulable = false
			// Persistent blocking is already accepted before cache cleanup. Keep
			// that effect in Blocked even when cleanup is missing or fails, and
			// report the incomplete follow-up as Partial.
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
			// Per-round failures can stop further refresh attempts. Check the
			// shared trip state before dispatch so reconciliation follows this
			// execution round's admission decision.
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
				// Renewal has instead committed permanent containment. Change the
				// final item action and applied count while retaining WouldRefresh
				// as the earlier planned classification.
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

// ClassifyGrokOAuthReconcileProvider returns the first actionable deficiency
// for an active Grok OAuth row. Nil, other kinds, and inactive status are
// skipped. Missing renewal text chooses persistent blocking even if access
// remains valid; missing access, missing/invalid expiry, or expiry at or within
// refreshWindow chooses renewal.
//
// The clock is reached only after usable expiry is obtained. Exact equality
// qualifies here, while the ordinary refresher applies its separate jittered
// strict timing comparison. The reason records this classification for planned
// counts before apply rereads can change the outcome.
func ClassifyGrokOAuthReconcileProvider(provider *Record, refreshWindow time.Duration, now func() time.Time) (reason, action string, actionable bool) {
	if provider == nil || !provider.IsGrokOAuth() || provider.Status != StatusActive {
		return "", "", false
	}
	// Renewal admission requires refresh-token text. Give its absence priority
	// over access and expiry problems, because those later deficiencies can
	// still be repaired through credential exchange.
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
