package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

const (
	ForbiddenTypeValidation = "validation"
	ForbiddenTypeViolation  = "violation"
	ForbiddenTypeForbidden  = "forbidden"

	// Machine-readable error codes.
	ErrorCodeForbidden       = "forbidden"
	ErrorCodeUnauthenticated = "unauthenticated"
	ErrorCodeRateLimited     = "rate_limited"
	ErrorCodeNetworkError    = "network_error"
)

type AntigravityQuotaClient interface {
	FetchAvailableModels(context.Context, string, string, int64) (*google.AntigravityFetchAvailableModelsResponse, map[string]any, error)
	LoadCodeAssist(context.Context, string) (*google.AntigravityLoadCodeAssistResponse, map[string]any, error)
}
type AntigravityQuotaOptions struct {
	NewClient                        func(string) (AntigravityQuotaClient, error)
	ReadLimit                        func() int64
	ResolveProxy                     func(context.Context, int64) (string, bool)
	ForbiddenBody                    func(error) (string, bool)
	ClassifyForbidden, ValidationURL func(string) string
	Warn                             func(string, ...any)
}
type AntigravityQuota struct{ Options AntigravityQuotaOptions }

// CanFetch reports whether the provider is Antigravity and has an access token.
func (f *AntigravityQuota) CanFetch(provider *Record) bool {
	if provider.Platform != PlatformAntigravity {
		return false
	}
	accessToken := provider.GetCredential("access_token")
	return accessToken != ""
}

// FetchQuota fetches Antigravity model quotas and available subscription metadata.
func (f *AntigravityQuota) FetchQuota(ctx context.Context, provider *Record, proxyURL string) (*QuotaResult, error) {
	accessToken := provider.GetCredential("access_token")
	projectID := provider.GetCredential("project_id")

	client, err := f.Options.NewClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create antigravity client failed: %w", err)
	}

	// Fetch model quotas with the configured response-size limit.
	modelsResp, modelsRaw, err := client.FetchAvailableModels(ctx, accessToken, projectID, f.Options.ReadLimit())
	if err != nil {
		// Represent a forbidden response as usage state instead of a fetch error.
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

	// Subscription and AI Credits discovery is best effort; model quotas remain usable on failure.
	tierRaw, tierNormalized, loadResp := f.fetchSubscriptionTier(ctx, client, accessToken)

	// Convert quota and subscription responses for display.
	usageInfo := f.BuildUsageInfo(modelsResp, tierRaw, tierNormalized, loadResp)

	return &QuotaResult{
		UsageInfo: usageInfo,
		Raw:       modelsRaw,
	}, nil
}

// fetchSubscriptionTier returns raw and normalized tiers, or empty strings on failure.
// It also returns the response so callers can read AI Credits.
func (f *AntigravityQuota) fetchSubscriptionTier(ctx context.Context, client AntigravityQuotaClient, accessToken string) (raw, normalized string, loadResp *google.AntigravityLoadCodeAssistResponse) {
	loadResp, _, err := client.LoadCodeAssist(ctx, accessToken)
	if err != nil {
		f.Options.Warn("failed to fetch subscription tier", "error", err)
		return "", "", nil
	}
	if loadResp == nil {
		return "", "", nil
	}

	raw = loadResp.GetTier() // Prefer paidTier over currentTier.
	normalized = NormalizeAntigravityTier(raw)
	return raw, normalized, loadResp
}

// NormalizeAntigravityTier returns FREE, PRO, ULTRA, UNKNOWN, or empty for an empty input.
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

// BuildUsageInfo converts quota and subscription responses to usage display data.
func (f *AntigravityQuota) BuildUsageInfo(modelsResp *google.AntigravityFetchAvailableModelsResponse, tierRaw, tierNormalized string, loadResp *google.AntigravityLoadCodeAssistResponse) *UsageInfo {
	now := time.Now()
	info := &UsageInfo{
		UpdatedAt:               &now,
		AntigravityQuota:        make(map[string]*AntigravityModelQuota),
		AntigravityQuotaDetails: make(map[string]*AntigravityModelDetail),
		SubscriptionTier:        tierNormalized,
		SubscriptionTierRaw:     tierRaw,
	}

	// Skip models without quota information.
	for modelName, modelInfo := range modelsResp.Models {
		if modelInfo.QuotaInfo == nil {
			continue
		}

		// Convert the remaining fraction in [0,1] to used percentage.
		utilization := int((1.0 - modelInfo.QuotaInfo.RemainingFraction) * 100)

		info.AntigravityQuota[modelName] = &AntigravityModelQuota{
			Utilization: utilization,
			ResetTime:   modelInfo.QuotaInfo.ResetTime,
		}

		// Copy model capabilities alongside quota information.
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

	// Expose upstream replacements for deprecated model IDs.
	if len(modelsResp.DeprecatedModelIDs) > 0 {
		info.ModelForwardingRules = make(map[string]string, len(modelsResp.DeprecatedModelIDs))
		for oldID, deprecated := range modelsResp.DeprecatedModelIDs {
			info.ModelForwardingRules[oldID] = deprecated.NewModelID
		}
	}

	// Populate the legacy FiveHour display from the first available priority model.
	priorityModels := []string{"claude-sonnet-4-20250514", "claude-sonnet-4", "gemini-2.5-pro"}
	for _, modelName := range priorityModels {
		if modelInfo, ok := modelsResp.Models[modelName]; ok && modelInfo.QuotaInfo != nil {
			utilization := (1.0 - modelInfo.QuotaInfo.RemainingFraction) * 100
			progress := &UsageProgress{
				Utilization: utilization,
			}
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

// GetProxyURL resolves the provider's proxy URL, or returns empty when unavailable.
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
