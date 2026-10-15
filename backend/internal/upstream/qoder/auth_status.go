package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// auth_status.go completes CN token identities through Gateway status and
// refreshes imported COSY credentials.
// CN OpenAPI tokens require Gateway status completion to obtain COSY user and
// account identity before inference. That request uses pre-login Appcode signing
// with the stored machine, while imported COSY credentials refresh through a
// separate authenticated Gateway endpoint.
//
// Status requests carry string-valued JSON in an encodeVersion=1 envelope,
// including empty token and authInfo fields required by the request shape.
// Status identity fields can differ from OpenAPI user IDs, so completion uses
// the device-token user ID for the status request and then keeps the effective
// OpenAPI access and refresh tokens. Legacy COSY refresh instead accepts token
// replacements from status and retains its old refresh token when omitted.

// Gateway paths separate pre-login identity completion, authenticated legacy
// refresh, and quota lookup so each caller selects its required authentication.
const (
	// AuthStatusPath completes QoderCN20 OpenAPI credentials into a COSY identity.
	AuthStatusPath = "/api/v3/user/status"
	// CosyRefreshTokenPath refreshes manually imported CN COSY tokens.
	CosyRefreshTokenPath = "/api/v3/user/refresh_token"
	// QuotaUsagePath is the quota-query path for both sites.
	QuotaUsagePath = "/api/v2/quota/usage"
)

// AuthStatusAuthInfo carries optional descriptive details inside a Gateway
// identity request.
type AuthStatusAuthInfo struct {
	UserName       string `json:"userName,omitempty"` // optional authenticated name
	OrganizationID string `json:"orgId,omitempty"`    // selected organization identifier
}

// AuthStatusParams represents the identity and token fields serialized for
// Gateway completion.
type AuthStatusParams struct {
	AccessKey      string `json:"accessKey,omitempty"`     // optional access key
	SecretKey      string `json:"secretKey,omitempty"`     // optional secret key
	SecurityToken  string `json:"securityToken,omitempty"` // optional security token
	UserID         string `json:"userId,omitempty"`        // OpenAPI ownership identifier
	OrganizationID string `json:"orgId,omitempty"`         // selected organization identifier
	Token          string `json:"token,omitempty"`         // optional protocol token
	// PersonalToken, SecurityOauthToken, RefreshToken, NeedRefresh, and AuthInfo
	// are serialized even at their zero values. Gateway identity requests retain
	// these keys as part of the request shape.
	PersonalToken      string             `json:"personalToken"`      // personal token text
	SecurityOauthToken string             `json:"securityOauthToken"` // inference access token
	RefreshToken       string             `json:"refreshToken"`       // credential refresh token
	NeedRefresh        bool               `json:"needRefresh"`        // refresh request marker
	AuthInfo           AuthStatusAuthInfo `json:"authInfo"`           // embedded descriptive metadata
}

// gatewayHTTPPayload wraps an inner JSON string for encoded Gateway identity
// requests.
type gatewayHTTPPayload struct {
	Payload       string `json:"payload"`             // inner JSON text
	EncodeVersion string `json:"encodeVersion"`       // body encoding version
	RequestID     string `json:"requestId,omitempty"` // optional request identifier
}

// AuthStatusResult represents wire identity and quota metadata returned by CN
// status.
type AuthStatusResult struct {
	Name                      string         `json:"name"`                      // authenticated display name
	ID                        string         `json:"id"`                        // COSY user identifier
	ProviderID                string         `json:"accountId"`                 // COSY account identifier
	StaffID                   string         `json:"staffId"`                   // status staff identifier
	Token                     string         `json:"token"`                     // optional protocol token
	Quota                     any            `json:"quota"`                     // raw quota metadata
	WhitelistStatus           any            `json:"whitelistStatus"`           // raw whitelist metadata
	OrganizationID            string         `json:"orgId"`                     // selected organization identifier
	OrganizationName          string         `json:"orgName"`                   // selected organization name
	YxUID                     string         `json:"yxUid"`                     // Gateway alternate UID
	AvatarURL                 string         `json:"avatarUrl"`                 // user avatar URL
	SecurityOauthToken        string         `json:"securityOauthToken"`        // inference access token
	RefreshToken              string         `json:"refreshToken"`              // credential refresh token
	ExpireTime                FlexibleInt64  `json:"expireTime"`                // status expiry value
	IsSubProvider             bool           `json:"isSubAccount"`              // subaccount status marker
	Email                     string         `json:"email"`                     // user contact email
	UserType                  string         `json:"userType"`                  // Qoder account classification
	IsPrivacyPolicyModifiable bool           `json:"isPrivacyPolicyModifiable"` // policy edit permission
	IsQuotaExceeded           bool           `json:"isQuotaExceeded"`           // reported quota exhaustion
	FeatureSwitches           map[string]any `json:"featureSwitches"`           // user feature switches
	TeamSwitches              map[string]any `json:"teamSwitches"`              // team feature switches
}

