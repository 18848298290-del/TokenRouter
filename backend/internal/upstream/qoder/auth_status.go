package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// AuthStatusPath completes QoderCN20 OpenAPI credentials into a COSY identity.
	AuthStatusPath = "/api/v3/user/status"
	// CosyRefreshTokenPath refreshes manually imported CN COSY tokens.
	CosyRefreshTokenPath = "/api/v3/user/refresh_token"
	// QuotaUsagePath is the quota-query path for both sites.
	QuotaUsagePath = "/api/v2/quota/usage"
)

// AuthStatusAuthInfo contains optional provider details embedded in a Gateway identity request.
type AuthStatusAuthInfo struct {
	UserName       string `json:"userName,omitempty"`
	OrganizationID string `json:"orgId,omitempty"`
}

// AuthStatusParams is the Gateway identity payload. Token fields, NeedRefresh, and AuthInfo
// serialize even when empty.
type AuthStatusParams struct {
	AccessKey          string             `json:"accessKey,omitempty"`
	SecretKey          string             `json:"secretKey,omitempty"`
	SecurityToken      string             `json:"securityToken,omitempty"`
	UserID             string             `json:"userId,omitempty"`
	OrganizationID     string             `json:"orgId,omitempty"`
	Token              string             `json:"token,omitempty"`
	PersonalToken      string             `json:"personalToken"`
	SecurityOauthToken string             `json:"securityOauthToken"`
	RefreshToken       string             `json:"refreshToken"`
	NeedRefresh        bool               `json:"needRefresh"`
	AuthInfo           AuthStatusAuthInfo `json:"authInfo"`
}

// gatewayHTTPPayload is the remoting.HttpPayload envelope for Gateway identity requests.
type gatewayHTTPPayload struct {
	Payload       string `json:"payload"`
	EncodeVersion string `json:"encodeVersion"`
	RequestID     string `json:"requestId,omitempty"`
}

// AuthStatusResult contains identity fields returned by CN Gateway status and refresh.
type AuthStatusResult struct {
	Name                      string         `json:"name"`
	ID                        string         `json:"id"`
	ProviderID                string         `json:"accountId"`
	StaffID                   string         `json:"staffId"`
	Token                     string         `json:"token"`
	Quota                     any            `json:"quota"`
	WhitelistStatus           any            `json:"whitelistStatus"`
	OrganizationID            string         `json:"orgId"`
	OrganizationName          string         `json:"orgName"`
	YxUID                     string         `json:"yxUid"`
	AvatarURL                 string         `json:"avatarUrl"`
	SecurityOauthToken        string         `json:"securityOauthToken"`
	RefreshToken              string         `json:"refreshToken"`
	ExpireTime                FlexibleInt64  `json:"expireTime"`
	IsSubProvider             bool           `json:"isSubAccount"`
	Email                     string         `json:"email"`
	UserType                  string         `json:"userType"`
	IsPrivacyPolicyModifiable bool           `json:"isPrivacyPolicyModifiable"`
	IsQuotaExceeded           bool           `json:"isQuotaExceeded"`
	FeatureSwitches           map[string]any `json:"featureSwitches"`
	TeamSwitches              map[string]any `json:"teamSwitches"`
}

// ExchangeQoderCN20PATContext runs the CN PAT exchange, userinfo, and status sequence.
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

// RefreshQoderCN20SessionContext runs the CN OAuth refresh, userinfo, and status sequence.
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

// CompleteQoderCN20IdentityContext completes a CN OpenAPI token into an inference-ready COSY identity.
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
	// status checks token ownership, so prefer the device token response user_id.
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
	// status uses the pre-login Appcode signature, with no COSY Authorization header.
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
	// Keep the effective OpenAPI tokens; status supplies provider identity fields.
	identity.SecurityOauthToken = provisional.SecurityOauthToken
	identity.RefreshToken = strings.TrimSpace(token.RefreshToken)
	return identity, expiresAt, nil
}

// RefreshCosySessionForProfileContext uses the legacy CN Gateway flow for imported COSY tokens.
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
	if strings.TrimSpace(identity.RefreshToken) == "" {
		identity.RefreshToken = provisional.RefreshToken
	}
	return identity, nil
}

// marshalAuthStatusRequest wraps JSON-encoded parameters as a string in an encodeVersion=1 envelope.
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
		// A successful QoderCN20 status response can omit the tokens already supplied in the request.
		SecurityOauthToken: firstNonEmpty(status.SecurityOauthToken, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.SecurityOauthToken })),
		RefreshToken:       firstNonEmpty(status.RefreshToken, fallbackIdentityValue(fallback, func(v *AuthIdentity) string { return v.RefreshToken })),
	}
	if identity.SecurityOauthToken == "" {
		return nil, fmt.Errorf("qoder: auth status response missing securityOauthToken")
	}
	return identity, nil
}

func fallbackIdentityValue(identity *AuthIdentity, getter func(*AuthIdentity) string) string {
	if identity == nil {
		return ""
	}
	return getter(identity)
}

func userIDFromInfo(user *UserInfo) string {
	if user == nil {
		return ""
	}
	return firstNonEmpty(user.UserID, user.ID)
}

func userNameFromInfo(user *UserInfo) string {
	if user == nil {
		return ""
	}
	return firstNonEmpty(user.Name, user.UserName)
}
