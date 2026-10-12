package google

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Antigravity discovery combines project identity, registered tiers, credit
// balances, and model quota observations. Tier responses accept both ID strings
// and objects; string decoding updates the identifier alone, while object
// decoding replaces the complete tier value after successful parsing.
//
// Paid-tier selection, credit access, and privacy checks expose different
// caller-visible rules. Returned credit slices remain shared with the paid
// tier; numeric text is scanned without validating the full field; and privacy
// means telemetryEnabled is absent, even when a present key contains false.
// These values retain supplier metadata for provider-side interpretation rather
// than establishing entitlement, model execution authority, or a verified
// telemetry state independently of the owning authorization/settings flow.

// AntigravityTokenResponse holds Google OAuth credentials returned for Antigravity authorization.
type AntigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`            // upstream access credential
	ExpiresIn    int64  `json:"expires_in"`              // seconds
	TokenType    string `json:"token_type"`              // returned authorization scheme
	Scope        string `json:"scope,omitempty"`         // granted OAuth scopes
	RefreshToken string `json:"refresh_token,omitempty"` // optional refresh credential
}

// AntigravityUserInfo holds email, display-name, and picture fields from Google profile JSON.
type AntigravityUserInfo struct {
	Email      string `json:"email"`                 // returned account email
	Name       string `json:"name,omitempty"`        // returned display name
	GivenName  string `json:"given_name,omitempty"`  // returned given name
	FamilyName string `json:"family_name,omitempty"` // returned family name
	Picture    string `json:"picture,omitempty"`     // returned picture URL
}

// AntigravityLoadCodeAssistRequest supplies client IDE metadata for project and tier discovery.
type AntigravityLoadCodeAssistRequest struct {
	Metadata struct {
		IDEType    string `json:"ideType"`    // client IDE identifier
		IDEVersion string `json:"ideVersion"` // client IDE version
		IDEName    string `json:"ideName"`    // client IDE name
	} `json:"metadata"` // requesting client metadata
}

// AntigravityTierInfo holds the identifier and display fields of a registered tier.
type AntigravityTierInfo struct {
	ID          string `json:"id"`          // upstream tier identifier
	Name        string `json:"name"`        // returned display name
	Description string `json:"description"` // returned tier description
}

// AntigravityIneligibleTier holds an unavailable tier and its reported ineligibility reason.
type AntigravityIneligibleTier struct {
	Tier *AntigravityTierInfo `json:"tier,omitempty"` // optional unavailable tier
	// ReasonCode can carry a supplier label such as INELIGIBLE_ACCOUNT.
	ReasonCode    string `json:"reasonCode,omitempty"`    // reported ineligibility code
	ReasonMessage string `json:"reasonMessage,omitempty"` // reported ineligibility message
}

// AntigravityLoadCodeAssistResponse holds project identity, registered tiers, and unavailable tier observations.
type AntigravityLoadCodeAssistResponse struct {
	CloudAICompanionProject string `json:"cloudaicompanionProject"` // discovered project identifier
	// CurrentTier and PaidTier are alternative registered observations.
	// GetTier prefers a nonempty paid ID, otherwise the current ID; absent
	// objects leave discovery and subscription interpretation to provider callers.
	CurrentTier     *AntigravityTierInfo         `json:"currentTier,omitempty"`     // current registered tier
	PaidTier        *AntigravityPaidTierInfo     `json:"paidTier,omitempty"`        // paid registered tier
	IneligibleTiers []*AntigravityIneligibleTier `json:"ineligibleTiers,omitempty"` // reported unavailable tiers
}

// AntigravityPaidTierInfo holds a registered paid tier and its credit observations.
type AntigravityPaidTierInfo struct {
	ID               string                       `json:"id"`                         // upstream tier identifier
	Name             string                       `json:"name"`                       // returned display name
	Description      string                       `json:"description"`                // returned tier description
	AvailableCredits []AntigravityAvailableCredit `json:"availableCredits,omitempty"` // reported credit balances
}

// AntigravityAvailableCredit retains one credit category and its numeric text observations.
type AntigravityAvailableCredit struct {
	// CreditAmount and MinimumCreditAmountForUsage retain numeric text from
	// the same CreditType observation. Their getters scan one floating value,
	// returning zero when none is assigned and retaining parsed prefixes.
	CreditType                  string `json:"creditType,omitempty"`                  // reported credit category
	CreditAmount                string `json:"creditAmount,omitempty"`                // reported balance text
	MinimumCreditAmountForUsage string `json:"minimumCreditAmountForUsage,omitempty"` // reported usage-threshold text
}

// AntigravityOnboardUserRequest supplies a selected tier and client metadata for onboarding.
type AntigravityOnboardUserRequest struct {
	TierID   string `json:"tierId"` // selected onboarding tier
	Metadata struct {
		IDEType    string `json:"ideType"`              // client IDE identifier
		Platform   string `json:"platform,omitempty"`   // client platform identifier
		PluginType string `json:"pluginType,omitempty"` // client plugin identifier
	} `json:"metadata"` // requesting client metadata
}

// AntigravityOnboardUserResponse holds operation identity, completion, and optional onboarding result data.
type AntigravityOnboardUserResponse struct {
	Name     string         `json:"name,omitempty"`     // upstream operation name
	Done     bool           `json:"done"`               // operation completion flag
	Response map[string]any `json:"response,omitempty"` // optional operation result
}

// AntigravityModelQuotaInfo holds a remaining quota fraction and its supplied reset text.
type AntigravityModelQuotaInfo struct {
	RemainingFraction float64 `json:"remainingFraction"`   // remaining quota fraction
	ResetTime         string  `json:"resetTime,omitempty"` // supplied reset timestamp
}

// AntigravityModelInfo holds advertised capabilities and optional per-model quota metadata.
type AntigravityModelInfo struct {
	QuotaInfo   *AntigravityModelQuotaInfo `json:"quotaInfo,omitempty"`   // optional model quota
	DisplayName string                     `json:"displayName,omitempty"` // advertised model name
	// SupportsImages, SupportsThinking, ThinkingBudget, Recommended, MaxTokens,
	// and MaxOutputTokens use pointers to distinguish absent metadata from
	// supplied false or zero observations; consumers choose their own defaults.
	SupportsImages     *bool           `json:"supportsImages,omitempty"`     // optional image-capability flag
	SupportsThinking   *bool           `json:"supportsThinking,omitempty"`   // optional reasoning-capability flag
	ThinkingBudget     *int            `json:"thinkingBudget,omitempty"`     // advertised reasoning-token budget
	Recommended        *bool           `json:"recommended,omitempty"`        // optional recommendation flag
	MaxTokens          *int            `json:"maxTokens,omitempty"`          // optional context-token limit
	MaxOutputTokens    *int            `json:"maxOutputTokens,omitempty"`    // optional output-token limit
	SupportedMimeTypes map[string]bool `json:"supportedMimeTypes,omitempty"` // advertised MIME support
}

// AntigravityDeprecatedModelInfo identifies the advertised successor for a deprecated model.
type AntigravityDeprecatedModelInfo struct {
	NewModelID string `json:"newModelId"` // advertised successor model
}

// AntigravityFetchAvailableModelsRequest selects project context for the upstream model-discovery call.
type AntigravityFetchAvailableModelsRequest struct {
	Project string `json:"project"` // selected project identifier
}

// AntigravityFetchAvailableModelsResponse holds advertised models and deprecated-model successor mappings.
type AntigravityFetchAvailableModelsResponse struct {
	Models             map[string]AntigravityModelInfo           `json:"models"`                       // ID-keyed model metadata
	DeprecatedModelIDs map[string]AntigravityDeprecatedModelInfo `json:"deprecatedModelIds,omitempty"` // deprecated-ID successor mappings
}

// AntigravitySetUserSettingsRequest supplies settings fields for the upstream settings update.
type AntigravitySetUserSettingsRequest struct {
	UserSettings map[string]any `json:"user_settings"` // requested settings fields
}

// AntigravityFetchUserInfoRequest selects project context for the upstream user-settings lookup.
type AntigravityFetchUserInfoRequest struct {
	Project string `json:"project"` // selected project identifier
}

// AntigravityFetchUserInfoResponse holds returned user settings and optional region metadata.
type AntigravityFetchUserInfoResponse struct {
	UserSettings map[string]any `json:"userSettings,omitempty"` // returned settings fields
	RegionCode   string         `json:"regionCode,omitempty"`   // returned region code
}

// AntigravitySetUserSettingsResponse holds settings returned after an upstream update operation.
type AntigravitySetUserSettingsResponse struct {
	UserSettings map[string]any `json:"userSettings,omitempty"` // returned settings fields
}

// UnmarshalJSON decodes a tier ID string or a complete tier object into a
// writable receiver. Empty or null input leaves it unchanged, while a string
// updates ID alone and retains existing Name and Description.
//
// Object decoding uses temporary alias state and replaces every tier field
// after success, clearing omitted fields. A decode error publishes no update.
// The alias bypasses this decoder's recursion, and the distinction between
// string and object updates matters when callers reuse a populated tier value.
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
	// alias retains tier fields while bypassing AntigravityTierInfo's custom decoder.
	type alias AntigravityTierInfo
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = AntigravityTierInfo(decoded)
	return nil
}

// UnmarshalJSON decodes a paid tier from an ID string or object into a
// writable receiver. Empty or null input leaves it unchanged; string input
// changes only ID and retains display fields and existing credit data.
//
// Object decoding replaces the complete paid-tier value after a temporary
// decode succeeds, so omitted credits become absent. Decode failure returns
// its error without publishing temporary fields. Callers needing replacement
// of every field choose object input rather than assuming a string ID clears
// the previously observed paid-tier metadata.
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
	// alias retains paid-tier fields while bypassing AntigravityPaidTierInfo's custom decoder.
	type alias AntigravityPaidTierInfo
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = AntigravityPaidTierInfo(raw)
	return nil
}

// GetAmount scans one floating value from a nonnil credit's amount text.
// Empty or unreadable text returns zero, and scan errors/counts are discarded;
// a parsed numeric prefix survives without full-text, sign, or balance validation.
func (c *AntigravityAvailableCredit) GetAmount() float64 {
	if c.CreditAmount == "" {
		return 0
	}
	var value float64
	// A partially readable balance can still assign a float before scanning
	// stops. Preserve that assigned value while leaving whole-field validation
	// to the credit observation's consumer.
	_, _ = fmt.Sscanf(c.CreditAmount, "%f", &value)
	return value
}

// GetMinimumAmount scans one floating value from a nonnil credit's usage
// threshold text. Empty or unreadable text returns zero; scan errors/counts
// are discarded, so the result alone does not establish complete numeric-field validity.
func (c *AntigravityAvailableCredit) GetMinimumAmount() float64 {
	if c.MinimumCreditAmountForUsage == "" {
		return 0
	}
	var value float64
	// Scanning one float leaves following text unchecked. The caller decides
	// whether that numeric observation is sufficient for credit use, since the
	// result cannot establish whole-field validity.
	_, _ = fmt.Sscanf(c.MinimumCreditAmountForUsage, "%f", &value)
	return value
}

// GetTier selects the registered identifier from a nonnil discovery response.
// A nonempty paid ID wins, including whitespace; otherwise a current-tier
// object supplies its ID. Missing tiers return empty without consulting
// ineligible-tier observations or normalizing the selected identifier.
func (r *AntigravityLoadCodeAssistResponse) GetTier() string {
	if r.PaidTier != nil && r.PaidTier.ID != "" {
		return r.PaidTier.ID
	}
	// Missing paid metadata or an empty paid ID supplies no preferred value.
	// Keep the current-tier ID available as a fallback, including its possible empty value.
	if r.CurrentTier != nil {
		return r.CurrentTier.ID
	}
	return ""
}

// GetAvailableCredits returns the paid tier's shared credit slice from a
// nonnil discovery response, or nil when no paid tier exists. Callers copy
// entries before independent mutable use; the getter performs no balance filtering.
func (r *AntigravityLoadCodeAssistResponse) GetAvailableCredits() []AntigravityAvailableCredit {
	if r.PaidTier == nil {
		return nil
	}
	return r.PaidTier.AvailableCredits
}

// AntigravityTierIDToPlanType maps trimmed, case-insensitive known tier IDs
// to display names. Exactly empty original input returns Free; other unknown
// input returns its supplied spelling, including a whitespace-only value whose
// trimmed form matched no known tier.
func AntigravityTierIDToPlanType(tierID string) string {
	switch strings.ToLower(strings.TrimSpace(tierID)) {
	case "free-tier":
		return "Free"
	case "g1-pro-tier":
		return "Pro"
	case "g1-ultra-tier":
		return "Ultra"
	default:
		// Only exactly empty original input receives the Free display default.
		// Other unknown labels retain their supplied spelling so discovery can expose
		// a vendor identifier that this mapper has not recognized.
		if tierID == "" {
			return "Free"
		}
		return tierID
	}
}

// IsPrivate reports whether telemetryEnabled is absent from returned settings.
// A nil response or nil settings returns true. Any present key returns false,
// including false or null values; callers interpret this presence-based
// observation separately from the effectiveness of upstream telemetry behavior.
func (r *AntigravityFetchUserInfoResponse) IsPrivate() bool {
	if r == nil || r.UserSettings == nil {
		return true
	}
	_, hasTelemetry := r.UserSettings["telemetryEnabled"]
	return !hasTelemetry
}

// IsSuccess reports whether a nonnil settings-update response has no
// telemetryEnabled key. Nil responses return false; absent or empty settings
// make a nonnil response pass. A present key returns false regardless of its value.
// The caller validates the exchange and uses this response-shape evidence.
func (r *AntigravitySetUserSettingsResponse) IsSuccess() bool {
	if r == nil {
		return false
	}
	if len(r.UserSettings) == 0 {
		return true
	}
	_, hasTelemetry := r.UserSettings["telemetryEnabled"]
	return !hasTelemetry
}