// ExchangeQoderCN20PATContext completes CN PAT exchange, userinfo lookup, and
// Gateway status. The profile must select CN, nil machine creates the CN shape,
// and a supplied doer controls both OpenAPI and Gateway requests. The returned
// expiry comes from the OpenAPI token.
func ExchangeQoderCN20PATContext(
	ctx context.Context,
	pat string,
	machine *MachineIdentity,
	profile Profile,
	doer RequestDoer,
) (*AuthIdentity, time.Time, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return nil, time.Time{}, err
	}
	if normalized.Site != SiteCN {
		return nil, time.Time{}, fmt.Errorf("qoder: QoderCN20 PAT requires cn site")
	}
	if machine == nil {
		machine = NewMachineForSite(normalized.Site)
	}
	openAPI := NewOAuthClientForProfile(normalized, nil)
	openAPI.Doer = doer
	token, err := openAPI.ExchangeQoderCN20PAT(ctx, pat)
	if err != nil {
		return nil, time.Time{}, err
	}
	user, err := openAPI.GetUserInfo(ctx, token.AccessTokenValue())
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("qoder: load PAT userinfo: %w", err)
	}
	return CompleteQoderCN20IdentityContext(ctx, normalized, token, user, machine, doer)
}

// RefreshQoderCN20SessionContext completes CN token refresh, userinfo lookup,
// and Gateway status. The profile must select CN and nil machine creates its
// site shape. Network or validation failures return before an inference identity
// is published.
func RefreshQoderCN20SessionContext(
	ctx context.Context,
	refreshToken string,
	machine *MachineIdentity,
	profile Profile,
	doer RequestDoer,
) (*AuthIdentity, time.Time, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return nil, time.Time{}, err
	}
	if normalized.Site != SiteCN {
		return nil, time.Time{}, fmt.Errorf("qoder: QoderCN20 refresh requires cn site")
	}
	if machine == nil {
		machine = NewMachineForSite(normalized.Site)
	}
	openAPI := NewOAuthClientForProfile(normalized, nil)
	openAPI.Doer = doer
	token, err := openAPI.RefreshQoderCN20Token(ctx, refreshToken)
	if err != nil {
		return nil, time.Time{}, err
	}
	user, err := openAPI.GetUserInfo(ctx, token.AccessTokenValue())
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("qoder: load refreshed userinfo: %w", err)
	}
	return CompleteQoderCN20IdentityContext(ctx, normalized, token, user, machine, doer)
}

