package qoder

import (
	"maps"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// models.go resolves site-specific model aliases, eligibility, thinking
// controls, and context capabilities.
// Model aliases and controls depend on the credential site, with shared route
// keys sometimes representing different public models. Catalog-derived tables
// keep route identity, thinking support, and input limits together, and callers
// obtain copied alias maps for independent enumeration.
//
// Eligibility distinguishes a known alias or route belonging to another site
// from an unregistered raw key. The former is rejected while the latter remains
// available for administrator-configured upstream use. Thinking and context
// lookup normalize spelling within a recognized site, with unknown controls
// unsupported and unresolved context using the execution input default. Local
// authentication conversion separately selects the token used for manual session
// import.

// Thinking capability values select supported switch or effort mappings;
// FallbackMaxInputTokens supplies the catalog's input limit for unresolved
// routes.
const (
	ThinkingUnsupported ThinkingCapability = iota // no adjustable thinking controls
	ThinkingToggleOnly                            // thinking switch without effort
	ThinkingHighMax                               // high or max effort
	ThinkingLowHighMax                            // low, high, or max effort
	// FallbackMaxInputTokens supplies the execution input limit for unresolved
	// routes. ContextCapabilityForSite returns that limit with runtime selection
	// disabled when no verified site capability applies.
	FallbackMaxInputTokens = modelcatalog.QoderFallbackInputLimit
)

// Global and CN alias, thinking, and context tables are catalog-derived
// snapshots read by request normalization and site eligibility checks.
var (
	globalAliases              = modelcatalog.QoderAliases("global")
	cnAliases                  = modelcatalog.QoderAliases("cn")
	globalThinkingCapabilities = qoderThinkingCapabilities("global")
	cnThinkingCapabilities     = qoderThinkingCapabilities("cn")
	globalContextCapabilities  = qoderContextCapabilities("global")
	cnContextCapabilities      = qoderContextCapabilities("cn")
)

// ThinkingCapability identifies the adjustable controls declared for a Qoder
// route.
type ThinkingCapability uint8

// ContextCapability holds a route's input-token limit and runtime
// context-selection flag.
type ContextCapability struct {
	// MaxInputTokens supplies the token cap written to model_config.
	// RuntimeSelectable controls whether the same cap is also written to parameters
	// and the IDE override during payload preparation.
	MaxInputTokens    int  // tokens
	RuntimeSelectable bool // runtime selection support
}

// AuthInfo represents decrypted local Qoder credentials used by the opt-in
// import path.
type AuthInfo struct {
	UID  string `json:"uid"`  // local user identifier
	Name string `json:"name"` // local display name
	// AccessToken and SecurityOauthToken name alternate saved access-token fields.
	// ToAuthIdentity selects SecurityOauthToken when nonempty and otherwise reads
	// AccessToken, retaining the selected field's spelling.
	AccessToken            string `json:"access_token"`              // legacy access token
	SecurityOauthToken     string `json:"security_oauth_token"`      // COSY access token
	RefreshToken           string `json:"refresh_token"`             // credential refresh token
	ExpireTime             int64  `json:"expire_time"`               // local token expiry
	RefreshTokenExpireTime int64  `json:"refresh_token_expire_time"` // local refresh expiry
	LoginMethod            string `json:"login_method"`              // local login method
	LoginTimestamp         int64  `json:"login_timestamp"`           // local login timestamp
	EncryptUserInfo        string `json:"encrypt_user_info"`         // encrypted metadata text
	Key                    string `json:"key"`                       // local metadata key
	Email                  string `json:"email"`                     // local contact email
	UserType               string `json:"userType"`                  // Qoder account classification
	MachineID              string `json:"_machine_id"`               // local machine identifier
	OrganizationID         string `json:"organization_id"`           // selected organization identifier
	OrganizationName       string `json:"organization_name"`         // selected organization name
}

// NormalizeModelForWhitelist resolves aliases from either site into route keys
// for matching. Empty input stays empty, and unmatched names retain trimmed
// spelling so provider allowlists can include raw upstream routes.
func NormalizeModelForWhitelist(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	if info, ok := LookupQoderModelAlias(trimmed); ok {
		return strings.TrimSpace(info.Key)
	}
	return trimmed
}

// AliasesForSite returns an independently editable copy of a site's public alias
// map. CN selects its table; other values select global, and modifying the
// returned map leaves the package snapshot unchanged.
func AliasesForSite(site Site) map[string]string {
	aliases := globalAliases
	if site == SiteCN {
		aliases = cnAliases
	}
	out := make(map[string]string, len(aliases))
	maps.Copy(out, aliases)
	return out
}

// AliasForSite returns the route mapped to an exact public alias for the site.
// CN selects its table and other site values select global; callers normalize
// spelling when they need case-insensitive matching.
func AliasForSite(site Site, model string) (string, bool) {
	aliases := globalAliases
	if site == SiteCN {
		aliases = cnAliases
	}
	route, ok := aliases[model]
	return route, ok
}

// ThinkingCapabilityForSite returns adjustable thinking controls after
// normalizing the model and resolving its site alias. Empty site means global.
// Unknown sites, unknown routes, and routes lacking adjustable controls return
// ThinkingUnsupported.
func ThinkingCapabilityForSite(site Site, model string) ThinkingCapability {
	var capabilities map[string]ThinkingCapability
	switch site {
	case "", SiteGlobal:
		capabilities = globalThinkingCapabilities
	case SiteCN:
		capabilities = cnThinkingCapabilities
	default:
		return ThinkingUnsupported
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if route, ok := AliasForSite(site, model); ok {
		model = route
	}
	return capabilities[model]
}

// ContextCapabilityForSite returns the route input limit and runtime-selection
// capability. Model spelling is normalized and aliases resolve within the
// selected site; empty site selects global, while unknown sites or routes use
// the execution input default.
func ContextCapabilityForSite(site Site, model string) ContextCapability {
	var capabilities map[string]ContextCapability
	switch site {
	case "", SiteGlobal:
		capabilities = globalContextCapabilities
	case SiteCN:
		capabilities = cnContextCapabilities
	default:
		return ContextCapability{MaxInputTokens: FallbackMaxInputTokens}
	}

	model = strings.ToLower(strings.TrimSpace(model))
	if route, ok := AliasForSite(site, model); ok {
		model = route
	}
	if capability, ok := capabilities[model]; ok {
		return capability
	}
	return ContextCapability{MaxInputTokens: FallbackMaxInputTokens}
}

// ModelCompatibleWithSite reports whether a known public alias or route belongs
// to the site. Model spelling is normalized before lookup. A public alias known
// only at the other site is rejected, while an unregistered raw route remains
// available for configured upstream use.
//
// A raw route recognized in either snapshot needs at least one alias target in
// the selected site. This distinguishes site-incompatible published identity
// from an unknown administrator-supplied route, while CN selects its own table
// and other site values select global.
func ModelCompatibleWithSite(site Site, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if _, ok := AliasForSite(site, model); ok {
		return true
	}
	if isKnownPublicAlias(model) {
		return false
	}
	if !isKnownRouteKey(model) {
		return true
	}
	for _, route := range AliasesForSite(site) {
		if route == model {
			return true
		}
	}
	return false
}

// isKnownPublicAlias reports whether either site snapshot contains the exact
// alias.
func isKnownPublicAlias(model string) bool {
	_, global := globalAliases[model]
	_, cn := cnAliases[model]
	return global || cn
}

// isKnownRouteKey reports whether either site alias map references the route.
func isKnownRouteKey(model string) bool {
	for _, aliases := range []map[string]string{globalAliases, cnAliases} {
		for _, route := range aliases {
			if route == model {
				return true
			}
		}
	}
	return false
}

// DefaultRequestModelIDsForSite returns sorted public aliases from a copied site
// table. CN selects CN aliases, while other site values select global; the
// returned slice can be modified by the caller.
func DefaultRequestModelIDsForSite(site Site) []string {
	ids := []string{}
	for alias := range AliasesForSite(site) {
		ids = append(ids, alias)
	}
	slices.Sort(ids)
	return ids
}

// DefaultRequestModelIDs returns the sorted, deduplicated union of both sites'
// public aliases.
func DefaultRequestModelIDs() []string {
	ids := append(DefaultRequestModelIDsForSite(SiteGlobal), DefaultRequestModelIDsForSite(SiteCN)...)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// ToAuthIdentity converts nonnil local authentication metadata into a session
// identity. SecurityOauthToken takes precedence over AccessToken, absent user
// type selects personal_standard, and the local UID fills both user and account
// IDs.
func (info *AuthInfo) ToAuthIdentity() *AuthIdentity {
	token := info.SecurityOauthToken
	// Older local exports store the access token under AccessToken. Use that field
	// when the COSY token is empty so manual import can construct an identity.
	if token == "" {
		token = info.AccessToken
	}
	userType := info.UserType
	if userType == "" {
		userType = "personal_standard"
	}
	return &AuthIdentity{
		Name:               info.Name,
		AID:                info.UID,
		UID:                info.UID,
		OrganizationID:     info.OrganizationID,
		OrganizationName:   info.OrganizationName,
		UserType:           userType,
		SecurityOauthToken: token,
		RefreshToken:       info.RefreshToken,
	}
}

// qoderThinkingCapabilities builds a route-indexed thinking snapshot from
// catalog aliases and reasoning options.
func qoderThinkingCapabilities(site string) map[string]ThinkingCapability {
	result := map[string]ThinkingCapability{}
	for _, key := range modelcatalog.QoderAliases(site) {
		options := modelcatalog.QoderReasoningOptions(site, key)
		result[key] = thinkingCapabilityFromAttributes(modelcatalog.Attributes{ReasoningOptions: &options})
	}
	return result
}

// thinkingCapabilityFromAttributes derives thinking support from resolved model
// attributes. An effort option selects low/high/max when low is supported and
// high/max otherwise; toggle-only attributes select the switch, and absent
// controls are unsupported.
func thinkingCapabilityFromAttributes(attributes modelcatalog.Attributes) ThinkingCapability {
	if attributes.HasReasoningOption("effort") {
		if supported := attributes.EffortSupport("low"); supported != nil && *supported {
			return ThinkingLowHighMax
		}
		return ThinkingHighMax
	}
	if attributes.HasReasoningOption("toggle") {
		return ThinkingToggleOnly
	}
	return ThinkingUnsupported
}

// qoderContextCapabilities builds route input limits and runtime flags from
// catalog aliases.
func qoderContextCapabilities(site string) map[string]ContextCapability {
	result := map[string]ContextCapability{}
	for _, key := range modelcatalog.QoderAliases(site) {
		limit, selectable := modelcatalog.QoderInputLimit(site, key)
		result[key] = ContextCapability{MaxInputTokens: limit, RuntimeSelectable: selectable}
	}
	return result
}
