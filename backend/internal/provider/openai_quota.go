package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const (
	chatGPTUsagePath                   = "/wham/usage"
	chatGPTRateLimitCreditsPath        = "/wham/rate-limit-reset-credits"
	chatGPTRateLimitResetPath          = "/wham/rate-limit-reset-credits/consume"
	codexInviteResetSupportsRewardless = "true"
	openaiQuotaResetCreditsKey         = "codex_reset_credit_snapshot"
)

var (
	ErrSparkShadowResetNotSupported = apperror.New(409, "SPARK_SHADOW_RESET_NOT_SUPPORTED", "spark shadow provider does not support credit reset; reset the parent provider")
	ErrOpenAIQuotaStopped           = apperror.New(503, "OPENAI_QUOTA_STOPPED", "openai quota service is stopped")
)

type OpenAIQuotaClient interface {
	GetJSON(context.Context, string, map[string]string) (map[string]any, error)
	GetJSONRaw(context.Context, string, map[string]string) ([]byte, error)
	PostJSON(context.Context, string, map[string]any) (map[string]any, error)
}

type PreparedOpenAIQuota struct {
	Provider *Record           `json:"-"`
	Token    string            `json:"-"`
	Client   OpenAIQuotaClient `json:"-"`
}

type OpenAIQuotaOptions struct {
	Configured func() bool
	Read       func(context.Context, int64) (*Record, error)
	Token      func(context.Context, *Record) (string, error)
	Client     func(context.Context, *Record, string) (OpenAIQuotaClient, error)
	SaveExtra  func(context.Context, int64, map[string]any) error
	RedeemID   func() (string, error)
	Warn, Info func(string, ...any)
}

type OpenAIQuotaService struct {
	Options  OpenAIQuotaOptions
	activity operationActivity
}

// QuotaResult contains normalized usage and the upstream response.
type QuotaResult struct {
	UsageInfo *UsageInfo     // Normalized usage information.
	Raw       map[string]any // Raw response suitable for provider Extra.
}

func (p PreparedOpenAIQuota) String() string { return "prepared OpenAI quota request" }

func NewOpenAIQuotaService(options OpenAIQuotaOptions) *OpenAIQuotaService {
	return &OpenAIQuotaService{Options: options}
}

func remarshalOpenAIQuotaPayload(raw map[string]any, target any) error {
	body, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, target)
}

