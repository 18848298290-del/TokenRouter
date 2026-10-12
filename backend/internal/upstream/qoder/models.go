package qoder

import (
	"maps"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

const (
	ThinkingUnsupported ThinkingCapability = iota
	ThinkingToggleOnly
	ThinkingHighMax
	ThinkingLowHighMax

	// FallbackMaxInputTokens is the input limit for routes absent from the capability snapshot.
	FallbackMaxInputTokens = modelcatalog.QoderFallbackInputLimit
)

var (
	globalAliases              = modelcatalog.QoderAliases("global")
	cnAliases                  = modelcatalog.QoderAliases("cn")
	globalThinkingCapabilities = qoderThinkingCapabilities("global")
	cnThinkingCapabilities     = qoderThinkingCapabilities("cn")
	globalContextCapabilities  = qoderContextCapabilities("global")
	cnContextCapabilities      = qoderContextCapabilities("cn")
)

// ThinkingCapability describes the thinking controls available to clients.
// The snapshot follows Qoder Global and CN 1.24.2; model updates require checking both site tables.
type ThinkingCapability uint8

// ContextCapability holds a route input limit and whether runtime context selection is supported.
type ContextCapability struct {
	MaxInputTokens    int
	RuntimeSelectable bool
}

// AuthInfo holds user data decrypted from local Qoder authentication storage.
type AuthInfo struct {
	UID                    string `json:"uid"`
	Name                   string `json:"name"`
	AccessToken            string `json:"access_token"`
	SecurityOauthToken     string `json:"security_oauth_token"`
	RefreshToken           string `json:"refresh_token"`
	ExpireTime             int64  `json:"expire_time"`
	RefreshTokenExpireTime int64  `json:"refresh_token_expire_time"`
	LoginMethod            string `json:"login_method"`
	LoginTimestamp         int64  `json:"login_timestamp"`
	EncryptUserInfo        string `json:"encrypt_user_info"`
	Key                    string `json:"key"`
	Email                  string `json:"email"`
	UserType               string `json:"userType"`
	MachineID              string `json:"_machine_id"`
	OrganizationID         string `json:"organization_id"`
	OrganizationName       string `json:"organization_name"`
}

// NormalizeModelForWhitelist resolves known aliases to route keys for whitelist checks.
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

// AliasesForSite returns a copy of the site public-alias-to-route map.
func AliasesForSite(site Site) map[string]string {
	aliases := globalAliases
	if site == SiteCN {
		aliases = cnAliases
	}
	out := make(map[string]string, len(aliases))
	maps.Copy(out, aliases)
	return out
}

// AliasForSite resolves a public alias for the site.
func AliasForSite(site Site, model string) (string, bool) {
	aliases := globalAliases
	if site == SiteCN {
		aliases = cnAliases
	}
	route, ok := aliases[model]
	return route, ok
}

// ThinkingCapabilityForSite resolves aliases and returns the route thinking controls.
// Unknown routes and models without adjustable thinking return ThinkingUnsupported.
// An empty site selects the global table.
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

// ContextCapabilityForSite returns the highest input capability after resolving aliases.
// An empty site means global. Unknown routes use the 200K limit with no runtime selection.
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

// ModelCompatibleWithSite reports whether the site accepts a known alias or route key.
// Unknown raw keys return true for passthrough.
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

func isKnownPublicAlias(model string) bool {
	_, global := globalAliases[model]
	_, cn := cnAliases[model]
	return global || cn
}

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

// DefaultRequestModelIDsForSite returns the sorted public aliases for the site.
func DefaultRequestModelIDsForSite(site Site) []string {
	ids := []string{}
	for alias := range AliasesForSite(site) {
		ids = append(ids, alias)
	}
	slices.Sort(ids)
	return ids
}

// DefaultRequestModelIDs returns the sorted union of both sites public aliases.
func DefaultRequestModelIDs() []string {
	ids := append(DefaultRequestModelIDsForSite(SiteGlobal), DefaultRequestModelIDsForSite(SiteCN)...)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// ToAuthIdentity converts local authentication data into a session identity.
func (info *AuthInfo) ToAuthIdentity() *AuthIdentity {
	token := info.SecurityOauthToken
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

func qoderThinkingCapabilities(site string) map[string]ThinkingCapability {
	result := map[string]ThinkingCapability{}
	for _, key := range modelcatalog.QoderAliases(site) {
		options := modelcatalog.QoderReasoningOptions(site, key)
		result[key] = thinkingCapabilityFromAttributes(modelcatalog.Attributes{ReasoningOptions: &options})
	}
	return result
}

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

func qoderContextCapabilities(site string) map[string]ContextCapability {
	result := map[string]ContextCapability{}
	for _, key := range modelcatalog.QoderAliases(site) {
		limit, selectable := modelcatalog.QoderInputLimit(site, key)
		result[key] = ContextCapability{MaxInputTokens: limit, RuntimeSelectable: selectable}
	}
	return result
}
