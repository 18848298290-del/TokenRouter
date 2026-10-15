package routing

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// group_routing_policy.go prepares group policy views and resolves model
// mappings, restrictions, and feature overrides.
// Group model permissions and mappings are read independently of shared price
// configurations. A policy view copies a group's model rules, allowed
// protocols, fallback routes, and enabled custom-list candidates before
// requestable catalog and billing metadata consumers use them.
//
// Model mapping selects one configured target, with exact case-insensitive
// names preceding sorted trailing-wildcard sources. Allowlist checks use the
// selected request stage and the shared price-name normalization, while an
// upstream-stage restriction is deferred until each provider supplies its final
// model. Malformed saved policy JSON produces an enabled empty allowlist
// restriction, so a decoding failure cannot silently widen model access.

// GroupPolicyView contains copied model rules and admission settings for
// routing consumers.
type GroupPolicyView struct {
	GroupRoutingPolicy // copied group model rules
	// AllowedProtocols and ProtocolFallbacks describe client entries and the
	// conversions usable on provider candidates. A conversion target can remain
	// closed as a client entry; protocol.CloneFallbacks copies the nested target
	// slices when GetGroupPolicy builds this view.
	// AllowedProtocols supplies RequestableResolver's admission set. Nil selects
	// its legacy model-only path, while an allocated empty slice rejects every
	// candidate.
	AllowedProtocols []protocol.ProtocolID // copied client admission selection
	// ProtocolFallbacks gives each entry its allowed conversion order. Missing
	// keys use automatic matching, and present empty slices require native
	// execution for that entry.
	ProtocolFallbacks map[protocol.ProtocolID][]protocol.ProtocolID // copied protocol conversion choices
	// RequireOAuthOnly excludes API-key providers in RequestableResolver when
	// AllowedProtocols is non-nil. Execution selectors apply their own OAuth
	// classification before choosing a provider for a request.
	RequireOAuthOnly bool // exclude API-key provider candidates
	// ModelsList supplies extra concrete request IDs only when the group's custom
	// list is enabled with entries; requestable resolution still verifies each
	// candidate's eligibility.
	ModelsList []string // enabled custom request-model candidates
}

// PlatformBoolOverride reads an optional boolean from flat or per-platform
// feature settings. Nil means the value is absent or has an unsupported shape,
// while a pointer to false preserves an explicit disablement.
//
// A flat boolean wins before legacy per-platform objects. A map[string]bool
// uses the platform string exactly as supplied; a map[string]any trims it and
// rejects an empty platform. Each accepted value receives separate pointer
// storage so callers can inspect false without confusing it with an omitted
// setting.
func PlatformBoolOverride(values map[string]any, key string, platform string) *bool {
	if values == nil {
		return nil
	}
	if v, ok := values[key].(bool); ok {
		return BoolOverridePtr(v)
	}
	// Legacy typed maps use their supplied platform key directly, retaining an
	// explicit false as a present override before generic JSON objects are
	// inspected.
	if typed, ok := values[key].(map[string]bool); ok {
		if value, found := typed[platform]; found {
			return BoolOverridePtr(value)
		}
	}
	raw, ok := values[key].(map[string]any)
	if !ok {
		return nil
	}
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return nil
	}
	if v, ok := raw[platform].(bool); ok {
		return BoolOverridePtr(v)
	}
	return nil
}

// BoolOverridePtr returns separately allocated pointer storage for a boolean
// override.
func BoolOverridePtr(v bool) *bool {
	return &v
}

