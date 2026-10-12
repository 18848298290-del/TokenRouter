package google

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// AntigravityTokenResponse is the Google OAuth token response used by Antigravity.
type AntigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// AntigravityUserInfo contains Google user profile fields.
type AntigravityUserInfo struct {
	Email      string `json:"email"`
	Name       string `json:"name,omitempty"`
	GivenName  string `json:"given_name,omitempty"`
	FamilyName string `json:"family_name,omitempty"`
	Picture    string `json:"picture,omitempty"`
}

// AntigravityLoadCodeAssistRequest carries client metadata for loadCodeAssist.
type AntigravityLoadCodeAssistRequest struct {
	Metadata struct {
		IDEType    string `json:"ideType"`
		IDEVersion string `json:"ideVersion"`
		IDEName    string `json:"ideName"`
	} `json:"metadata"`
}

// AntigravityTierInfo describes a subscription tier.
type AntigravityTierInfo struct {
	ID          string `json:"id"`   // free-tier, g1-pro-tier, g1-ultra-tier
	Name        string `json:"name"` // Display name
	Description string `json:"description"`
}

// AntigravityIneligibleTier describes an unavailable tier and the reason for ineligibility.
type AntigravityIneligibleTier struct {
	Tier *AntigravityTierInfo `json:"tier,omitempty"`
	// ReasonCode identifies ineligibility, such as INELIGIBLE_ACCOUNT.
	ReasonCode    string `json:"reasonCode,omitempty"`
	ReasonMessage string `json:"reasonMessage,omitempty"`
}

// AntigravityLoadCodeAssistResponse contains project and subscription tier information.
type AntigravityLoadCodeAssistResponse struct {
	CloudAICompanionProject string                       `json:"cloudaicompanionProject"`
	CurrentTier             *AntigravityTierInfo         `json:"currentTier,omitempty"`
	PaidTier                *AntigravityPaidTierInfo     `json:"paidTier,omitempty"`
	IneligibleTiers         []*AntigravityIneligibleTier `json:"ineligibleTiers,omitempty"`
}

// AntigravityPaidTierInfo describes a paid tier and its AI Credits balances.
type AntigravityPaidTierInfo struct {
	ID               string                       `json:"id"`
	Name             string                       `json:"name"`
	Description      string                       `json:"description"`
	AvailableCredits []AntigravityAvailableCredit `json:"availableCredits,omitempty"`
}

// AntigravityAvailableCredit contains one AI Credits balance and its usage threshold.
type AntigravityAvailableCredit struct {
	CreditType                  string `json:"creditType,omitempty"`
	CreditAmount                string `json:"creditAmount,omitempty"`
	MinimumCreditAmountForUsage string `json:"minimumCreditAmountForUsage,omitempty"`
}

// AntigravityOnboardUserRequest carries the selected tier and client metadata.
type AntigravityOnboardUserRequest struct {
	TierID   string `json:"tierId"`
	Metadata struct {
		IDEType    string `json:"ideType"`
		Platform   string `json:"platform,omitempty"`
		PluginType string `json:"pluginType,omitempty"`
	} `json:"metadata"`
}

// AntigravityOnboardUserResponse reports onboarding operation progress.
type AntigravityOnboardUserResponse struct {
	Name     string         `json:"name,omitempty"`
	Done     bool           `json:"done"`
	Response map[string]any `json:"response,omitempty"`
}

