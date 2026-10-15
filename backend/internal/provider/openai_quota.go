package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// openai_quota.go queries OpenAI quota, consumes reset credits, and builds
// persisted quota observations.
// OpenAI quota requests bind account identity and client transport before
// reading usage or consuming a reset credit. Credential shadows read through a
// separately loaded parent, while reset consumption rejects the initially
// loaded shadow row and then prepares the request through another provider
// read.
//
// Usage querying combines the main response with optional credit details:
// explicit detail count wins, and a present detail list can replace the main
// list even when empty. Credit snapshots reject positive counts without
// details before caching, but accepted cache patches retain their credit
// object and can be locally augmented before persistence fails. Consumption
// has no local rollback; response conversion can fail after an upstream action
// already succeeded. Tracked public operations let shutdown cancel active work
// and preserve the first stop result.

// OpenAI quota paths and cache keys bind account queries, reset consumption,
// rewardless query compatibility, and the stored reset-credit observation.
const (
	// chatGPTUsagePath identifies the account usage query endpoint.
	chatGPTUsagePath = "/wham/usage"
	// chatGPTRateLimitCreditsPath identifies reset-credit detail discovery.
	chatGPTRateLimitCreditsPath = "/wham/rate-limit-reset-credits"
	// chatGPTRateLimitResetPath identifies the upstream credit-consumption
	// endpoint.
	chatGPTRateLimitResetPath = "/wham/rate-limit-reset-credits/consume"
	// codexInviteResetSupportsRewardless encodes rewardless-invite support as
	// query text.
	codexInviteResetSupportsRewardless = "true"
	// openaiQuotaResetCreditsKey identifies the persisted reset-credit
	// observation.
	openaiQuotaResetCreditsKey = "codex_reset_credit_snapshot"
)

// OpenAI quota errors distinguish unsupported shadow consumption from service
// shutdown, allowing callers to retain the correct admission failure category.
var (
	// ErrSparkShadowResetNotSupported reports reset consumption on a credential
	// shadow.
	ErrSparkShadowResetNotSupported = apperror.New(409, "SPARK_SHADOW_RESET_NOT_SUPPORTED", "spark shadow provider does not support credit reset; reset the parent provider")
	// ErrOpenAIQuotaStopped reports rejected work after quota service shutdown.
	ErrOpenAIQuotaStopped = apperror.New(503, "OPENAI_QUOTA_STOPPED", "openai quota service is stopped")
)

// OpenAIQuotaClient supplies account-scoped JSON queries and credit
// consumption requests.
type OpenAIQuotaClient interface {
	// GetJSON reads an account endpoint with optional query values as raw fields.
	GetJSON(context.Context, string, map[string]string) (map[string]any, error)
	// GetJSONRaw reads endpoint bytes for credit-list presence and numeric
	// parsing.
	GetJSONRaw(context.Context, string, map[string]string) ([]byte, error)
	// PostJSON submits a stable JSON-compatible body and returns raw result
	// fields.
	// Caller cancellation governs the request; upstream effects may already exist
	// when a transport error is returned.
	PostJSON(context.Context, string, map[string]any) (map[string]any, error)
}

// PreparedOpenAIQuota retains the resolved record, token, and account request
// client.
type PreparedOpenAIQuota struct {
	// Provider is the reader's borrowed effective record, possibly a resolved
	// parent. Keep its mutable identity and transport inputs stable through client
	// requests.
	Provider *Record `json:"-"`
	Token    string  `json:"-"` // prepared account access text
	// Client is supplied for the effective record and must remain usable through
	// requests. Its implementation owns transport resources and request response
	// cleanup.
	Client OpenAIQuotaClient `json:"-"`
}

