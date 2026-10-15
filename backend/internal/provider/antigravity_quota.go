package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// antigravity_quota.go fetches Antigravity model quotas and assembles
// utilization, subscription, and credit observations for display.
// Antigravity model quota display combines available-model responses with
// best-effort subscription and paid-tier AI Credits. Forbidden protocol
// failures become account-state display data, while other model fetch errors
// abort and subscription errors leave the successful model observation usable.
//
// Only models with quota contribute capability details. Integer model
// utilization truncates the remaining-fraction conversion without clamping,
// while the compatibility FiveHour view keeps a floating value from the first
// eligible model in a fixed priority list. Capability pointers and MIME maps
// remain borrowed from the response until CloneUsageInfo is used.
// Deprecated-model redirects are copied independently, reset text is retained
// raw per model, and parsed FiveHour countdown can be negative. Callers keep
// typed responses and their nested data stable through display construction.

// Antigravity quota constants distinguish forbidden recovery categories and
// machine-readable failure labels consumed by administration display and
// account recovery decisions.
const (
	// ForbiddenTypeValidation identifies manual account verification required.
	ForbiddenTypeValidation = "validation"
	// ForbiddenTypeViolation identifies account violation recovery category.
	ForbiddenTypeViolation = "violation"
	// ForbiddenTypeForbidden identifies unclassified forbidden account state.
	ForbiddenTypeForbidden = "forbidden"

	// Machine-readable error codes.
	// ErrorCodeForbidden identifies forbidden usage query failure.
	ErrorCodeForbidden = "forbidden"
	// ErrorCodeUnauthenticated identifies authentication usage query failure.
	ErrorCodeUnauthenticated = "unauthenticated"
	// ErrorCodeRateLimited identifies rate-limited usage query failure.
	ErrorCodeRateLimited = "rate_limited"
	// ErrorCodeNetworkError identifies network usage query failure.
	ErrorCodeNetworkError = "network_error"
)

// AntigravityQuotaClient reads bounded model quota and account subscription
// protocol observations.
type AntigravityQuotaClient interface {
	// FetchAvailableModels reads quota with access token, project, and response
	// limit. Return nonnil typed data on success; callers borrow its nested
	// capability values and raw object while constructing display observations.
	FetchAvailableModels(context.Context, string, string, int64) (*google.AntigravityFetchAvailableModelsResponse, map[string]any, error)
	// LoadCodeAssist reads account tier and paid-credit observations for the
	// token.
	// The quota consumer discards partial data on error, while successful nil
	// data means subscription metadata is unavailable beside usable model quotas.
	LoadCodeAssist(context.Context, string) (*google.AntigravityLoadCodeAssistResponse, map[string]any, error)
}

// AntigravityQuotaOptions supplies transport, body limits, forbidden
// classification, and diagnostic callbacks.
type AntigravityQuotaOptions struct {
	// NewClient is required for quota querying and returns a usable client
	// for the supplied proxy URL. Factory error aborts before model requests.
	NewClient func(string) (AntigravityQuotaClient, error)
	// ReadLimit is required for model fetching and supplies its protocol body
	// cap. The platform client owns enforcement and reports bounded-read failures.
	ReadLimit func() int64
	// ResolveProxy optionally resolves selected record transport. Missing or
	// false availability yields empty, with actual fallback owned by the caller's
	// client.
	ResolveProxy func(context.Context, int64) (string, bool)
	// ForbiddenBody is required on model fetch failure and identifies a
	// displayable forbidden response. A nonmatch retains the original query error.
	ForbiddenBody func(error) (string, bool)
	// ClassifyForbidden and ValidationURL are required for recognized forbidden
	// text. Classification chooses verification/violation flags, while URL
	// extraction contributes the administrator's recovery link from that same
	// body.
	// ClassifyForbidden: forbidden category classifier; ValidationURL: recovery
	// URL extractor.
	ClassifyForbidden, ValidationURL func(string) string
	// Warn is required when subscription discovery fails. That diagnostic
	// leaves successful model quota data available while subscription metadata is
	// omitted.
	Warn func(string, ...any)
}

// AntigravityQuota binds model quota and subscription discovery to configured
// protocol dependencies.
type AntigravityQuota struct {
	// Options retains callback values and their shared client dependencies.
	// Configure them before queries and keep borrowed response data stable
	// throughout display conversion.
	Options AntigravityQuotaOptions
}

// CanFetch reports whether a stable nonnil record uses Antigravity with a
// nonempty access-token string. It checks neither credential type nor
// whitespace-only content, so callers retain their own provider admission
// rules.
func (f *AntigravityQuota) CanFetch(provider *Record) bool {
	if provider.Platform != PlatformAntigravity {
		return false
	}
	accessToken := provider.GetCredential("access_token")
	return accessToken != ""
}