// AntigravityModelQuotaInfo reports the remaining quota fraction and reset time.
type AntigravityModelQuotaInfo struct {
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

// AntigravityModelInfo describes model capabilities and quota.
type AntigravityModelInfo struct {
	QuotaInfo          *AntigravityModelQuotaInfo `json:"quotaInfo,omitempty"`
	DisplayName        string                     `json:"displayName,omitempty"`
	SupportsImages     *bool                      `json:"supportsImages,omitempty"`
	SupportsThinking   *bool                      `json:"supportsThinking,omitempty"`
	ThinkingBudget     *int                       `json:"thinkingBudget,omitempty"`
	Recommended        *bool                      `json:"recommended,omitempty"`
	MaxTokens          *int                       `json:"maxTokens,omitempty"`
	MaxOutputTokens    *int                       `json:"maxOutputTokens,omitempty"`
	SupportedMimeTypes map[string]bool            `json:"supportedMimeTypes,omitempty"`
}

// AntigravityDeprecatedModelInfo identifies the replacement for a deprecated model.
type AntigravityDeprecatedModelInfo struct {
	NewModelID string `json:"newModelId"`
}

// AntigravityFetchAvailableModelsRequest selects the project for a model query.
type AntigravityFetchAvailableModelsRequest struct {
	Project string `json:"project"`
}

// AntigravityFetchAvailableModelsResponse contains available models and replacements.
type AntigravityFetchAvailableModelsResponse struct {
	Models             map[string]AntigravityModelInfo           `json:"models"`
	DeprecatedModelIDs map[string]AntigravityDeprecatedModelInfo `json:"deprecatedModelIds,omitempty"`
}

// AntigravitySetUserSettingsRequest is the setUserSettings request body.
type AntigravitySetUserSettingsRequest struct {
	UserSettings map[string]any `json:"user_settings"`
}

// AntigravityFetchUserInfoRequest selects the project for fetchUserInfo.
type AntigravityFetchUserInfoRequest struct {
	Project string `json:"project"`
}

// AntigravityFetchUserInfoResponse contains user settings and region.
type AntigravityFetchUserInfoResponse struct {
	UserSettings map[string]any `json:"userSettings,omitempty"`
	RegionCode   string         `json:"regionCode,omitempty"`
}

// AntigravitySetUserSettingsResponse contains the settings returned after an update.
type AntigravitySetUserSettingsResponse struct {
	UserSettings map[string]any `json:"userSettings,omitempty"`
}

// UnmarshalJSON supports both legacy string tiers and object tiers.
func (t *AntigravityTierInfo) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		var id string
		if err := json.Unmarshal(data, &id); err != nil {
			return err
		}
		t.ID = id
		return nil
	}
	type alias AntigravityTierInfo
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = AntigravityTierInfo(decoded)
	return nil
}

// UnmarshalJSON accepts paidTier as either a tier ID string or an object.
func (p *AntigravityPaidTierInfo) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		var id string
		if err := json.Unmarshal(data, &id); err != nil {
			return err
		}
		p.ID = id
		return nil
	}
	type alias AntigravityPaidTierInfo
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = AntigravityPaidTierInfo(raw)
	return nil
}

// GetAmount parses the credit amount as a float, returning zero when no number can be read.
func (c *AntigravityAvailableCredit) GetAmount() float64 {
	if c.CreditAmount == "" {
		return 0
	}
	var value float64
	_, _ = fmt.Sscanf(c.CreditAmount, "%f", &value)
	return value
}

// GetMinimumAmount parses the usage threshold as a float, returning zero when no number can be read.
func (c *AntigravityAvailableCredit) GetMinimumAmount() float64 {
	if c.MinimumCreditAmountForUsage == "" {
		return 0
	}
	var value float64
	_, _ = fmt.Sscanf(c.MinimumCreditAmountForUsage, "%f", &value)
	return value
}

// GetTier returns the paid tier ID when present, otherwise the current tier ID.
func (r *AntigravityLoadCodeAssistResponse) GetTier() string {
	if r.PaidTier != nil && r.PaidTier.ID != "" {
		return r.PaidTier.ID
	}
	if r.CurrentTier != nil {
		return r.CurrentTier.ID
	}
	return ""
}

// GetAvailableCredits returns the paid tier balances, or nil when no paid tier exists.
func (r *AntigravityLoadCodeAssistResponse) GetAvailableCredits() []AntigravityAvailableCredit {
	if r.PaidTier == nil {
		return nil
	}
	return r.PaidTier.AvailableCredits
}

// AntigravityTierIDToPlanType maps tier IDs to display names and treats an empty ID as Free.
func AntigravityTierIDToPlanType(tierID string) string {
	switch strings.ToLower(strings.TrimSpace(tierID)) {
	case "free-tier":
		return "Free"
	case "g1-pro-tier":
		return "Pro"
	case "g1-ultra-tier":
		return "Ultra"
	default:
		if tierID == "" {
			return "Free"
		}
		return tierID
	}
}

// IsPrivate reports whether telemetryEnabled is absent; a nil response also returns true.
func (r *AntigravityFetchUserInfoResponse) IsPrivate() bool {
	if r == nil || r.UserSettings == nil {
		return true
	}
	_, hasTelemetry := r.UserSettings["telemetryEnabled"]
	return !hasTelemetry
}

// IsSuccess reports whether a non-nil response has no telemetryEnabled key.
func (r *AntigravitySetUserSettingsResponse) IsSuccess() bool {
	if r == nil {
		return false
	}
	// Absent and empty userSettings both indicate success.
	if len(r.UserSettings) == 0 {
		return true
	}
	// A present telemetryEnabled key means it was not removed.
	_, hasTelemetry := r.UserSettings["telemetryEnabled"]
	return !hasTelemetry
}