// DecodeGroupRoutingPolicy decodes persisted group model rules and
// distinguishes absent input from a failed decode. Empty input returns a zero
// policy, leaving model restrictions inactive, while malformed nonempty JSON
// returns Enabled and RestrictModels with no allowed names.
//
// JSON decoding can assign earlier fields before discovering an error. Discard
// that partial value and return the enabled empty restriction so consumers deny
// every model instead of using whichever fields happened to decode. Valid JSON,
// including a zero policy, returns its decoded values.
func DecodeGroupRoutingPolicy(raw []byte) GroupRoutingPolicy {
	var policy GroupRoutingPolicy
	if len(raw) > 0 && json.Unmarshal(raw, &policy) != nil {
		// Discard partially decoded values on failure and install an empty enabled
		// restriction so malformed storage cannot permit models through a recovered
		// partial policy.
		return GroupRoutingPolicy{Enabled: true, RestrictModels: true}
	}
	return policy
}

// ValidateGroupRoutingPolicy checks the model restriction stage, mapping
// conflicts, and stored model patterns. It validates supplied spelling without
// rewriting it, permits trailing wildcards in mapping sources and allowlist
// patterns, and bounds pattern byte lengths and allowlist entry counts.
func ValidateGroupRoutingPolicy(p GroupRoutingPolicy) error {
	switch p.RestrictionModelSource {
	case "", BillingModelSourceRequested, BillingModelSourceGroupMapped, BillingModelSourceUpstream:
	default:
		return apperror.BadRequest("INVALID_ROUTING_POLICY", "invalid restriction_model_source")
	}
	if err := validateNoConflictingMappings(p.ModelMapping); err != nil {
		return err
	}
	// Mapping names are bounded to 512 bytes, matching validation of individual
	// allowlist patterns rather than counting Unicode characters.
	for source, target := range p.ModelMapping {
		if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" || len(source) > 512 || len(target) > 512 || strings.Contains(strings.TrimSuffix(source, "*"), "*") {
			return apperror.BadRequest("INVALID_ROUTING_POLICY", "invalid model mapping")
		}
	}
	// Limit stored allowlists to one thousand entries.
	if len(p.AllowedModels) > 1000 {
		return apperror.BadRequest("INVALID_ROUTING_POLICY", "invalid model allowlist")
	}
	// Allowlist names share the mapping-name byte ceiling.
	for _, model := range p.AllowedModels {
		if strings.TrimSpace(model) == "" || len(model) > 512 || strings.Contains(strings.TrimSuffix(model, "*"), "*") {
			return apperror.BadRequest("INVALID_ROUTING_POLICY", "invalid model allowlist pattern")
		}
	}
	return nil
}

// GetGroupPolicy loads the requested group and returns its routing and
// protocol settings in separate collections. Missing service, reader, or group
// yields nil, and read errors propagate.
//
// Disabled routing policy produces zero model rules while admission settings
// and enabled custom-list entries remain available. GroupRoutingPolicy.Clone
// copies supported nested map and slice shapes; other dynamic feature values
// can retain references, so callers keep those shared values immutable.
func (s *PricingConfigService) GetGroupPolicy(ctx context.Context, groupID int64) (*GroupPolicyView, error) {
	if s == nil || s.options.ReadGroup == nil {
		return nil, nil
	}
	group, err := s.options.ReadGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, nil
	}
	policy := group.RoutingPolicy.Clone()
	if !policy.Enabled {
		policy = GroupRoutingPolicy{}
	}
	var models []string
	if group.CustomModelsListEnabled() {
		models = append([]string(nil), group.ModelsListConfig.Models...)
	}
	return &GroupPolicyView{ModelsList: models, GroupRoutingPolicy: policy, AllowedProtocols: append([]protocol.ProtocolID{}, group.AllowedProtocols...), ProtocolFallbacks: protocol.CloneFallbacks(group.ProtocolFallbacks), RequireOAuthOnly: group.RequireOAuthOnly}, nil
}

// RestrictionSource returns the configured model stage used for allowlist
// checks. A nil policy or an empty stored stage selects group_mapped, matching
// the default applied when a mapping result is initialized.
func (p *GroupPolicyView) RestrictionSource() string {
	if p == nil || p.RestrictionModelSource == "" {
		return BillingModelSourceGroupMapped
	}
	return p.RestrictionModelSource
}