// CompleteQoderCN20IdentityContext obtains COSY identity for a validated CN
// OpenAPI token. The CN profile and token expiry are validated before status.
// Device-token user ID takes precedence over userinfo identity because the
// request checks ownership against that token.
//
// Status runs with pre-login Appcode signing and machine metadata. Returned
// user, account, and organization fields normalize into AuthIdentity, while the
// effective OpenAPI access and refresh tokens replace any status token values.
// The returned expiry remains the OpenAPI expiry for provider persistence.
func CompleteQoderCN20IdentityContext(
	ctx context.Context,
	profile Profile,
	token *DeviceTokenResponse,
	user *UserInfo,
	machine *MachineIdentity,
	doer RequestDoer,
) (*AuthIdentity, time.Time, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return nil, time.Time{}, err
	}
	if normalized.Site != SiteCN {
		return nil, time.Time{}, fmt.Errorf("qoder: QoderCN20 completion requires cn site")
	}
	if machine == nil {
		machine = NewMachineForSite(normalized.Site)
	}
	expiresAt, err := token.ValidateQoderCN20(time.Now())
	if err != nil {
		return nil, time.Time{}, err
	}
	// Status verifies the token's owner, whose user_id can differ from userinfo.
	// Prefer the device-token identifier before using userinfo to fill a missing
	// ID.
	userID := firstNonEmpty(token.UserID, userIDFromInfo(user))
	if userID == "" {
		return nil, time.Time{}, fmt.Errorf("qoder: QoderCN20 completion requires user id")
	}
	provisional := &AuthIdentity{
		Name:               firstNonEmpty(userNameFromInfo(user), token.UserName),
		AID:                userID,
		UID:                userID,
		UserType:           "personal_standard",
		SecurityOauthToken: token.AccessTokenValue(),
		RefreshToken:       strings.TrimSpace(token.RefreshToken),
	}
	// COSY identity has not been completed yet. Send status with Appcode signing
	// and machine headers so authentication does not depend on the identity being
	// requested.
	session := &SessionContext{
		Machine:       machine,
		Site:          normalized.Site,
		ClientVersion: normalized.ClientVersion,
	}
	params := AuthStatusParams{
		UserID:             userID,
		SecurityOauthToken: provisional.SecurityOauthToken,
		RefreshToken:       provisional.RefreshToken,
	}
	body, err := marshalAuthStatusRequest(params)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("qoder: encode auth status request: %w", err)
	}
	client := NewClientForProfile(normalized)
	var status AuthStatusResult
	if err := client.SignatureJSONRequestContextWithDoer(ctx, http.MethodPost, session, AuthStatusPath, body, nil, doer, &status); err != nil {
		return nil, time.Time{}, fmt.Errorf("qoder: complete QoderCN20 identity: %w", err)
	}
	identity, err := identityFromAuthStatus(&status, provisional)
	if err != nil {
		return nil, time.Time{}, err
	}
	// Status resolves COSY identity fields, while OpenAPI supplied the effective
	// token pair and expiry. Reinstall that pair so later refresh uses the same
	// credentials.
	identity.SecurityOauthToken = provisional.SecurityOauthToken
	identity.RefreshToken = strings.TrimSpace(token.RefreshToken)
	return identity, expiresAt, nil
}

// RefreshCosySessionForProfileContext refreshes imported CN COSY credentials
// through Gateway. CN site, user ID, security token, and refresh token are
// required; nil machine creates the CN shape. Status can replace identity and
// token fields, while an omitted refresh token retains the request value.
func RefreshCosySessionForProfileContext(
	ctx context.Context,
	profile Profile,
	refreshToken string,
	securityOauthToken string,
	userID string,
	organizationID string,
	machine *MachineIdentity,
	doer RequestDoer,
) (*AuthIdentity, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return nil, err
	}
	if normalized.Site != SiteCN {
		return nil, fmt.Errorf("qoder: Gateway COSY refresh requires cn site")
	}
	if machine == nil {
		machine = NewMachineForSite(normalized.Site)
	}
	provisional := &AuthIdentity{
		AID:                strings.TrimSpace(userID),
		UID:                strings.TrimSpace(userID),
		OrganizationID:     strings.TrimSpace(organizationID),
		UserType:           "personal_standard",
		SecurityOauthToken: strings.TrimSpace(securityOauthToken),
		RefreshToken:       strings.TrimSpace(refreshToken),
	}
	if provisional.UID == "" || provisional.SecurityOauthToken == "" || provisional.RefreshToken == "" {
		return nil, fmt.Errorf("qoder: COSY refresh requires user id, security token and refresh token")
	}
	session, err := NewSessionForProfileWithKey(provisional, machine, normalized, nil)
	if err != nil {
		return nil, err
	}
	body, err := marshalAuthStatusRequest(AuthStatusParams{
		UserID:             provisional.UID,
		OrganizationID:     provisional.OrganizationID,
		SecurityOauthToken: provisional.SecurityOauthToken,
		RefreshToken:       provisional.RefreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("qoder: encode COSY refresh request: %w", err)
	}
	client := NewClientForProfile(normalized)
	var status AuthStatusResult
	if err := client.JSONRequestContextWithDoer(ctx, http.MethodPost, session, CosyRefreshTokenPath, body, nil, doer, &status); err != nil {
		return nil, fmt.Errorf("qoder: refresh COSY identity: %w", err)
	}
	identity, err := identityFromAuthStatus(&status, provisional)
	if err != nil {
		return nil, err
	}
	// Keep the saved token when the assembled refresh value is blank. The provider
	// can then retain its existing refresh credentials when merging this result.
	if strings.TrimSpace(identity.RefreshToken) == "" {
		identity.RefreshToken = provisional.RefreshToken
	}
	return identity, nil
}

