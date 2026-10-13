package openai

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/imroc/req/v3"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// ChatGPT account metadata can contain personal and workspace entries under
// different IDs. Account lookup prefers the requested organization when it is
// usable, then selects a usable default account, a paid account, or any
// remaining account while keeping plan, expiry, and source ID from one
// candidate.
//
// Deactivation markers exclude candidates, and a parseable expiry must be later
// than the observation time. Missing or malformed expiry remains usable because
// lookup cannot infer expiration from that value. Request and parsing failures
// return empty optional metadata for the provider import path to handle.
// Subscription lookup uses an explicit account ID and validates active_until
// separately. Training preference updates distinguish success, ordinary
// failure, and recognized Cloudflare blocking.

// Privacy labels distinguish disabled training, failed preference updates, and
// recognized Cloudflare blocking when provider imports consume optional settings results.
const (
	PrivacyModeTrainingOff = "training_off"
	PrivacyModeFailed      = "training_set_failed"
	PrivacyModeCFBlocked   = "training_set_cf_blocked"
)

type (
	// PrivacyClientFactory supplies a proxy-configured req client for optional
	// ChatGPT metadata exchanges.
	PrivacyClientFactory func(string) (*req.Client, error)
	// ChatGPTAccountInfo names the protocol metadata extracted from a selected
	// ChatGPT account.
	ChatGPTAccountInfo = wire.ChatGPTAccountInfo
	// PrivacyEndpoints supplies the configured ChatGPT settings, account, and
	// subscription URLs.
	PrivacyEndpoints struct {
		// Settings updates training preferences, Providers reads account entries, and
		// Subscriptions reads account-specific expiry. These URLs use the supplied
		// bearer token and proxy client factory in the respective privacy methods.
		Settings, Providers, Subscriptions string
	}
	// PrivacyClient performs optional account metadata and training-preference
	// exchanges through configured endpoints.
	PrivacyClient struct {
		Endpoints PrivacyEndpoints // selected ChatGPT endpoint URLs
	}
)

// DisableOpenAITraining requests training_allowed=false within a fifteen-second
// context. Missing token or client factory returns empty text; success returns
// training_off, and transport/status failures return a failure label with
// recognized blocking distinguished separately.
func (p PrivacyClient) DisableOpenAITraining(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL string) string {
	if accessToken == "" || clientFactory == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	// Release this optional metadata request budget when its operation returns,
	// so the local timeout does not outlive the provider import or preference update.
	defer cancel()

	// Client construction, endpoint status, and response parsing belong to this
	// optional exchange; their logged failures produce its declared unavailable metadata or preference result.
	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Warn("openai_privacy_client_error", "error", err.Error())
		return PrivacyModeFailed
	}

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetHeader("sec-fetch-mode", "cors").
		SetHeader("sec-fetch-site", "same-origin").
		SetHeader("sec-fetch-dest", "empty").
		SetQueryParam("feature", "training_allowed").
		SetQueryParam("value", "false").
		Patch(p.Endpoints.Settings)
	if err != nil {
		slog.Warn("openai_privacy_request_error", "error", err.Error())
		return PrivacyModeFailed
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		body := resp.String()
		if strings.Contains(body, "cloudflare") || strings.Contains(body, "cf-") || strings.Contains(body, "Just a moment") {
			slog.Warn("openai_privacy_cf_blocked", "status", resp.StatusCode)
			return PrivacyModeCFBlocked
		}
	}

	if !resp.IsSuccessState() {
		slog.Warn("openai_privacy_failed", "status", resp.StatusCode, "body", Truncate(resp.String(), 200))
		return PrivacyModeFailed
	}

	slog.Info("openai_privacy_training_disabled")
	return PrivacyModeTrainingOff
}