// OpenAIQuotaOptions supplies provider lookup, authentication, transport,
// persistence, and diagnostics.
type OpenAIQuotaOptions struct {
	// Configured is required by preparation and reports service admission
	// configuration. False rejects before provider reads; keep callback
	// configuration stable through use.
	Configured func() bool
	// Read is required by lookup and returns a borrowed record. Nil successful
	// result becomes NotFound, and callers retain record stability during
	// requests.
	Read func(context.Context, int64) (*Record, error)
	// Token optionally acquires ordinary OAuth access text. Blank success falls
	// back to stored access; Agent Identity bypasses this callback entirely.
	Token func(context.Context, *Record) (string, error)
	// Client is required after preparation checks and returns a usable account
	// client. Error aborts, while success retains the effective record and
	// selected token.
	Client func(context.Context, *Record, string) (OpenAIQuotaClient, error)
	// SaveExtra is required for accepted cache snapshots and receives the borrowed
	// patch/credits. The implementation owns persistence and must finish before
	// return.
	SaveExtra func(context.Context, int64, map[string]any) error
	// RedeemID is required by reset consumption and produces one ID per call.
	// Failure aborts before PostJSON, with no local ID reuse or retry.
	RedeemID func() (string, error)
	// Warn and Info remain configured through requests: Warn diagnoses omitted
	// credit details, while Info reports accepted decoded reset consumption.
	// Missing reached callbacks are caller configuration errors.
	// Warn: credit detail diagnostics; Info: reset acceptance diagnostics.
	Warn, Info func(string, ...any)
}

// OpenAIQuotaService owns tracked quota reads, credit consumption, and
// snapshot persistence.
type OpenAIQuotaService struct {
	// Options retains callbacks and their dependencies across tracked work.
	// Configure them before requests and keep them stable through shutdown and
	// completion.
	Options OpenAIQuotaOptions
	// activity tracks public request/write lifetime and retains the first shutdown
	// result. Keep this service instance stable and do not copy it after use.
	activity operationActivity
}

// QuotaResult carries a normalized quota display beside its borrowed upstream
// payload.
type QuotaResult struct {
	// UsageInfo is a constructed display view that can retain borrowed upstream
	// pointers/maps. Use CloneUsageInfo before independently mutating those nested
	// capability values.
	UsageInfo *UsageInfo // Normalized usage information.
	// Raw remains the upstream model payload without independent copying. Keep
	// nested values stable when retaining or passing this map into later
	// consumers.
	Raw map[string]any // Raw response suitable for provider Extra.
}

// String returns fixed diagnostic text omitting prepared credentials and
// client details.
func (p PreparedOpenAIQuota) String() string { return "prepared OpenAI quota request" }

// NewOpenAIQuotaService retains the supplied callbacks without invoking or
// validating them. Configure required dependencies before use and keep them
// stable through requests; the new instance owns its tracked operation
// lifecycle.
func NewOpenAIQuotaService(options OpenAIQuotaOptions) *OpenAIQuotaService {
	return &OpenAIQuotaService{Options: options}
}

// remarshalOpenAIQuotaPayload converts a stable JSON-compatible raw map into
// the supplied decode target. Marshal or decode failure propagates, and
// decoding may leave the target partly changed; this conversion does not
// preserve arbitrary Go value types.
func remarshalOpenAIQuotaPayload(raw map[string]any, target any) error {
	// Encode raw fields before typed decoding so the protocol target controls
	// recognized field/number representation. Unsupported marshal values fail
	// here rather than entering the typed observation as arbitrary Go objects.
	body, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, target)
}

// QueryUsage reads usage and best-effort reset-credit details for the
// requested row. It registers caller/shutdown cancellation, requires usable
// prepared dependencies and reached Warn, and returns a newly decoded usage
// object.
//
// Detail request failure retains the main observation. A successfully parsed
// detail list replaces the main list even when empty, and an independently
// present count wins over list-derived eligible count. Malformed detail lists
// can still contribute a valid count through the protocol parser. FetchedAt
// uses the current Unix-second clock after main decoding, and later detail
// failure does not discard that timestamp or successful main usage.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OpenAIQuotaService) QueryUsage(ctx context.Context, providerID int64) (*openai.OpenAIQuotaUsage, error) {
	// Register before provider/client work so shutdown rejects new requests and
	// cancels this admitted operation until its deferred completion releases
	// ownership of the tracked lifetime.
	ctx, done, activityErr := s.activity.begin(ctx, ErrOpenAIQuotaStopped)
	if activityErr != nil {
		return nil, activityErr
	}
	defer done()

	providerCtx, err := s.PrepareProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	// Send the shared rewardless-invite compatibility value used by Codex invite
	// status queries, keeping this usage request on their configured account-query
	// shape without inferring per-address invitation eligibility.
	raw, err := providerCtx.Client.GetJSON(ctx, chatGPTUsagePath, map[string]string{
		"supports_rewardless_invites": codexInviteResetSupportsRewardless,
	})
	if err != nil {
		return nil, err
	}

	var usage openai.OpenAIQuotaUsage
	if err := remarshalOpenAIQuotaPayload(raw, &usage); err != nil {
		return nil, err
	}
	usage.FetchedAt = time.Now().Unix()
	// Merge optional detail observations after successful main decoding. Presence
	// flags preserve an explicit empty list/count independently, preventing stale
	// main credits from surviving a known replacement observation.
	details := s.queryResetCreditDetails(ctx, providerCtx)
	if details != nil {
		hasDetailCount := details.AvailableCount != nil
		if usage.RateLimitResetCredits == nil {
			usage.RateLimitResetCredits = &openai.OpenAIRateLimitResetCredits{}
		}
		if details.CreditListPresent {
			usage.RateLimitResetCredits.Credits = details.Credits
		}
		switch {
		case hasDetailCount:
			usage.RateLimitResetCredits.AvailableCount = *details.AvailableCount
		case details.CreditListPresent:
			usage.RateLimitResetCredits.AvailableCount = details.AvailableCreditCount
		}
	}
	return &usage, nil
}

