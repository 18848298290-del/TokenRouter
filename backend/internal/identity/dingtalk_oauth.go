package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type DingTalkOAuthOptions struct {
	Enabled             bool   `mapstructure:"enabled"`
	ClientID            string `mapstructure:"client_id"`
	ClientSecret        string `mapstructure:"client_secret"`
	AuthorizeURL        string `mapstructure:"authorize_url"`
	TokenURL            string `mapstructure:"token_url"`
	UserInfoURL         string `mapstructure:"userinfo_url"`
	Scopes              string `mapstructure:"scopes"`
	RedirectURL         string `mapstructure:"redirect_url"`
	FrontendRedirectURL string `mapstructure:"frontend_redirect_url"`

	// Application kind and login mode.
	DingTalkAppKind string `mapstructure:"dingtalk_app_kind"` // Enabled configurations require internal_app.
	AppType         string `mapstructure:"app_type"`          // "public" (default) | "internal"

	// Organization restriction: none or internal_only.
	CorpRestrictionPolicy   string `mapstructure:"corp_restriction_policy"`
	InternalCorpID          string `mapstructure:"internal_corp_id"`
	BypassRegistration      bool   `mapstructure:"bypass_registration"`
	SyncCorpEmail           bool   `mapstructure:"sync_corp_email"`
	SyncDisplayName         bool   `mapstructure:"sync_display_name"`
	SyncDept                bool   `mapstructure:"sync_dept"`
	SyncCorpEmailAttrKey    string `mapstructure:"sync_corp_email_attr_key"`
	SyncDisplayNameAttrKey  string `mapstructure:"sync_display_name_attr_key"`
	SyncDeptAttrKey         string `mapstructure:"sync_dept_attr_key"`
	SyncCorpEmailAttrName   string `mapstructure:"sync_corp_email_attr_name"`
	SyncDisplayNameAttrName string `mapstructure:"sync_display_name_attr_name"`
	SyncDeptAttrName        string `mapstructure:"sync_dept_attr_name"`

	// Email and username policy.
	RequireEmail            bool   `mapstructure:"require_email"`
	UsernameOverwritePolicy string `mapstructure:"username_overwrite_policy"`

	// Attribute matching and synchronization extension settings.
	UsernameAttributeKey         string   `mapstructure:"username_attribute_key"`
	EnableAttributeMatching      bool     `mapstructure:"enable_attribute_matching"`
	EnableAttributeSync          bool     `mapstructure:"enable_attribute_sync"`
	AttributeSyncFields          []string `mapstructure:"attribute_sync_fields"`
	AttributeSyncOverwritePolicy string   `mapstructure:"attribute_sync_overwrite_policy"`
}
type DingTalkUserTokenResp struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpireIn     int64  `json:"expireIn"`
	CorpID       string `json:"corpId"`
}
type DingTalkAPIError struct {
	Code    string
	Message string
	HTTP    int
}

// DingTalkOAuthClient exchanges authorization codes and reads user and department data.
// The provider implementation owns the token cache.
type DingTalkOAuthClient interface {
	ExchangeCodeForUserToken(context.Context, string) (*DingTalkUserTokenResp, error)
	GetUnionIdByUserToken(context.Context, string) (string, string, error)
	GetUserIdByUnionId(context.Context, string) (string, error)
	GetStaffInfoByUserId(context.Context, string) (*DingTalkProfileSnapshot, error)
	DingTalkDepartmentReader
}

func (e *DingTalkAPIError) Error() string {
	return fmt.Sprintf("dingtalk api error code=%s msg=%s http=%d", e.Code, e.Message, e.HTTP)
}

func DingTalkSyntheticEmail(userID string) string {
	return "dingtalk-" + strings.ToLower(strings.TrimSpace(userID)) + DingTalkConnectSyntheticEmailDomain
}

func DingTalkBindLoginCompletionResponse(redirectTo string) map[string]any {
	return map[string]any{
		"step":                      "bind_login_required",
		"existing_account_bindable": true,
		"create_account_allowed":    false,
		"redirect":                  redirectTo,
	}
}