// FetchChatGPTAccountInfo returns plan, expiry, and source account ID from a
// usable backend entry. Missing credentials, unusable result shape, request
// failure, or no selected plan returns nil. The supplied client factory and
// context control a fifteen-second request budget.
//
// A usable orgID entry takes precedence. Otherwise selection collects a
// default, first paid, and first arbitrary candidate while filtering
// deactivation and known expired entitlements. Default takes precedence over
// paid, then arbitrary; ties within those categories follow map iteration and
// are not deterministic. Each chosen tuple comes from one entry so account
// identity and subscription metadata stay together.
func (p PrivacyClient) FetchChatGPTAccountInfo(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL, orgID string) *ChatGPTAccountInfo {
	if accessToken == "" || clientFactory == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	// Release this optional metadata request budget when its operation returns,
	// so the local timeout does not outlive the provider import or preference update.
	defer cancel()

	// Client construction, endpoint status, and response parsing belong to this
	// optional exchange; their logged failures produce its declared unavailable metadata or preference result.
	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Debug("chatgpt_account_check_client_error", "error", err.Error())
		return nil
	}

	var result map[string]any
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetSuccessResult(&result).
		Get(p.Endpoints.Providers)
	if err != nil {
		slog.Debug("chatgpt_account_check_request_error", "error", err.Error())
		return nil
	}

	if !resp.IsSuccessState() {
		slog.Debug("chatgpt_account_check_failed", "status", resp.StatusCode, "body", Truncate(resp.String(), 200))
		return nil
	}

	info := &ChatGPTAccountInfo{}

	providers, ok := result["accounts"].(map[string]any)
	if !ok {
		slog.Debug("chatgpt_account_check_no_accounts", "body", Truncate(resp.String(), 300))
		return nil
	}

	// The caller supplies its access-token organization selection. Use that entry
	// when usable so workspace metadata follows the requested account before default selection.
	if orgID != "" {
		if acctRaw, ok := providers[orgID]; ok {
			if acct, ok := acctRaw.(map[string]any); ok {
				if IsUsableChatGPTAccountCandidate(acct, time.Now()) {
					FillProviderInfo(info, acct, orgID)
				}
			}
		}
	}

	// Missing selected-account plan triggers candidate selection. Keep each plan,
	// expiry, and account ID together so the eventual preference cannot mix workspace entries.
	if info.PlanType == "" {
		// candidate keeps the plan, expiry, and account identity from one usable
		// entry so the final preference installs all three values from the same source.
		type candidate struct {
			planType   string // candidate account plan type
			expiresAt  string // candidate entitlement expiry text
			providerID string // candidate source account identifier
		}
		var defaultC, paidC, anyC candidate
		for key, acctRaw := range providers {
			acct, ok := acctRaw.(map[string]any)
			if !ok {
				continue
			}
			if !IsUsableChatGPTAccountCandidate(acct, time.Now()) {
				continue
			}
			planType := ExtractPlanType(acct)
			if planType == "" {
				continue
			}
			ea := ExtractEntitlementExpiresAt(acct)
			id := ChatGPTAccountObjectID(acct, key)
			if anyC.planType == "" {
				anyC = candidate{planType, ea, id}
			}
			if provider, ok := acct["account"].(map[string]any); ok {
				if isDefault, _ := provider["is_default"].(bool); isDefault {
					defaultC = candidate{planType, ea, id}
				}
			}
			if !strings.EqualFold(planType, "free") && paidC.planType == "" {
				paidC = candidate{planType, ea, id}
			}
		}
		// Resolve one candidate tuple after scanning usable entries; its plan, expiry,
		// and account identity move together into the result for downstream account matching.
		switch {
		case defaultC.planType != "":
			info.PlanType, info.SubscriptionExpiresAt, info.ProviderID = defaultC.planType, defaultC.expiresAt, defaultC.providerID
		case paidC.planType != "":
			info.PlanType, info.SubscriptionExpiresAt, info.ProviderID = paidC.planType, paidC.expiresAt, paidC.providerID
		default:
			info.PlanType, info.SubscriptionExpiresAt, info.ProviderID = anyC.planType, anyC.expiresAt, anyC.providerID
		}
	}

	if info.PlanType == "" {
		slog.Debug("chatgpt_account_check_no_plan_type", "body", Truncate(resp.String(), 300))
		return nil
	}

	slog.Info("chatgpt_account_check_success", "plan_type", info.PlanType, "subscription_expires_at", info.SubscriptionExpiresAt, "org_id", orgID)
	return info
}

// FetchChatGPTSubscriptionExpiresAt requests active_until for the trimmed
// providerID within a fifteen-second context. Missing inputs, request failures,
// absent timestamps, and invalid RFC3339 values return empty text; success
// returns the trimmed upstream timestamp.
func (p PrivacyClient) FetchChatGPTSubscriptionExpiresAt(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL, providerID string) string {
	providerID = strings.TrimSpace(providerID)
	if accessToken == "" || providerID == "" || clientFactory == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	// Release this optional metadata request budget when its operation returns,
	// so the local timeout does not outlive the provider import or preference update.
	defer cancel()

	// Client construction, endpoint status, and response parsing belong to this
	// optional exchange; their logged failures produce its declared unavailable metadata or preference result.
	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Debug("chatgpt_subscription_client_error", "error", err.Error())
		return ""
	}

	var result struct {
		PlanType    string `json:"plan_type"`    // reported subscription plan type
		ActiveUntil string `json:"active_until"` // reported subscription expiry timestamp
		WillRenew   bool   `json:"will_renew"`   // reported subscription renewal flag
		ID          string `json:"id"`           // reported subscription record identifier
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetSuccessResult(&result).
		SetQueryParam("account_id", providerID).
		Get(p.Endpoints.Subscriptions)
	if err != nil {
		slog.Debug("chatgpt_subscription_request_error", "error", err.Error())
		return ""
	}
	if !resp.IsSuccessState() {
		slog.Debug("chatgpt_subscription_failed", "status", resp.StatusCode, "body", Truncate(resp.String(), 200))
		return ""
	}

	activeUntil := strings.TrimSpace(result.ActiveUntil)
	if activeUntil == "" {
		slog.Debug("chatgpt_subscription_no_active_until", "plan_type", result.PlanType, "has_subscription_id", strings.TrimSpace(result.ID) != "", "will_renew", result.WillRenew)
		return ""
	}
	if _, err := time.Parse(time.RFC3339, activeUntil); err != nil {
		slog.Debug("chatgpt_subscription_bad_active_until", "active_until", activeUntil, "error", err.Error())
		return ""
	}

	slog.Info("chatgpt_subscription_success", "plan_type", result.PlanType, "subscription_expires_at", activeUntil, "account_id", providerID)
	return activeUntil
}