// FetchQuota queries models and subscription hints using a stable nonnil
// record and supplied proxy URL. Required factory returns a usable client,
// model success supplies nonnil data, and reached classification/limit
// callbacks are required.
//
// Recognized forbidden errors become a dated UsageInfo with recovery flags and
// nil method error; their raw model response is absent. Other model errors
// propagate. Successful models survive subscription query failure, while a
// successful subscription can add tier and paid credits. Raw is borrowed model
// payload, and capability pointers/maps remain borrowed inside the built view
// until the caller copies it.
func (f *AntigravityQuota) FetchQuota(ctx context.Context, provider *Record, proxyURL string) (*QuotaResult, error) {
	accessToken := provider.GetCredential("access_token")
	projectID := provider.GetCredential("project_id")

	client, err := f.Options.NewClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create antigravity client failed: %w", err)
	}

	// Apply the configured model-response cap before constructing display data.
	// The client rejects oversized or failed bounded reads rather than treating
	// truncated JSON as a successful quota observation.
	modelsResp, modelsRaw, err := client.FetchAvailableModels(ctx, accessToken, projectID, f.Options.ReadLimit())
	if err != nil {
		// Preserve a recognized forbidden body as account-state display data so
		// administration can show its recovery category and URL beside nil method
		// error; unrelated protocol failures still abort the fetch.
		if forbiddenBody, ok := f.Options.ForbiddenBody(err); ok {
			now := time.Now()
			fbType := f.Options.ClassifyForbidden(forbiddenBody)
			return &QuotaResult{
				UsageInfo: &UsageInfo{
					UpdatedAt:       &now,
					IsForbidden:     true,
					ForbiddenReason: forbiddenBody,
					ForbiddenType:   fbType,
					ValidationURL:   f.Options.ValidationURL(forbiddenBody),
					NeedsVerify:     fbType == ForbiddenTypeValidation,
					IsBanned:        fbType == ForbiddenTypeViolation,
					ErrorCode:       ErrorCodeForbidden,
				},
			}, nil
		}
		return nil, err
	}

	// Subscription errors discard that response but retain successful model
	// quotas. The display therefore remains useful while tier/credit discovery
	// is unavailable for this query.
	tierRaw, tierNormalized, loadResp := f.fetchSubscriptionTier(ctx, client, accessToken)

	// Convert quota and subscription responses for display.
	usageInfo := f.BuildUsageInfo(modelsResp, tierRaw, tierNormalized, loadResp)

	return &QuotaResult{
		UsageInfo: usageInfo,
		Raw:       modelsRaw,
	}, nil
}

// fetchSubscriptionTier returns tier labels and successful load data for a
// usable client. Error is diagnosed through required Warn and discards even
// nonnil partial response; nil successful response yields empty metadata,
// while paid tier precedes current tier.
func (f *AntigravityQuota) fetchSubscriptionTier(ctx context.Context, client AntigravityQuotaClient, accessToken string) (raw, normalized string, loadResp *google.AntigravityLoadCodeAssistResponse) {
	// Discard partial subscription data on failure while diagnosing it, because
	// this path displays models independently and only successful tier data
	// contributes credit and subscription metadata.
	loadResp, _, err := client.LoadCodeAssist(ctx, accessToken)
	if err != nil {
		f.Options.Warn("failed to fetch subscription tier", "error", err)
		return "", "", nil
	}
	if loadResp == nil {
		return "", "", nil
	}

	// Prefer a nonempty paid tier ID, then current tier, so a response carrying
	// both observations displays its paid subscription identity.
	raw = loadResp.GetTier()
	normalized = NormalizeAntigravityTier(raw)
	return raw, normalized, loadResp
}

// NormalizeAntigravityTier returns the first matching lowercase substring
// category as ULTRA, PRO, then FREE. Exactly empty input stays empty; other
// text, including unrecognized whitespace-only input, returns UNKNOWN without
// trimming or account eligibility validation.
func NormalizeAntigravityTier(raw string) string {
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "ultra"):
		return "ULTRA"
	case strings.Contains(lower, "pro"):
		return "PRO"
	case strings.Contains(lower, "free"):
		return "FREE"
	default:
		return "UNKNOWN"
	}
}