func DingTalkUpstreamClaims(staff *DingTalkProfileSnapshot, unionID, corpID string) map[string]any {
	primaryDeptID := int64(0)
	if len(staff.DeptIDs) > 0 {
		primaryDeptID = staff.DeptIDs[0]
	}
	return map[string]any{
		"email":           staff.Email,
		"username":        staff.Name,
		"nickname":        staff.Nickname,
		"subject":         unionID,      // Global unionID, matching identityKey.ProviderSubject.
		"corp_user_id":    staff.UserID, // Organization-local user ID for auditing; empty for cross-organization users.
		"union_id":        unionID,
		"corp_id":         corpID,
		"primary_dept_id": primaryDeptID, // First department ID used by internal_only profile synchronization.
	}
}

func DingTalkCorpAllowed(cfg DingTalkOAuthOptions, corpID string) bool {
	switch cfg.CorpRestrictionPolicy {
	case "internal_only":
		// Organization membership is checked by GetUserIdByUnionId with the app token.
		// Some authorization responses omit corpId, so this check accepts missing values.
		// The provider maps membership lookup errors 60011/60121 to corp_rejected.
		// ValidateDingTalkConfig requires AppType=internal for this policy.
		return true
	case "none", "":
		return true
	default:
		return false
	}
}

// DingTalkErrorCode maps provider errors to OAuth redirect error codes.
func DingTalkErrorCode(err error) string {
	var apiErr *DingTalkAPIError
	if !errors.As(err, &apiErr) {
		return "upstream_error"
	}
	switch apiErr.Code {
	case "60011", "60121":
		return "corp_rejected"
	case "40014", "50015", "88":
		return "upstream_error"
	default:
		return "upstream_error"
	}
}

func PrepareDingTalkChoice(
	identity PendingAuthIdentityKey,
	suggestedEmail string,
	resolvedEmail string,
	redirectTo string,
	browserSessionKey string,
	upstreamClaims map[string]any,
	compatEmail string,
	compatEmailUser *User,
	forceEmailOnSignup bool,
	signupBlocked bool,
) OAuthPendingDraft {
	suggestionEmail := strings.TrimSpace(suggestedEmail)
	canonicalEmail := strings.TrimSpace(resolvedEmail)
	if suggestionEmail == "" {
		suggestionEmail = canonicalEmail
	}

	completionResponse := map[string]any{
		"step":                      OAuthPendingChoiceStep,
		"adoption_required":         true,
		"redirect":                  strings.TrimSpace(redirectTo),
		"email":                     suggestionEmail,
		"resolved_email":            canonicalEmail,
		"existing_account_email":    "",
		"existing_account_bindable": false,
		"create_account_allowed":    !signupBlocked,
		"force_email_on_signup":     forceEmailOnSignup,
		"choice_reason":             "third_party_signup",
	}
	if strings.TrimSpace(compatEmail) != "" {
		completionResponse["compat_email"] = strings.TrimSpace(compatEmail)
	}
	resolvedChoiceEmail := suggestionEmail
	if compatEmailUser != nil {
		completionResponse["email"] = strings.TrimSpace(compatEmailUser.Email)
		completionResponse["existing_account_email"] = strings.TrimSpace(compatEmailUser.Email)
		completionResponse["existing_account_bindable"] = true
		completionResponse["choice_reason"] = "compat_email_match"
		resolvedChoiceEmail = strings.TrimSpace(compatEmailUser.Email)
	}
	if forceEmailOnSignup && compatEmailUser == nil {
		completionResponse["choice_reason"] = "force_email_on_signup"
	}
	// When signup is blocked, send the user directly to existing-account binding.
	// A compatible email match can prefill that form, but cannot enable account creation.
	if signupBlocked {
		completionResponse["step"] = "bind_login_required"
		completionResponse["existing_account_bindable"] = true
		completionResponse["choice_reason"] = "signup_blocked_redirect_to_bind"
	}

	var targetUserID *int64
	if compatEmailUser != nil && compatEmailUser.ID > 0 {
		targetUserID = &compatEmailUser.ID
	}

	return OAuthPendingDraft{
		Intent:                 OAuthIntentLogin,
		Identity:               identity,
		TargetUserID:           targetUserID,
		ResolvedEmail:          resolvedChoiceEmail,
		RedirectTo:             redirectTo,
		BrowserSessionKey:      browserSessionKey,
		UpstreamIdentityClaims: upstreamClaims,
		CompletionResponse:     completionResponse,
	}
}

// DingTalkSignupBlocked evaluates the supplied registration setting; HTTP callers control when it is read.
func DingTalkSignupBlocked(cfg DingTalkOAuthOptions, enabled bool) bool {
	return !enabled && (!cfg.BypassRegistration || cfg.CorpRestrictionPolicy != "internal_only")
}
