package routing

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// GroupPolicyView contains the group's routing rules and allowed protocols.
type GroupPolicyView struct {
	GroupRoutingPolicy
	AllowedProtocols  []protocol.ProtocolID
	ProtocolFallbacks map[protocol.ProtocolID][]protocol.ProtocolID
	RequireOAuthOnly  bool
	// ModelsList contains enabled custom display IDs used as catalog candidates.
	ModelsList []string
}

// PlatformBoolOverride reads a boolean override, including legacy per-platform objects.
// It returns nil for missing or incorrectly typed values so the caller can apply its default.
func PlatformBoolOverride(values map[string]any, key string, platform string) *bool {
	if values == nil {
		return nil
	}
	if v, ok := values[key].(bool); ok {
		return BoolOverridePtr(v)
	}
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

func BoolOverridePtr(v bool) *bool {
	return &v
}

// DecodeGroupRoutingPolicy parses stored rules and returns a deny-all model policy for malformed data.
func DecodeGroupRoutingPolicy(raw []byte) GroupRoutingPolicy {
	var policy GroupRoutingPolicy
	if len(raw) > 0 && json.Unmarshal(raw, &policy) != nil {
		return GroupRoutingPolicy{Enabled: true, RestrictModels: true}
	}
	return policy
}

// ValidateGroupRoutingPolicy checks the allowlist stage and model rules before storage.
func ValidateGroupRoutingPolicy(p GroupRoutingPolicy) error {
	switch p.RestrictionModelSource {
	case "", BillingModelSourceRequested, BillingModelSourceGroupMapped, BillingModelSourceUpstream:
	default:
		return apperror.BadRequest("INVALID_ROUTING_POLICY", "invalid restriction_model_source")
	}
	if err := validateNoConflictingMappings(p.ModelMapping); err != nil {
		return err
	}
	for source, target := range p.ModelMapping {
		if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" || len(source) > 512 || len(target) > 512 || strings.Contains(strings.TrimSuffix(source, "*"), "*") {
			return apperror.BadRequest("INVALID_ROUTING_POLICY", "invalid model mapping")
		}
	}
	if len(p.AllowedModels) > 1000 {
		return apperror.BadRequest("INVALID_ROUTING_POLICY", "invalid model allowlist")
	}
	for _, model := range p.AllowedModels {
		if strings.TrimSpace(model) == "" || len(model) > 512 || strings.Contains(strings.TrimSuffix(model, "*"), "*") {
			return apperror.BadRequest("INVALID_ROUTING_POLICY", "invalid model allowlist pattern")
		}
	}
	return nil
}

// GetGroupPolicy copies policy from the injected group reader, which can use the trusted request snapshot.
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

func (p *GroupPolicyView) RestrictionSource() string {
	if p == nil || p.RestrictionModelSource == "" {
		return BillingModelSourceGroupMapped
	}
	return p.RestrictionModelSource
}

// ResolveModel applies one group mapping, trying exact matches before ordered prefix patterns.
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
	for _, source := range sortedModelMappingSources(rules) {
		if strings.HasSuffix(source, "*") && strings.HasPrefix(strings.ToLower(model), strings.ToLower(strings.TrimSuffix(source, "*"))) {
			return rules[source]
		}
	}
	return model
}

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

// IsWebSearchEmulationEnabled reads the platform flag and returns false for missing or non-object configuration.
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

func (p *GroupPolicyView) IsBedrockCCCompatEnabled(platform string) bool {
	if p == nil {
		return false
	}
	value := PlatformBoolOverride(p.FeaturesConfig, featureKeyBedrockCCCompat, platform)
	return value != nil && *value
}

// ModelForRestriction selects the model for the configured allowlist stage.
// The upstream stage returns an empty string because it requires a per-provider check.
func ModelForRestriction(source, requestedModel, groupMappedModel string) string {
	switch source {
	case BillingModelSourceRequested:
		return requestedModel
	case BillingModelSourceUpstream:
		return ""
	case BillingModelSourceGroupMapped:
		return groupMappedModel
	default:
		return groupMappedModel
	}
}
