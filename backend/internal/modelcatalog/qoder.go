package modelcatalog

import "strings"

const QoderFallbackInputLimit = 200000

var qoderRoutes = []qoderRoute{
	{Site: "global", Model: "claude-opus-4-6", Key: "ultimate", Identity: "anthropic/claude-opus-4-6", Input: 1000000, Selectable: true},
	{Site: "global", Model: "performance", Key: "performance", Input: 1000000, Selectable: true},
	{Site: "global", Model: "efficient", Key: "efficient", Input: 180000},
	{Site: "global", Model: "lite", Key: "lite", Input: 180000},
	{Model: "auto", Key: "auto", Input: 180000},
	{Model: "qwen3.8-max", Key: "qmodel_38max", Identity: "alibaba/qwen3.8-max", Input: 1000000, Selectable: true, Toggle: true},
	{Model: "qwen3.7-max", Key: "qmodel_latest", Identity: "alibaba/qwen3.7-max", Input: 1000000, Selectable: true, Toggle: true},
	{Model: "qwen3.7-plus", Key: "qmodel", Identity: "alibaba/qwen3.7-plus", Input: 1000000, Selectable: true, Toggle: true},
	{Site: "cn", Model: "qwen3.6-flash", Key: "q36fmodel", Identity: "alibaba/qwen3.6-flash", Input: 1000000, Selectable: true},
	{Site: "global", Model: "kimi-k3", Key: "kmodel_latest", Identity: "moonshotai/kimi-k3", Input: 1000000, Selectable: true},
	{Model: "kimi-k2.7-code", Key: "kmodel", Identity: "moonshotai/kimi-k2.7-code", Input: 256000, Selectable: true},
	{Model: "glm-5.3", Key: "gmodel", Identity: "zhipuai/glm-5.3", Input: 1000000, Selectable: true, Toggle: true, Efforts: []string{"low", "high", "max"}},
	{Model: "glm-5.2", Key: "gm51model", Identity: "zhipuai/glm-5.2", Input: 1000000, Selectable: true, Toggle: true, Efforts: []string{"high", "max"}},
	{Model: "deepseek-v4-pro", Key: "dmodel", Identity: "deepseek/deepseek-v4-pro", Input: 1000000, Selectable: true, Toggle: true, Efforts: []string{"high", "max"}},
	{Model: "deepseek-v4-flash", Key: "dfmodel", Identity: "deepseek/deepseek-v4-flash", Input: 1000000, Selectable: true, Toggle: true, Efforts: []string{"high", "max"}},
	{Site: "global", Model: "minimax-m3", Key: "mmodel", Identity: "minimax/minimax-m3", Input: 1000000, Selectable: true},
	{Site: "cn", Model: "minimax-m2.7", Key: "mmodel", Identity: "minimax/minimax-m2.7", Input: 200000, Selectable: true},
}

// qoderRoute describes public IDs, service routes, and parameter support in the Qoder 1.24.2 integration.
type qoderRoute struct {
	Site, Model, Key, Identity string
	Input                      int
	Selectable, Toggle         bool
	Efforts                    []string
}

func QoderAliases(site string) map[string]string {
	if site == "" {
		site = "global"
	}
	result := map[string]string{}
	for _, route := range qoderRoutes {
		if site == "global" || site == "cn" {
			if route.Site == "" || route.Site == site {
				result[route.Model] = route.Key
			}
		}
	}
	return result
}

func QoderModelIdentity(site, model string) string {
	if route, found := findQoderRoute(site, model); found {
		return route.Identity
	}
	return ""
}

func QoderInputLimit(site, model string) (int, bool) {
	if route, found := findQoderRoute(site, model); found {
		return route.Input, route.Selectable
	}
	return QoderFallbackInputLimit, false
}

func QoderReasoningOptions(site, model string) []ReasoningOption {
	result := []ReasoningOption{}
	route, found := findQoderRoute(site, model)
	if !found {
		return result
	}
	if route.Toggle {
		result = append(result, ReasoningOption{Type: "toggle"})
	}
	if len(route.Efforts) > 0 {
		option := ReasoningOption{Type: "effort"}
		for _, effort := range route.Efforts {
			option.Values = append(option.Values, valuePointer(effort))
		}
		result = append(result, option)
	}
	return result
}

func findQoderRoute(site, model string) (qoderRoute, bool) {
	if site == "" {
		site = "global"
	}
	if site != "global" && site != "cn" {
		return qoderRoute{}, false
	}
	model = strings.ToLower(strings.TrimSpace(model))
	for _, route := range qoderRoutes {
		if (route.Site == "" || route.Site == site) && (route.Model == model || route.Key == model) {
			return route, true
		}
	}
	return qoderRoute{}, false
}
