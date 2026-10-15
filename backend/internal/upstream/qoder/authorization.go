package qoder

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// authorization.go completes site-specific device authorization and prepares
// credentials with optional metadata warnings.
// Device polling separates an unfinished browser login from a completed token
// exchange. CompleteAuthorization returns a pending result until the access
// token is available, then resolves the identity and machine fields that the
// provider authorization owner will persist.
//
// CN tokens require userinfo and Gateway status completion before inference.
// Global login can retain token-response identity when userinfo is unavailable,
// with auxiliary lookup failures represented by sanitized warnings. Organization
// tags fill missing metadata using the OpenAPI user identifier, which can differ
// from the COSY account identifier returned by CN status. The stored flow keeps
// the creation-time profile, machine, and proxy together across polls.

// OAuthClientPort supplies polling and identity lookups for the authorization
// completion flow.
type OAuthClientPort interface {
	// PollDeviceToken queries the nonce with its PKCE verifier and reports token
	// readiness. A pending login returns ready=false with no error; ready=true
	// supplies a usable token response for identity completion.
	PollDeviceToken(ctx context.Context, nonce, verifier string) (*DeviceTokenResponse, bool, error)
	// GetUserInfo loads OpenAPI user metadata using the supplied access token. The
	// authorization caller decides whether a lookup failure aborts CN completion or
	// becomes a warning for global login.
	GetUserInfo(ctx context.Context, token string) (*UserInfo, error)
	// GetOrganizationTags loads organization metadata for the supplied OpenAPI user
	// identifier. The identifier can differ from a CN COSY account ID, so callers
	// select the lookup identity before invoking this operation.
	GetOrganizationTags(ctx context.Context, token, uid string) (*OrganizationTags, error)
}

// CN20OAuthCompleter turns validated CN OpenAPI tokens into an inference
// identity and expiry.
type CN20OAuthCompleter interface {
	// CompleteQoderCN20Identity completes CN status using the token, user metadata,
	// and stored machine. Callers supply a validated CN token and retain the
	// returned OpenAPI expiry alongside the COSY identity for persistence.
	CompleteQoderCN20Identity(
		ctx context.Context,
		token *DeviceTokenResponse,
		user *UserInfo,
		machine *MachineIdentity,
	) (*AuthIdentity, time.Time, error)
}
type (
	// OAuthClientFactory constructs an authorization client using the stored
	// profile and proxy.
	OAuthClientFactory func(profile Profile, proxyURL string) (OAuthClientPort, error)
	// AuthorizationTokenInfo carries credentials and sanitized warnings returned to
	// the provider authorization owner.
	AuthorizationTokenInfo struct {
		SecurityOauthToken string         `json:"security_oauth_token"`        // inference access token
		RefreshToken       string         `json:"refresh_token,omitempty"`     // credential refresh token
		MachineID          string         `json:"machine_id"`                  // saved machine identifier
		MachineToken       string         `json:"machine_token,omitempty"`     // saved machine token
		MachineType        string         `json:"machine_type,omitempty"`      // saved machine type
		UID                string         `json:"uid,omitempty"`               // COSY user identifier
		AID                string         `json:"aid,omitempty"`               // COSY account identifier
		OrganizationID     string         `json:"organization_id,omitempty"`   // selected organization identifier
		OrganizationName   string         `json:"organization_name,omitempty"` // selected organization name
		Name               string         `json:"name,omitempty"`              // authenticated display name
		UserType           string         `json:"user_type,omitempty"`         // Qoder account classification
		Site               string         `json:"site"`                        // credential site name
		RefreshMode        string         `json:"refresh_mode"`                // credential refresh workflow
		ExpiresAt          string         `json:"expires_at,omitempty"`        // UTC RFC3339 expiry
		Extra              map[string]any `json:"extra,omitempty"`             // sanitized auxiliary warnings
	}
)