// CacheResetCreditsSnapshot tracks a cache write using caller/shutdown
// cancellation. Missing credits or positive count without details rejects the
// write while preserving durable data; accepted credits remain borrowed
// through SaveExtra, whose failure becomes a cache-write error.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OpenAIQuotaService) CacheResetCreditsSnapshot(ctx context.Context, providerID int64, credits *openai.OpenAIRateLimitResetCredits) error {
	// Register before snapshot validation and persistence so shutdown can cancel
	// admitted cache work and wait through its SaveExtra callback until deferred
	// completion releases ownership of the write.
	ctx, done, activityErr := s.activity.begin(ctx, ErrOpenAIQuotaStopped)
	if activityErr != nil {
		return activityErr
	}
	defer done()

	return s.cacheResetCreditsSnapshot(ctx, providerID, credits, nil)
}

// CachePostResetSnapshot tracks one cache patch for credits and normalized
// post-reset usage. Nil usage or incomplete positive credits rejects the
// write; usage remains stable through conversion and its credit object is
// borrowed by SaveExtra.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OpenAIQuotaService) CachePostResetSnapshot(ctx context.Context, providerID int64, usage *openai.OpenAIQuotaUsage) error {
	// Register before snapshot validation and persistence so shutdown can cancel
	// admitted cache work and wait through its SaveExtra callback until deferred
	// completion releases ownership of the write.
	ctx, done, activityErr := s.activity.begin(ctx, ErrOpenAIQuotaStopped)
	if activityErr != nil {
		return activityErr
	}
	defer done()

	if usage == nil {
		return s.cacheResetCreditsSnapshot(ctx, providerID, nil, nil)
	}
	return s.cacheResetCreditsSnapshot(
		ctx,
		providerID,
		usage.RateLimitResetCredits,
		BuildOpenAIAutoResetUsageUpdates(usage, time.Now()),
	)
}

// BuildOpenAIAutoResetUsageUpdates converts stable usage windows into shared
// Codex Extra fields. Nil usage or rate limits returns nil; supplied now
// records a UTC observation timestamp.
//
// Primary and secondary observations first retain their percentages, relative
// reset seconds, and whole-minute durations. The shared normalizer then
// assigns short/long windows, rather than assuming primary always means
// FiveHour. Integer conversions are unvalidated, and division drops partial
// minutes before that classification. BuildCodexUsageExtraUpdates derives
// absolute resets from the chosen observation time and returns scalar cache
// fields for later display.
func BuildOpenAIAutoResetUsageUpdates(usage *openai.OpenAIQuotaUsage, now time.Time) map[string]any {
	if usage == nil || usage.RateLimit == nil {
		return nil
	}
	snapshot := &openai.OpenAICodexUsageSnapshot{UpdatedAt: now.UTC().Format(time.RFC3339)}
	applyWindow := func(window *openai.OpenAIRateLimitWindow, primary bool) {
		if window == nil {
			return
		}
		used := window.UsedPercent
		resetAfter := int(window.ResetAfterSeconds)
		// Drop partial minutes before shared duration-based window classification.
		windowMinutes := int(window.LimitWindowSeconds / 60)
		if primary {
			snapshot.PrimaryUsedPercent = &used
			snapshot.PrimaryResetAfterSeconds = &resetAfter
			snapshot.PrimaryWindowMinutes = &windowMinutes
		} else {
			snapshot.SecondaryUsedPercent = &used
			snapshot.SecondaryResetAfterSeconds = &resetAfter
			snapshot.SecondaryWindowMinutes = &windowMinutes
		}
	}
	applyWindow(usage.RateLimit.PrimaryWindow, true)
	applyWindow(usage.RateLimit.SecondaryWindow, false)
	return BuildCodexUsageExtraUpdates(snapshot, now)
}

