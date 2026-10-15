package modelcatalog

import "strings"

// qoder.go maps Qoder site-specific route keys to public identities and
// execution capabilities.
// Qoder public model names and route keys are matched within a selected site.
// Global and CN routes can share a key while selecting different public
// identities, so site qualification participates in aliasing, metadata lookup,
// input limits, and declared reasoning controls.
//
// The route table keeps those representations together. Empty route sites
// apply to either recognized site, while an empty caller site selects global.
// Unknown sites produce no route, and unknown models use a fixed execution
// input limit with runtime context selection disabled. Some registered service
// keys have no public catalog identity and therefore retain unknown metadata.

// QoderFallbackInputLimit supplies the input token limit when no registered
// site and model route can be resolved.
const QoderFallbackInputLimit = 200000

// qoderRoutes binds site-qualified public names to service keys, public catalog
// identities, and supported request controls.
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

// qoderRoute describes public IDs, service routes, and parameter support in the
// Qoder 1.24.2 integration.
type qoderRoute struct {
	// Site restricts Model and Key matching, with empty Site applying to both
	// recognized sites. Identity names the public catalog model for that route
	// and can be empty for service keys without public metadata.
	Site, Model, Key, Identity string   // applicable route site, public model name, upstream service key, public catalog identity
	Input                      int      // tokens
	Selectable, Toggle         bool     // runtime context selection and reasoning toggle support
	Efforts                    []string // supported reasoning effort choices
}

// QoderAliases returns public-name to service-key mappings for one site.
// An empty site selects global, while an unrecognized site returns an empty
// map; callers can edit the returned map independently.
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

// QoderModelIdentity returns the public catalog identity for a registered
// public name or service key. Unknown routes and registered keys lacking
// public metadata return an empty identity.
func QoderModelIdentity(site, model string) string {
	if route, found := findQoderRoute(site, model); found {
		return route.Identity
	}
	// No registered site route identifies this input. Leave the public identity
	// empty so metadata resolution retains unknown attributes instead of
	// selecting another route.
	return ""
}

// QoderInputLimit returns a route's input token limit and runtime context
// selection flag. Unknown routes use QoderFallbackInputLimit with selection
// disabled; empty sites follow the global routing default.
func QoderInputLimit(site, model string) (int, bool) {
	if route, found := findQoderRoute(site, model); found {
		return route.Input, route.Selectable
	}
	// Unregistered models have no route-specific context rule. Retain the fixed
	// execution default and disable context selection for attribute consumers
	// of this input.
	return QoderFallbackInputLimit, false
}

// QoderReasoningOptions returns independently populated toggle and effort
// controls for a registered route. Unknown routes and routes with no declared
// controls return an explicit empty list for attribute resolution.
func QoderReasoningOptions(site, model string) []ReasoningOption {
	result := []ReasoningOption{}
	route, found := findQoderRoute(site, model)
	if !found {
		// An unknown route supplies no declared reasoning controls. Keep an
		// explicit empty result for callers that apply Qoder's available
		// parameter choices.
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

// findQoderRoute matches a normalized public name or service key within one
// recognized site. Empty sites select global; unknown sites and unmatched
// models return false, while returned effort slices borrow route table storage.
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