// AuthorizationFlow retains the machine, site, and browser proof used throughout
// one device login.
type AuthorizationFlow struct {
	// Nonce, CodeVerifier, and AuthURL retain one browser proof until completion.
	// ProxyURL retains the creation-time transport choice used by the client
	// factory during each poll.
	Nonce, CodeVerifier, AuthURL, ProxyURL string // device polling nonce, private PKCE verifier, browser authorization URL, captured transport proxy
	// Machine is shared across polling and CN completion. The authorization owner
	// retains this pointer for the lifetime of the stored flow.
	Machine *MachineIdentity // flow machine identity
	Site    Site             // credential site name
	Profile Profile          // flow endpoint profile
}

// CompleteAuthorization polls a stored flow and returns credentials or a pending
// marker. The caller supplies a usable flow and factory and owns authorization
// expiry, claiming, and persistence. A false readiness result becomes
// pending=true; exchange errors return pending=false.
//
// CN requires validated tokens, userinfo, and CN20OAuthCompleter status
// completion. Global userinfo failure uses the token-response user ID and
// records a sanitized warning. Organization lookup failures become warnings for
// either site, and CN lookup uses the OpenAPI user ID before falling back to
// COSY identity fields.
func CompleteAuthorization(ctx context.Context, session *AuthorizationFlow, factory OAuthClientFactory) (*AuthorizationTokenInfo, bool, error) {
	client, err := factory(session.Profile, session.ProxyURL)
	if err != nil {
		return nil, false, err
	}

	tokenResp, ready, err := client.PollDeviceToken(ctx, session.Nonce, session.CodeVerifier)
	if err != nil {
		return nil, false, err
	}
	if !ready {
		return nil, true, nil
	}

	accessToken := tokenResp.AccessTokenValue()
	if session.Site == SiteCN {
		expiresAt, validateErr := tokenResp.ValidateQoderCN20(time.Now())
		if validateErr != nil {
			return nil, false, validateErr
		}
		userInfo, userErr := client.GetUserInfo(ctx, accessToken)
		if userErr != nil {
			return nil, false, userErr
		}
		completer, ok := client.(CN20OAuthCompleter)
		if !ok {
			return nil, false, errors.New("qoder CN OAuth client does not support status completion")
		}
		identity, completedExpiry, completeErr := completer.CompleteQoderCN20Identity(ctx, tokenResp, userInfo, session.Machine)
		if completeErr != nil {
			return nil, false, completeErr
		}
		if !completedExpiry.IsZero() {
			expiresAt = completedExpiry
		}
		// CN status can return a different COSY account identifier. Query organization
		// tags with the OpenAPI user identifier so optional metadata belongs to this
		// login.
		organizationLookupUID := FirstNonEmptyQoder(tokenResp.UserID, tokenResp.ID)
		if userInfo != nil {
			organizationLookupUID = FirstNonEmptyQoder(userInfo.UserID, userInfo.ID, organizationLookupUID)
		}
		orgErr := populateQoderOrganizationForUID(ctx, client, accessToken, organizationLookupUID, identity)
		return BuildAuthorizationTokenInfoForSite(identity, session.Machine, SiteCN, RefreshModeQoderCN20, expiresAt, nil, orgErr), false, nil
	}
	userInfo, userErr := client.GetUserInfo(ctx, accessToken)
	// Global login can build identity from the token response when userinfo fails.
	// Carry that user ID forward and report the lookup failure through fixed warnings.
	if userErr != nil {
		userInfo = &UserInfo{ID: tokenResp.UserID}
	}
	identity := BuildIdentityFromDeviceToken(userInfo, tokenResp)
	orgErr := populateQoderOrganization(ctx, client, accessToken, identity)
	expiresAt := tokenResp.ExpiryTime(time.Now())
	return BuildAuthorizationTokenInfoForSite(identity, session.Machine, SiteGlobal, RefreshModeCosy, expiresAt, userErr, orgErr), false, nil
}

// populateQoderOrganization fills missing organization metadata using the
// identity UID or AID. A nil client or identity, an existing organization ID, or
// missing lookup identity leaves the input unchanged; lookup failures return to
// the authorization caller.
func populateQoderOrganization(ctx context.Context, client OAuthClientPort, token string, identity *AuthIdentity) error {
	return populateQoderOrganizationForUID(ctx, client, token, "", identity)
}