// cacheResetCreditsSnapshot validates credit detail completeness before
// requesting one cache patch. Nil credits or positive count with empty details
// rejects the observation, preserving durable data by skipping SaveExtra.
//
// A nil updates map is allocated; otherwise the supplied mutable map is
// augmented directly with the borrowed credits pointer before persistence.
// SaveExtra is required and receives that combined patch, with failure wrapped
// as a cache error while the caller map retains its inserted key. This method
// performs no defensive credit copy or observation-identity comparison;
// callers synchronize shared inputs and the configured writer owns actual
// persistence behavior.
func (s *OpenAIQuotaService) cacheResetCreditsSnapshot(ctx context.Context, providerID int64, credits *openai.OpenAIRateLimitResetCredits, updates map[string]any) error {
	if credits == nil || (credits.AvailableCount > 0 && len(credits.Credits) == 0) {
		return apperror.New(
			502,
			"OPENAI_QUOTA_RESET_CREDITS_REFRESH_FAILED",
			"failed to refresh reset-credit expiration details; cached data was preserved",
		)
	}
	if s == nil || s.Options.SaveExtra == nil {
		return apperror.InternalServer("OPENAI_QUOTA_NOT_CONFIGURED", "openai quota cache repository is not configured")
	}
	if updates == nil {
		updates = make(map[string]any, 1)
	}
	// Augment the borrowed patch before the single persistence call. Write failure
	// leaves this local insertion intact while no rollback or identity comparison
	// is performed by this layer.
	updates[openaiQuotaResetCreditsKey] = credits
	if err := s.Options.SaveExtra(ctx, providerID, updates); err != nil {
		return apperror.New(
			500,
			"OPENAI_QUOTA_CACHE_WRITE_FAILED",
			"failed to cache reset-credit details",
		).WithCause(err)
	}
	return nil
}

// queryResetCreditDetails returns optional parsed details for a stable
// prepared client. Transport failure is diagnosed through required Warn and
// becomes nil, allowing the main usage query to survive.
//
// The protocol parser keeps an independently valid count when a credit list is
// malformed. Such an error is diagnosed, but that count can still replace the
// main count; without count it contributes nothing. Successful absence of both
// count and list also returns nil, while an explicit empty list remains
// present and can replace main credits. This method distinguishes missing
// observations from available zero/list absence rather than inferring credit
// eligibility from query success.
func (s *OpenAIQuotaService) queryResetCreditDetails(ctx context.Context, providerCtx *PreparedOpenAIQuota) *openai.OpenAIRateLimitResetCreditDetails {
	// Treat failed detail queries as optional observations, retaining decoded
	// main usage. The parser can preserve an independent count while rejecting
	// a malformed credit list below.
	raw, err := providerCtx.Client.GetJSONRaw(ctx, chatGPTRateLimitCreditsPath, nil)
	if err != nil {
		s.Options.Warn("openai_quota_reset_credit_details_failed", "provider_id", providerCtx.Provider.ID, "error", err)
		return nil
	}
	details, err := openai.ParseOpenAIRateLimitResetCreditDetails(raw)
	if err != nil {
		s.Options.Warn("openai_quota_reset_credit_details_parse_failed", "provider_id", providerCtx.Provider.ID, "error", err)
		// Retain an independently parsed count despite list failure so it can replace
		// stale main counts. The parser withholds malformed-list presence and data.
		if details.AvailableCount == nil {
			return nil
		}
	}
	if details.AvailableCount == nil && !details.CreditListPresent {
		return nil
	}
	return &details
}

