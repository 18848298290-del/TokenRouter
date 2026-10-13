package identity

import (
	"context"
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// WeChat browser login can use open-platform or official-account
// configuration under one canonical provider key. Compatibility lookups
// retain the older key, while account-choice payloads keep provider subjects
// and selected email hints separate from an unresolved local account target.
//
// The draft builders retain caller-owned claim maps and return new completion
// maps. PrepareWeChatPending can carry a supplied token pair into stored local
// flow state. Ordinary PendingStore consumption sanitizes those fields, while
// transactional binding consumption retains that stored state; response
// normalization independently strips credentials from browser payloads.
// PrepareWeChatChoice leaves TargetUserID unset, so its email hint cannot
// authorize token issuance. Local binding, registration and frontend progression
// remain in the surrounding authentication flow.

// These keys distinguish the current shared WeChat identity namespace from
// the legacy namespace retained by compatibility lookups and administrator binding.
const (
	WeChatOAuthProviderKey       = "wechat-main" // canonical shared provider namespace
	WeChatOAuthLegacyProviderKey = "wechat"      // legacy identity provider namespace
)

// WeChatOAuthOptions carries channel settings resolved for one browser
// authorization ceremony.
type WeChatOAuthOptions struct {
	// Mode, AppID, AppSecret, AuthorizeURL, Scope, RedirectURI, FrontendCallback
	// and APIBaseURL describe the same selected ceremony. HTTP handlers resolve
	// these values before the client uses its application credentials and
	// endpoint addresses.
	// Mode: selected authorization channel mode. AppID: selected channel
	// application identifier. AppSecret: selected channel application
	// credential. AuthorizeURL: selected provider authorization endpoint. Scope:
	// requested authorization scope value. RedirectURI: backend authorization
	// callback address. FrontendCallback: frontend authorization return address.
	// APIBaseURL: provider identity API base.
	Mode, AppID, AppSecret, AuthorizeURL, Scope, RedirectURI, FrontendCallback, APIBaseURL string
	// OpenEnabled and MPEnabled record the two browser capabilities
	// independently. RequiresUnionID reports true only when both are true, so
	// the callback can require its shared cross-channel subject.
	// OpenEnabled: open-platform browser capability switch. MPEnabled:
	// official-account browser capability switch.
	OpenEnabled, MPEnabled bool
}

// WeChatOAuthTokenResponse represents upstream token, subject and
// provider-error response fields.
type WeChatOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`  // upstream bearer access credential
	ExpiresIn    int64  `json:"expires_in"`    // seconds
	RefreshToken string `json:"refresh_token"` // upstream renewal token credential
	OpenID       string `json:"openid"`        // application-specific provider subject identifier
	Scope        string `json:"scope"`         // granted upstream authorization scopes
	UnionID      string `json:"unionid"`       // cross-channel provider subject identifier
	ErrCode      int64  `json:"errcode"`       // upstream API error number
	ErrMsg       string `json:"errmsg"`        // upstream API error message
}

// WeChatOAuthUserInfoResponse carries provider profile hints and their
// upstream error fields.
type WeChatOAuthUserInfoResponse struct {
	OpenID     string `json:"openid"`     // application-specific provider subject identifier
	Nickname   string `json:"nickname"`   // upstream display name hint
	HeadImgURL string `json:"headimgurl"` // upstream profile image URL
	UnionID    string `json:"unionid"`    // cross-channel provider subject identifier
	ErrCode    int64  `json:"errcode"`    // upstream API error number
	ErrMsg     string `json:"errmsg"`     // upstream API error message
}

// WeChatOAuthClient exchanges provider codes for upstream token and identity
// observations.
type WeChatOAuthClient interface {
	// FetchIdentity exchanges an authorization code under resolved WeChat
	// channel settings and returns upstream token/profile observations. The
	// caller owns local binding, token issuance and browser cookies; provider or
	// transport failures remain errors.
	FetchIdentity(context.Context, WeChatOAuthOptions, string) (*WeChatOAuthTokenResponse, *WeChatOAuthUserInfoResponse, error)
}

// RequiresUnionID reports whether both browser-capability switches are
// simultaneously enabled.
func (c WeChatOAuthOptions) RequiresUnionID() bool { return c.OpenEnabled && c.MPEnabled }

// WeChatSyntheticEmail builds the reserved compatibility address for a
// trimmed subject. Blank subjects return empty text, while nonblank subject
// case is retained; this formatting helper performs no provider verification
// or mailbox-syntax validation.
func WeChatSyntheticEmail(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	return "wechat-" + subject + WeChatConnectSyntheticEmailDomain
}

// WeChatFallbackUsername builds a display fallback from a trimmed subject.
// Blank input uses wechat_user; nonblank input takes up to 512 subject bytes
// before adding its prefix, with the truncation helper repairing UTF-8 only
// after an actual cut.
func WeChatFallbackUsername(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "wechat_user"
	}
	// Apply the subject-prefix byte budget before adding the display-name prefix.
	// The delegated helper repairs partial UTF-8 only when truncation occurs.
	return "wechat_" + logredact.TruncateUTF8Value(subject, 512)
}

// PrepareWeChatPending builds a draft from a completed or recoverable
// authorization result. Invitation-required errors become a recoverable
// payload and take precedence over any supplied token pair; other errors
// return before draft construction. Nil error with a pair places its
// credentials in the completion payload.
//
// The draft retains the supplied claim map and target-user pointer, and
// no-error/no-pair input can produce a redirect-only payload. Initial
// pending-session creation retains this completion payload. Response
// normalization strips immediate credentials from its returned browser map.
// Ordinary PendingStore consumption also sanitizes stored local state, while
// PostgreSQL binding/adoption consumption retains it. Callers keep identity,
// target and upstream claims aligned and account for the consumer selected by
// their flow.
func PrepareWeChatPending(
	intent string,
	providerSubject string,
	email string,
	redirectTo string,
	browserSessionKey string,
	upstreamClaims map[string]any,
	tokenPair *TokenPair,
	authErr error,
	targetUserID *int64,
) (OAuthPendingDraft, error) {
	completionResponse := map[string]any{
		"redirect": redirectTo,
	}
	// An authorization error outranks a supplied token pair: preserve only the
	// recoverable invitation step, or return the other failure before publishing
	// any draft.
	if authErr != nil {
		if errors.Is(authErr, ErrOAuthInvitationRequired) {
			completionResponse["error"] = "invitation_required"
		} else {
			return OAuthPendingDraft{}, authErr
		}
	} else if tokenPair != nil {
		completionResponse["access_token"] = tokenPair.AccessToken
		completionResponse["refresh_token"] = tokenPair.RefreshToken
		completionResponse["expires_in"] = tokenPair.ExpiresIn
		completionResponse["token_type"] = "Bearer"
	}

	return OAuthPendingDraft{
		Intent: intent,
		Identity: PendingAuthIdentityKey{
			ProviderType:    "wechat",
			ProviderKey:     WeChatOAuthProviderKey,
			ProviderSubject: providerSubject,
		},
		TargetUserID:           targetUserID,
		ResolvedEmail:          email,
		RedirectTo:             redirectTo,
		BrowserSessionKey:      browserSessionKey,
		UpstreamIdentityClaims: upstreamClaims,
		CompletionResponse:     completionResponse,
	}, nil
}

// PrepareWeChatChoice prepares the frontend account-choice payload and
// selected email hint. A supplied compatibility user prefills
// existing-account controls, while forceEmailOnSignup replaces the choice
// reason even for that match; the helper does not perform account lookup or
// registration.
//
// The draft leaves TargetUserID unset even when a compatibility user is supplied.
// PendingOAuthCompletionCanIssueTokenPair therefore cannot treat its email
// hint as a resolved account target. The returned claim map remains shared,
// while the completion map is new, so callers retain stable upstream data
// through later pending publication.
func PrepareWeChatChoice(
	identity PendingAuthIdentityKey,
	suggestedEmail string,
	resolvedEmail string,
	redirectTo string,
	browserSessionKey string,
	upstreamClaims map[string]any,
	compatEmail string,
	compatEmailUser *User,
	forceEmailOnSignup bool,
) OAuthPendingDraft {
	suggestionEmail := strings.TrimSpace(suggestedEmail)
	canonicalEmail := strings.TrimSpace(resolvedEmail)
	// Use the resolved hint when the suggestion is blank; later compatibility
	// prefill can still choose an account email without resolving the draft's
	// target ID.
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
		"create_account_allowed":    true,
		"force_email_on_signup":     forceEmailOnSignup,
		"choice_reason":             "third_party_signup",
	}
	if strings.TrimSpace(compatEmail) != "" {
		completionResponse["compat_email"] = strings.TrimSpace(compatEmail)
	}
	if compatEmailUser != nil {
		completionResponse["email"] = strings.TrimSpace(compatEmailUser.Email)
		completionResponse["existing_account_email"] = strings.TrimSpace(compatEmailUser.Email)
		completionResponse["existing_account_bindable"] = true
		completionResponse["choice_reason"] = "compat_email_match"
	}
	// The forced-email reason supersedes compatibility-match wording while
	// retaining that match's prefilled controls; later pending actions still
	// resolve their account target.
	if forceEmailOnSignup {
		completionResponse["choice_reason"] = "force_email_on_signup"
	}

	resolvedChoiceEmail := suggestionEmail
	if compatEmailUser != nil {
		resolvedChoiceEmail = strings.TrimSpace(compatEmailUser.Email)
	}

	return OAuthPendingDraft{
		Intent:                 OAuthIntentLogin,
		Identity:               identity,
		ResolvedEmail:          resolvedChoiceEmail,
		RedirectTo:             redirectTo,
		BrowserSessionKey:      browserSessionKey,
		UpstreamIdentityClaims: upstreamClaims,
		CompletionResponse:     completionResponse,
	}
}

// WeChatCompatibleProviderKeys returns the requested trimmed key followed by
// the legacy key when distinct under case-insensitive comparison. Blank input
// selects wechat-main; a nonblank key retains its case, and the result does
// not add every known alias.
func WeChatCompatibleProviderKeys(providerKey string) []string {
	preferred := strings.TrimSpace(providerKey)
	// An omitted key starts lookup with the current canonical namespace; the
	// following legacy candidate keeps older stored WeChat relationships
	// discoverable for this caller.
	if preferred == "" {
		preferred = WeChatOAuthProviderKey
	}
	keys := []string{preferred}
	// Append the legacy namespace after a distinct preferred key so callers can
	// inspect older relationships while avoiding a case-insensitive duplicate
	// candidate.
	if !strings.EqualFold(preferred, WeChatOAuthLegacyProviderKey) {
		keys = append(keys, WeChatOAuthLegacyProviderKey)
	}
	return keys
}
