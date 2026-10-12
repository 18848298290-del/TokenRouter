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

	// FallbackMaxInputTokens 是未知或尚未纳入能力快照的 Qoder route 使用的保守输入上限。
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

// ThinkingCapability 描述 Qoder 模型可由客户端调整的思考能力。
// 能力快照来自 Qoder 国际版和国内版 1.24.2；更新模型列表时必须同步核对对应站点能力表。
type ThinkingCapability uint8

// ContextCapability 描述 Qoder route 的最高输入上限以及是否支持运行时上下文档位。
type ContextCapability struct {
	MaxInputTokens    int
	RuntimeSelectable bool
}

// AuthInfo 保存从本地 Qoder 认证存储解密出的用户信息。
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

// NormalizeModelForWhitelist 将已知别名转换成模型白名单使用的路由键。
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

// AliasesForSite 返回指定站点公开 alias 到内部 route key 的副本。
func AliasesForSite(site Site) map[string]string {
	aliases := globalAliases
	if site == SiteCN {
		aliases = cnAliases
	}
	out := make(map[string]string, len(aliases))
	maps.Copy(out, aliases)
	return out
}

// AliasForSite 解析指定站点的公开 alias。
func AliasForSite(site Site, model string) (string, bool) {
	aliases := globalAliases
	if site == SiteCN {
		aliases = cnAliases
	}
	route, ok := aliases[model]
	return route, ok
}

// ThinkingCapabilityForSite 按站点和最终路由查询可调思考能力。
// 公开 alias 会先解析为 route key；未知路由以及不可调模型均不主动透传。
// 空站点按国际站能力查询。
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

// ContextCapabilityForSite 按站点和最终路由返回最高上下文能力。
// 公开 alias 会先解析为 route key；空站点按国际站处理，未知 route 回退 200K 且不选择运行时档位。
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

// ModelCompatibleWithSite 判断已知 alias 或 route key 是否能由指定站点处理。
// 未知 raw key 保持透传，因此返回 true。
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

// DefaultRequestModelIDsForSite 从站点路由规则中枚举公开别名。
func DefaultRequestModelIDsForSite(site Site) []string {
	ids := []string{}
	for alias := range AliasesForSite(site) {
		ids = append(ids, alias)
	}
	slices.Sort(ids)
	return ids
}

// DefaultRequestModelIDs 返回两个站点的路由别名并集。
func DefaultRequestModelIDs() []string {
	ids := append(DefaultRequestModelIDsForSite(SiteGlobal), DefaultRequestModelIDsForSite(SiteCN)...)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// ToAuthIdentity 将本地认证信息转换为用于构建 session 的 AuthIdentity。
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