// QueryUsage fetches current upstream rate-limit windows and available reset credits.
func (s *OpenAIQuotaService) QueryUsage(ctx context.Context, providerID int64) (*openai.OpenAIQuotaUsage, error) {
	ctx, done, activityErr := s.activity.begin(ctx, ErrOpenAIQuotaStopped)
	if activityErr != nil {
		return nil, activityErr
	}
	defer done()

	providerCtx, err := s.PrepareProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
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

// CacheResetCreditsSnapshot stores a complete reset-credit observation.
// Positive counts require credit details so expiry can be reflected by clients.
func (s *OpenAIQuotaService) CacheResetCreditsSnapshot(ctx context.Context, providerID int64, credits *openai.OpenAIRateLimitResetCredits) error {
	ctx, done, activityErr := s.activity.begin(ctx, ErrOpenAIQuotaStopped)
	if activityErr != nil {
		return activityErr
	}
	defer done()

	return s.cacheResetCreditsSnapshot(ctx, providerID, credits, nil)
}

// CachePostResetSnapshot stores observed reset credits and usage windows after a reset.
func (s *OpenAIQuotaService) CachePostResetSnapshot(ctx context.Context, providerID int64, usage *openai.OpenAIQuotaUsage) error {
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

// BuildOpenAIAutoResetUsageUpdates builds five-hour and seven-day cache updates after a reset.
// The caller persists these fields for subsequent provider usage views.
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

func (s *OpenAIQuotaService) queryResetCreditDetails(ctx context.Context, providerCtx *PreparedOpenAIQuota) *openai.OpenAIRateLimitResetCreditDetails {
	raw, err := providerCtx.Client.GetJSONRaw(ctx, chatGPTRateLimitCreditsPath, nil)
	if err != nil {
		s.Options.Warn("openai_quota_reset_credit_details_failed", "provider_id", providerCtx.Provider.ID, "error", err)
		return nil
	}
	details, err := openai.ParseOpenAIRateLimitResetCreditDetails(raw)
	if err != nil {
		s.Options.Warn("openai_quota_reset_credit_details_parse_failed", "provider_id", providerCtx.Provider.ID, "error", err)
		// A malformed list can still supply an independently valid count; discard the malformed list.
		if details.AvailableCount == nil {
			return nil
		}
	}
	if details.AvailableCount == nil && !details.CreditListPresent {
		return nil
	}
	return &details
}

// ResetCredit consumes a credit to reset upstream rate-limit windows.
func (s *OpenAIQuotaService) ResetCredit(ctx context.Context, providerID int64) (*openai.OpenAIQuotaResetResult, error) {
	ctx, done, activityErr := s.activity.begin(ctx, ErrOpenAIQuotaStopped)
	if activityErr != nil {
		return nil, activityErr
	}
	defer done()

	provider, err := s.LoadProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	// Shadows use parent credentials; credit-reset requests must target the parent.
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

// BuildCodexSparkWindowExtraUpdates extracts Spark windows from /wham/usage additional_rate_limits.
// It uses codex_* keys on the shadow record so scheduling and display use the same field names.
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

	// Use the shared normalizer to assign primary and secondary windows to five-hour and seven-day fields.
	snap := &openai.OpenAICodexUsageSnapshot{}
	if w := spark.PrimaryWindow; w != nil {
		p := w.UsedPercent
		snap.PrimaryUsedPercent = &p
		ra := int(w.ResetAfterSeconds)
		snap.PrimaryResetAfterSeconds = &ra
		wm := int(w.LimitWindowSeconds / 60)
		snap.PrimaryWindowMinutes = &wm
	}
	if w := spark.SecondaryWindow; w != nil {
		p := w.UsedPercent
		snap.SecondaryUsedPercent = &p
		ra := int(w.ResetAfterSeconds)
		snap.SecondaryResetAfterSeconds = &ra
		wm := int(w.LimitWindowSeconds / 60)
		snap.SecondaryWindowMinutes = &wm
	}

	normalized := snap.Normalize()
	if normalized == nil {
		return nil
	}

	updates := make(map[string]any)
	if normalized.Used5hPercent != nil {
		updates["codex_5h_used_percent"] = *normalized.Used5hPercent
	}
	if normalized.Reset5hSeconds != nil {
		updates["codex_5h_reset_after_seconds"] = *normalized.Reset5hSeconds
	}
	if normalized.Window5hMinutes != nil {
		updates["codex_5h_window_minutes"] = *normalized.Window5hMinutes
	}
	if normalized.Used7dPercent != nil {
		updates["codex_7d_used_percent"] = *normalized.Used7dPercent
	}
	if normalized.Reset7dSeconds != nil {
		updates["codex_7d_reset_after_seconds"] = *normalized.Reset7dSeconds
	}
	if normalized.Window7dMinutes != nil {
		updates["codex_7d_window_minutes"] = *normalized.Window7dMinutes
	}
	if r := CodexResetAtRFC3339(now, normalized.Reset5hSeconds); r != nil {
		updates["codex_5h_reset_at"] = *r
	}
	if r := CodexResetAtRFC3339(now, normalized.Reset7dSeconds); r != nil {
		updates["codex_7d_reset_at"] = *r
	}
	if len(updates) == 0 {
		return nil
	}
	updates["codex_usage_updated_at"] = now.Format(time.RFC3339)
	return updates
}

func (s *OpenAIQuotaService) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "OpenAIQuotaService")
}