// ResolveModel applies one group mapping and returns its configured target
// verbatim. A nil view or unmatched name retains the input, and exact
// case-insensitive matches are tested before wildcard prefixes.
//
// Mapping sources are sorted case-insensitively with their original spelling as
// a tie breaker. The first matching prefix in that order wins, rather than a
// longest-prefix rule. Targets are returned directly, so chained mappings
// require another deliberate caller action and are not expanded by this method.
func (p *GroupPolicyView) ResolveModel(model string) string {
	if p == nil {
		return model
	}
	rules := p.ModelMapping
	for _, source := range sortedModelMappingSources(rules) {
		if strings.EqualFold(source, model) {
			return rules[source]
		}
	}
	// An unmatched exact source permits the sorted wildcard pass. Keeping the
	// same source order makes overlapping patterns resolve independently of Go's
	// map iteration.
	for _, source := range sortedModelMappingSources(rules) {
		if strings.HasSuffix(source, "*") && strings.HasPrefix(strings.ToLower(model), strings.ToLower(strings.TrimSuffix(source, "*"))) {
			return rules[source]
		}
	}
	return model
}

// IsModelRestricted reports whether the supplied model fails the group's
// enabled allowlist. Nil views and RestrictModels=false permit the model, while
// an enabled empty allowlist rejects every model.
//
// The model and each pattern prefix are trimmed and lowercased through
// pricing.NormalizePriceModelName. Exact patterns compare full normalized
// names; trailing asterisks also admit names with that normalized prefix.
// Normalization keeps version and supplier text in the name, so patterns
// compare the complete identity at the configured request stage.
func (p *GroupPolicyView) IsModelRestricted(model string) bool {
	if p == nil || !p.RestrictModels {
		return false
	}
	model = pricing.NormalizePriceModelName(model)
	for _, pattern := range p.AllowedModels {
		wildcard := strings.HasSuffix(pattern, "*")
		prefix := pricing.NormalizePriceModelName(strings.TrimSuffix(pattern, "*"))
		if model == prefix || (wildcard && strings.HasPrefix(model, prefix)) {
			return false
		}
	}
	return true
}

// IsWebSearchEmulationEnabled reads the exact platform flag from the
// web_search_emulation object. Nil views, unsupported object shapes, missing
// flags, and nonboolean entries disable emulation; the platform string is used
// without normalization.
func (p *GroupPolicyView) IsWebSearchEmulationEnabled(platform string) bool {
	if p == nil {
		return false
	}
	values, ok := p.FeaturesConfig[featureKeyWebSearchEmulation].(map[string]any)
	if !ok {
		return false
	}
	enabled, ok := values[platform].(bool)
	return ok && enabled
}

// IsBedrockCCCompatEnabled reads the platform-compatible Bedrock CC override
// from stored feature settings. Nil views and absent or invalid overrides
// return false, while a flat or supported per-platform boolean follows
// PlatformBoolOverride.
func (p *GroupPolicyView) IsBedrockCCCompatEnabled(platform string) bool {
	if p == nil {
		return false
	}
	value := PlatformBoolOverride(p.FeaturesConfig, featureKeyBedrockCCCompat, platform)
	return value != nil && *value
}

// ModelForRestriction selects the model for a request-stage allowlist check.
// The upstream stage returns an empty string so callers defer checking to
// individual provider models; empty or unsupported sources use the group-mapped
// model.
func ModelForRestriction(source, requestedModel, groupMappedModel string) string {
	switch source {
	case BillingModelSourceRequested:
		return requestedModel
	case BillingModelSourceUpstream:
		return ""
	case BillingModelSourceGroupMapped:
		return groupMappedModel
	default:
		// Unrecognized stored stages still need an admission identity. Use the
		// group-mapped default so early callers can evaluate the saved allowlist.
		return groupMappedModel
	}
}