// ResetCredit consumes one credit using caller/shutdown cancellation and a
// generated request ID. Initial credential shadows are rejected before
// preparation; reached RedeemID, client, and Info dependencies are required.
//
// Provider preparation rereads the row after the initial shadow guard, so
// those reads do not form an atomic admission snapshot. One ID is generated
// per call with no local retry or deduplication across calls. Post failure
// propagates, and successful upstream consumption is followed by JSON
// conversion before returning its result. Conversion failure can therefore
// occur after the credit action; this method performs no rollback, runtime
// recovery, or cache refresh for that accepted upstream work.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OpenAIQuotaService) ResetCredit(ctx context.Context, providerID int64) (*openai.OpenAIQuotaResetResult, error) {
	// Register before provider/client work so shutdown rejects new requests and
	// cancels this admitted operation until its deferred completion releases
	// ownership of the tracked lifetime.
	ctx, done, activityErr := s.activity.begin(ctx, ErrOpenAIQuotaStopped)
	if activityErr != nil {
		return nil, activityErr
	}
	defer done()

	provider, err := s.LoadProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	// Reject consumption on the initially loaded shadow because its quota queries
	// use parent credentials. Preparation rereads afterward, so this guard does
	// not freeze identity against a concurrent row change.
	if provider.IsCredentialShadow() {
		return nil, ErrSparkShadowResetNotSupported
	}
	providerCtx, err := s.PrepareProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	redeemRequestID, err := s.Options.RedeemID()
	if err != nil {
		return nil, apperror.Newf(500, "OPENAI_QUOTA_REDEEM_ID_FAILED", "failed to generate redeem id: %v", err)
	}

	// Generate one ID before consumption and decode its response afterward. A
	// decoding failure can follow an accepted upstream action, so this sequence
	// cannot promise rollback or safe automatic retry.
	raw, err := providerCtx.Client.PostJSON(ctx, chatGPTRateLimitResetPath, map[string]any{
		"redeem_request_id": redeemRequestID,
	})
	if err != nil {
		return nil, err
	}

	var result openai.OpenAIQuotaResetResult
	if err := remarshalOpenAIQuotaPayload(raw, &result); err != nil {
		return nil, err
	}
	s.Options.Info("openai_quota_reset_success",
		"provider_id", providerID,
		"code", result.Code,
		"windows_reset", result.WindowsReset,
	)
	return &result, nil
}

// PrepareProvider resolves an OpenAI OAuth row and a usable account client.
// Configured and Read dependencies must exist, and a successful Client
// callback is required after identity/token validation.
//
// Credential shadows load their parent, rejecting nested shadows and non-OAuth
// parents, then use that parent record for identity, token, and transport.
// Account or organization identity must be nonblank. Agent Identity bypasses
// token acquisition; other rows prefer optional Token output and fall back to
// stored access text when blank. Returned record/client remain borrowed, with
// callers keeping them stable through requests. Preparation itself is
// untracked, so direct callers own its lifetime and shutdown coordination.
func (s *OpenAIQuotaService) PrepareProvider(ctx context.Context, providerID int64) (*PreparedOpenAIQuota, error) {
	if s == nil || s.Options.Configured == nil || !s.Options.Configured() {
		return nil, apperror.InternalServer("OPENAI_QUOTA_NOT_CONFIGURED", "openai quota service is not configured")
	}
	provider, err := s.LoadProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if !provider.IsOpenAIOAuth() {
		return nil, apperror.BadRequest("OPENAI_QUOTA_UNSUPPORTED_PROVIDER", "only OpenAI OAuth providers support quota reset")
	}

	// Resolve the parent before choosing account identity, token, and client so
	// the query uses one effective credential owner rather than mixing shadow
	// credentials with parent transport inputs.
	if provider.IsCredentialShadow() {
		parent, resolveErr := s.LoadProvider(ctx, *provider.ParentProviderID)
		if resolveErr != nil {
			return nil, apperror.Newf(502, "OPENAI_QUOTA_SHADOW_RESOLVE_FAILED", "failed to resolve shadow provider: %v", resolveErr)
		}
		if parent.IsCredentialShadow() {
			return nil, apperror.Newf(502, "OPENAI_QUOTA_SHADOW_RESOLVE_FAILED", "spark shadow parent %d is itself a shadow", parent.ID)
		}
		if !parent.IsOpenAIOAuth() {
			return nil, apperror.Newf(502, "OPENAI_QUOTA_SHADOW_RESOLVE_FAILED", "spark shadow parent %d is not OpenAI OAuth", parent.ID)
		}
		provider = parent
	}

	if strings.TrimSpace(provider.GetChatGPTAccountID()) == "" && strings.TrimSpace(provider.GetCredential("organization_id")) == "" {
		return nil, apperror.BadRequest("OPENAI_QUOTA_MISSING_PROVIDER_ID", "chatgpt_account_id is missing; please re-authorize this provider")
	}

	// Agent Identity delegates authentication to its client. Ordinary OAuth uses
	// supplied token acquisition before stored text, keeping both choices on the
	// effective parent record after shadow resolution.
	token := ""
	if !provider.IsOpenAIAgentIdentity() && s.Options.Token != nil {
		token, err = s.Options.Token(ctx, provider)
		if err != nil {
			return nil, err
		}
	}
	if !provider.IsOpenAIAgentIdentity() && strings.TrimSpace(token) == "" {
		token = provider.GetOpenAIAccessToken()
	}
	if !provider.IsOpenAIAgentIdentity() && strings.TrimSpace(token) == "" {
		return nil, apperror.BadRequest("OPENAI_QUOTA_MISSING_TOKEN", "missing OpenAI OAuth access token")
	}

	client, err := s.Options.Client(ctx, provider, token)
	if err != nil {
		return nil, err
	}
	return &PreparedOpenAIQuota{Provider: provider, Token: token, Client: client}, nil
}