// marshalAuthStatusRequest returns the status parameters as text inside the
// Gateway JSON envelope. The envelope fixes encodeVersion to one and omits
// requestId; Client applies COSY byte encoding before sending it.
func marshalAuthStatusRequest(params AuthStatusParams) ([]byte, error) {
	inner, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode inner payload: %w", err)
	}
	body, err := json.Marshal(gatewayHTTPPayload{
		Payload:       string(inner),
		EncodeVersion: "1",
	})
	if err != nil {
		return nil, fmt.Errorf("encode request envelope: %w", err)
	}
	return body, nil
}

// identityFromAuthStatus resolves a status response against the provisional
// request identity. Nil status is an error. Name, organization, and user type
// prefer nonempty status fields, while user and account IDs use their respective
// status ID/accountId order before request identity.
//
// A successful status can omit tokens already sent in the request. Token
// selection therefore falls back to the provisional identity, and the resulting
// security token is required. CN20 completion subsequently reinstalls its
// effective OpenAPI tokens.
func identityFromAuthStatus(status *AuthStatusResult, fallback *AuthIdentity) (*AuthIdentity, error) {
	if status == nil {
		return nil, fmt.Errorf("qoder: auth status response is empty")
	}
	identity := &AuthIdentity{
		Name:             firstNonEmpty(status.Name, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.Name })),
		AID:              firstNonEmpty(status.ProviderID, status.ID, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.AID })),
		UID:              firstNonEmpty(status.ID, status.ProviderID, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.UID })),
		YxUID:            status.YxUID,
		OrganizationID:   firstNonEmpty(status.OrganizationID, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.OrganizationID })),
		OrganizationName: firstNonEmpty(status.OrganizationName, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.OrganizationName })),
		UserType:         firstNonEmpty(status.UserType, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.UserType }), "personal_standard"),
		// Status can omit tokens already sent in the request. Retain the provisional
		// pair so a successful identity response still yields usable inference
		// credentials.
		SecurityOauthToken: firstNonEmpty(status.SecurityOauthToken, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.SecurityOauthToken })),
		RefreshToken:       firstNonEmpty(status.RefreshToken, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.RefreshToken })),
	}
	if identity.SecurityOauthToken == "" {
		return nil, fmt.Errorf("qoder: auth status response missing securityOauthToken")
	}
	return identity, nil
}

// fallbackIdentityValue reads a provisional identity field through getter.
// A nil identity supplies empty text to firstNonEmpty; a nonnil identity requires
// a usable getter and retains that field's spelling.
func fallbackIdentityValue(identity *AuthIdentity, getter func(*AuthIdentity) string) string {
	if identity == nil {
		return ""
	}
	return getter(identity)
}

// userIDFromInfo selects the trimmed OpenAPI ownership identifier from userinfo.
// UserID takes precedence over ID, and absent metadata supplies empty text so
// completion can use the device-token identifier.
func userIDFromInfo(user *UserInfo) string {
	if user == nil {
		return ""
	}
	return firstNonEmpty(user.UserID, user.ID)
}

// userNameFromInfo selects the trimmed display name from OpenAPI userinfo.
// Name takes precedence over UserName, and absent metadata supplies empty text
// so completion can use the token response's name.
func userNameFromInfo(user *UserInfo) string {
	if user == nil {
		return ""
	}
	return firstNonEmpty(user.Name, user.UserName)
}