// FillProviderInfo updates a nonnil info with plan, expiry, and account
// identity extracted from acct. Missing nested account ID uses fallbackID
// through ChatGPTAccountObjectID, while plan and expiry remain the supplied
// account values.
func FillProviderInfo(info *ChatGPTAccountInfo, acct map[string]any, fallbackID string) {
	info.PlanType = ExtractPlanType(acct)
	info.SubscriptionExpiresAt = ExtractEntitlementExpiresAt(acct)
	info.ProviderID = ChatGPTAccountObjectID(acct, fallbackID)
}

// ChatGPTAccountObjectID returns a trimmed nonblank nested account_id or the
// trimmed fallbackID. Nil or incomplete account maps use the fallback,
// preserving the source map key when the response omits its own identifier.
func ChatGPTAccountObjectID(acct map[string]any, fallbackID string) string {
	if provider, ok := acct["account"].(map[string]any); ok {
		if id, ok := provider["account_id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return strings.TrimSpace(fallbackID)
}

// ExtractPlanType prefers a nonempty account.plan_type string, then
// entitlement.subscription_plan. It retains supplied spelling and whitespace;
// absent or nonstring values produce empty text for the account-selection
// caller.
func ExtractPlanType(acct map[string]any) string {
	if provider, ok := acct["account"].(map[string]any); ok {
		if planType, ok := provider["plan_type"].(string); ok && planType != "" {
			return planType
		}
	}
	if entitlement, ok := acct["entitlement"].(map[string]any); ok {
		if subPlan, ok := entitlement["subscription_plan"].(string); ok && subPlan != "" {
			return subPlan
		}
	}
	return ""
}

// IsUsableChatGPTAccountCandidate rejects nil, deactivated, and known expired
// account entries. Missing or malformed entitlement expiry is treated as
// usable, while a valid timestamp must be strictly later than now.
func IsUsableChatGPTAccountCandidate(acct map[string]any, now time.Time) bool {
	if acct == nil || HasChatGPTAccountDeactivatedMarker(acct) {
		return false
	}
	if provider, ok := acct["account"].(map[string]any); ok && HasChatGPTAccountDeactivatedMarker(provider) {
		return false
	}

	expiresAt := ExtractEntitlementExpiresAt(acct)
	if expiresAt == "" {
		return true
	}
	expiry, err := time.Parse(time.RFC3339, expiresAt)
	// Unparseable expiry provides no reliable expiration evidence, so lookup
	// retains the account and lets metadata consumers validate the stored timestamp separately.
	if err != nil {
		return true
	}
	return expiry.After(now)
}

// HasChatGPTAccountDeactivatedMarker recognizes supported true flags, nonblank
// deactivation timestamps, or normalized inactive status names. Other shapes
// and values return false, so candidate selection only excludes evidence this
// parser recognizes.
func HasChatGPTAccountDeactivatedMarker(obj map[string]any) bool {
	for _, key := range []string{"deactivated", "is_deactivated", "disabled", "is_disabled"} {
		if value, ok := obj[key].(bool); ok && value {
			return true
		}
	}
	for _, key := range []string{"deactivated_at", "disabled_at", "deleted_at"} {
		if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	for _, key := range []string{"status", "state"} {
		value, _ := obj[key].(string)
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "deactivated", "disabled", "deleted", "inactive", "suspended":
			return true
		}
	}
	return false
}

// ExtractEntitlementExpiresAt returns entitlement.expires_at when its value is
// a string. Missing object or field yields empty text; callers needing a
// timestamp validate and normalize the supplied value themselves.
func ExtractEntitlementExpiresAt(acct map[string]any) string {
	entitlement, ok := acct["entitlement"].(map[string]any)
	if !ok {
		return ""
	}
	ea, _ := entitlement["expires_at"].(string)
	return ea
}

// Truncate bounds diagnostic text by bytes and appends the omitted-byte count.
// Callers provide nonnegative n; untruncated values retain their original
// string, and truncation may split a multibyte character.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Bound diagnostic bytes while retaining the omitted byte count.
	return s[:n] + fmt.Sprintf("...(%d more)", len(s)-n)
}