// LoadProvider returns the configured reader's borrowed record for the
// requested ID. Missing reader, lookup failure, or nil successful result
// produces an error; callers keep returned mutable values stable during later
// preparation or requests.
func (s *OpenAIQuotaService) LoadProvider(ctx context.Context, providerID int64) (*Record, error) {
	if s == nil || s.Options.Read == nil {
		return nil, apperror.InternalServer("OPENAI_QUOTA_NOT_CONFIGURED", "openai quota service is not configured")
	}
	provider, err := s.Options.Read(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, apperror.NotFound("PROVIDER_NOT_FOUND", "provider not found")
	}
	return provider, nil
}

// BuildCodexSparkWindowExtraUpdates extracts shared Codex fields from the
// first codex_bengalfox additional limit. Nil usage, no matching entry, or nil
// rate limits on that first match returns nil, ignoring any later duplicate
// match.
//
// Window duration is truncated to whole minutes before shared normalization
// decides FiveHour and SevenDay placement. Relative seconds derive absolute
// reset text in supplied now's time zone, while the update timestamp retains
// that same RFC 3339 offset. Negative reset counts remain in relative fields
// but absolute timing clamps them to zero through CodexResetAtRFC3339. Scalar
// output uses ordinary codex_* keys read by shadow display/scheduling. Callers
// keep usage stable and provide representable reset intervals; this
// constructor does not persist or merge its observation.
func BuildCodexSparkWindowExtraUpdates(usage *openai.OpenAIQuotaUsage, now time.Time) map[string]any {
	if usage == nil {
		return nil
	}
	var spark *openai.OpenAIRateLimit
	for i := range usage.AdditionalRateLimits {
		a := usage.AdditionalRateLimits[i]
		if a.MeteredFeature == "codex_bengalfox" {
			spark = a.RateLimit
			break
		}
	}
	if spark == nil {
		return nil
	}

	// Preserve paired percent/reset/duration observations before normalization so
	// a longer primary window can map to SevenDay and a shorter secondary to
	// FiveHour without losing their associated reset timing.
	snap := &openai.OpenAICodexUsageSnapshot{}
	if w := spark.PrimaryWindow; w != nil {
		p := w.UsedPercent
		snap.PrimaryUsedPercent = &p
		ra := int(w.ResetAfterSeconds)
		snap.PrimaryResetAfterSeconds = &ra
		// Drop partial minutes before shared duration-based window classification.
		wm := int(w.LimitWindowSeconds / 60)
		snap.PrimaryWindowMinutes = &wm
	}
	if w := spark.SecondaryWindow; w != nil {
		p := w.UsedPercent
		snap.SecondaryUsedPercent = &p
		ra := int(w.ResetAfterSeconds)
		snap.SecondaryResetAfterSeconds = &ra
		// Drop partial minutes before shared duration-based window classification.
		wm := int(w.LimitWindowSeconds / 60)
		snap.SecondaryWindowMinutes = &wm
	}

	normalized := snap.Normalize()
	if normalized == nil {
		return nil
	}

	updates := make(map[string]any)
	addCodexNormalizedWindowExtraUpdates(updates, normalized, now)
	if len(updates) == 0 {
		return nil
	}
	updates["codex_usage_updated_at"] = now.Format(time.RFC3339)
	return updates
}

// StopContext rejects later tracked quota operations, cancels active work, and
// waits within ctx. The first shutdown result, including an unfinished-work
// timeout, remains retained on subsequent calls; direct preparation is outside
// this registration.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OpenAIQuotaService) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "OpenAIQuotaService")
}