// BuildUsageInfo creates quota/display maps from stable nonnil model data and
// optional successful subscription. Caller-supplied tier strings are retained
// independently of the response.
//
// Models without quota are omitted from both quota and capability maps. Model
// percentage truncates without clamping, while FiveHour selects its first
// eligible fixed-priority model and keeps floating utilization. Capability
// pointers and MIME maps remain borrowed; redirects and credit structs are
// newly allocated scalar data. Reset parsing failure retains utilization with
// unknown timing, and parsed countdowns can be negative. Credit numeric
// getters can accept partial text, so display conversion does not establish
// whole-field validity.
func (f *AntigravityQuota) BuildUsageInfo(modelsResp *google.AntigravityFetchAvailableModelsResponse, tierRaw, tierNormalized string, loadResp *google.AntigravityLoadCodeAssistResponse) *UsageInfo {
	now := time.Now()
	info := &UsageInfo{
		UpdatedAt:               &now,
		AntigravityQuota:        make(map[string]*AntigravityModelQuota),
		AntigravityQuotaDetails: make(map[string]*AntigravityModelDetail),
		SubscriptionTier:        tierNormalized,
		SubscriptionTierRaw:     tierRaw,
	}

	// Quota presence controls both display maps. Omit capabilities for models
	// lacking quota so each detail entry remains paired with an available quota
	// observation.
	for modelName, modelInfo := range modelsResp.Models {
		if modelInfo.QuotaInfo == nil {
			continue
		}

		// The integer display receives the converted fraction directly. Finite
		// representable values truncate; bounds and nonfinite observations are not
		// validated or clamped before conversion.
		utilization := int((1.0 - modelInfo.QuotaInfo.RemainingFraction) * 100)

		info.AntigravityQuota[modelName] = &AntigravityModelQuota{
			Utilization: utilization,
			ResetTime:   modelInfo.QuotaInfo.ResetTime,
		}

		// Retain capability values beside the same model quota. Pointer fields and
		// MIME maps remain borrowed from the response, so independent mutation needs
		// CloneUsageInfo after this construction.
		detail := &AntigravityModelDetail{
			DisplayName:        modelInfo.DisplayName,
			SupportsImages:     modelInfo.SupportsImages,
			SupportsThinking:   modelInfo.SupportsThinking,
			ThinkingBudget:     modelInfo.ThinkingBudget,
			Recommended:        modelInfo.Recommended,
			MaxTokens:          modelInfo.MaxTokens,
			MaxOutputTokens:    modelInfo.MaxOutputTokens,
			SupportedMimeTypes: modelInfo.SupportedMimeTypes,
		}
		info.AntigravityQuotaDetails[modelName] = detail
	}

	// Copy deprecated-model replacements into a separate map so later display
	// edits can change redirects independently of the fetched response values.
	if len(modelsResp.DeprecatedModelIDs) > 0 {
		info.ModelForwardingRules = make(map[string]string, len(modelsResp.DeprecatedModelIDs))
		for oldID, deprecated := range modelsResp.DeprecatedModelIDs {
			info.ModelForwardingRules[oldID] = deprecated.NewModelID
		}
	}

	// Select the first quota-bearing fixed-priority model for compatibility
	// FiveHour display. Its floating utilization remains distinct from truncated
	// per-model percentages, and unmatched models cannot populate this view.
	priorityModels := []string{"claude-sonnet-4-20250514", "claude-sonnet-4", "gemini-2.5-pro"}
	for _, modelName := range priorityModels {
		if modelInfo, ok := modelsResp.Models[modelName]; ok && modelInfo.QuotaInfo != nil {
			utilization := (1.0 - modelInfo.QuotaInfo.RemainingFraction) * 100
			progress := &UsageProgress{
				Utilization: utilization,
			}
			// Keep utilization when reset parsing fails, leaving unknown timing rather
			// than discarding the selected model. Parsed countdowns retain their signed
			// whole seconds even after an upstream reset has passed.
			if modelInfo.QuotaInfo.ResetTime != "" {
				if resetTime, err := time.Parse(time.RFC3339, modelInfo.QuotaInfo.ResetTime); err == nil {
					progress.ResetsAt = &resetTime
					progress.RemainingSeconds = int(time.Until(resetTime).Seconds())
				}
			}
			info.FiveHour = progress
			break
		}
	}

	// Credit getters scan one numeric value and suppress parse failure to zero,
	// so this display conversion retains successful model data even with
	// unusable credit text and does not validate whole numeric fields.
	if loadResp != nil {
		for _, credit := range loadResp.GetAvailableCredits() {
			info.AICredits = append(info.AICredits, AICredit{
				CreditType:     credit.CreditType,
				Amount:         credit.GetAmount(),
				MinimumBalance: credit.GetMinimumAmount(),
			})
		}
	}

	return info
}

// GetProxyURL returns resolved transport for a stable nonnil record or empty
// when no selection/resolver exists. False availability also returns empty,
// leaving the caller to choose its configured protocol transport fallback.
func (f *AntigravityQuota) GetProxyURL(ctx context.Context, provider *Record) string {
	if provider.ProxyID == nil || f.Options.ResolveProxy == nil {
		return ""
	}
	value, ok := f.Options.ResolveProxy(ctx, *provider.ProxyID)
	if !ok {
		return ""
	}
	return value
}