// populateQoderOrganizationForUID fills missing organization metadata from
// OpenAPI organization tags. A supplied lookup UID wins over identity UID and
// AID, allowing CN status identity to differ from OpenAPI identity.
//
// Existing organization IDs, nil inputs, and absent lookup IDs skip the request.
// Lookup errors return before mutation; a returned tag record supplies trimmed
// name and ID values together. The caller performs this mutation before building
// the authorization response so its credential fields include the resolved metadata.
func populateQoderOrganizationForUID(ctx context.Context, client OAuthClientPort, token, lookupUID string, identity *AuthIdentity) error {
	if client == nil || identity == nil {
		return nil
	}
	if strings.TrimSpace(identity.OrganizationID) != "" {
		return nil
	}
	uid := strings.TrimSpace(lookupUID)
	if uid == "" {
		uid = strings.TrimSpace(identity.UID)
	}
	if uid == "" {
		uid = strings.TrimSpace(identity.AID)
	}
	if uid == "" {
		return nil
	}
	tags, err := client.GetOrganizationTags(ctx, token, uid)
	if err != nil {
		return err
	}
	if tags == nil {
		return nil
	}
	identity.OrganizationID = strings.TrimSpace(tags.OrganizationID)
	identity.OrganizationName = strings.TrimSpace(tags.OrganizationName)
	return nil
}

// BuildAuthorizationTokenInfoForSite assembles the credential response for the
// provider authorization owner. Nil identity selects personal_standard and nil
// machine creates the site's machine shape. Credential strings are trimmed, a
// nonzero expiry becomes UTC RFC3339 text, and auxiliary errors become fixed
// warning maps rather than exposing raw upstream error text.
func BuildAuthorizationTokenInfoForSite(
	identity *AuthIdentity,
	machine *MachineIdentity,
	site Site,
	refreshMode string,
	expiresAt time.Time,
	userErr error,
	orgErr error,
) *AuthorizationTokenInfo {
	if identity == nil {
		identity = &AuthIdentity{UserType: "personal_standard"}
	}
	if machine == nil {
		machine = NewMachineForSite(site)
	}
	extra := map[string]any{}
	if userErr != nil {
		extra["userinfo_warning"] = sanitizedQoderOAuthWarning("userinfo_unavailable", "Qoder user info could not be loaded")
	}
	if orgErr != nil {
		extra["organization_warning"] = sanitizedQoderOAuthWarning("organization_unavailable", "Qoder organization info could not be loaded")
	}
	if len(extra) == 0 {
		extra = nil
	}
	tokenInfo := &AuthorizationTokenInfo{
		SecurityOauthToken: strings.TrimSpace(identity.SecurityOauthToken),
		RefreshToken:       strings.TrimSpace(identity.RefreshToken),
		MachineID:          strings.TrimSpace(machine.MachineID),
		MachineToken:       strings.TrimSpace(machine.MachineToken),
		MachineType:        strings.TrimSpace(machine.MachineType),
		UID:                strings.TrimSpace(identity.UID),
		AID:                strings.TrimSpace(identity.AID),
		OrganizationID:     strings.TrimSpace(identity.OrganizationID),
		OrganizationName:   strings.TrimSpace(identity.OrganizationName),
		Name:               strings.TrimSpace(identity.Name),
		UserType:           FirstNonEmptyQoder(identity.UserType, "personal_standard"),
		Site:               string(site),
		RefreshMode:        refreshMode,
		Extra:              extra,
	}
	if !expiresAt.IsZero() {
		tokenInfo.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	return tokenInfo
}

// sanitizedQoderOAuthWarning returns the fixed code and message of an
// authorization warning.
func sanitizedQoderOAuthWarning(code, message string) map[string]string {
	return map[string]string{
		"code":    code,
		"message": message,
	}
}

// DefaultOAuthClientFactory obtains the pooled transport for a site's OAuth
// client. The proxy URL is trimmed and the transport uses a twenty-second
// timeout; callers reuse the returned client for flow polling and metadata
// lookup.
func DefaultOAuthClientFactory(profile Profile, proxyURL string) (OAuthClientPort, error) {
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL: strings.TrimSpace(proxyURL),
		Timeout:  20 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return NewOAuthClientForProfile(profile, client), nil
}
