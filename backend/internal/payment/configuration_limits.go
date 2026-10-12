package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

type ConfigEasyPayCustomMethodDisplayConfig struct {
	DisplayNameLocalization *locale.Update[string] `json:"displayNameLocalization,omitempty"`
	Type                    string                 `json:"type"`
	DisplayName             string                 `json:"displayName"`
}

// GetAvailableMethodLimits collects all payment types from enabled provider
// instances and returns limits for each, plus the global widest range.
// Stripe instances share the "stripe" group, including legacy card/link subtype configurations.
func (s *ConfigService) GetAvailableMethodLimits(ctx context.Context) (*MethodLimitsResponse, error) {
	instances, err := s.store.ListInstances(ctx, InstanceFilter{EnabledOnly: true})
	if err != nil {
		return nil, fmt.Errorf("query provider instances: %w", err)
	}
	cfg, err := s.GetPaymentConfig(ctx)
	if err != nil {
		return nil, err
	}
	typeInstances := ConfigPcGroupByPaymentType(instances)
	typeInstances = s.ConfigPcApplyEnabledVisibleMethodInstances(ctx, typeInstances, instances)
	resp := &MethodLimitsResponse{
		Methods: make(map[string]MethodLimits, len(typeInstances)),
	}
	for pt, insts := range typeInstances {
		currency, ok := s.ConfigPcAggregateMethodCurrency(insts)
		if !ok {
			continue
		}
		ml := ConfigPcAggregateMethodLimits(pt, insts)
		ml = ConfigPcApplyEffectiveMethodFee(cfg, ml)
		ml.DisplayName = s.ConfigPcAggregateMethodDisplayName(pt, insts, locale.FromContext(ctx))
		ml.Currency = currency
		resp.Methods[ml.PaymentType] = ml
	}
	resp.GlobalMin, resp.GlobalMax = ConfigPcComputeGlobalRange(resp.Methods)
	return resp, nil
}

func (s *ConfigService) ConfigPcApplyEnabledVisibleMethodInstances(ctx context.Context, typeInstances map[string][]*ProviderInstance, instances []*ProviderInstance) map[string][]*ProviderInstance {
	if len(typeInstances) == 0 {
		return typeInstances
	}

	filtered := make(map[string][]*ProviderInstance, len(typeInstances))
	maps.Copy(filtered, typeInstances)

	for _, method := range []string{TypeAlipay, TypeWxpay} {
		matching := ConfigFilterEnabledVisibleMethodInstances(instances, method)
		providerKey, err := s.ConfigResolveVisibleMethodProviderKey(ctx, method, matching)
		if err != nil {
			delete(filtered, method)
			continue
		}
		if providerKey == "" {
			if len(matching) == 0 {
				delete(filtered, method)
				continue
			}
			filtered[method] = matching
			continue
		}
		selectedInstances := ConfigFilterVisibleMethodInstancesByProviderKey(instances, method, providerKey)
		if len(selectedInstances) == 0 {
			delete(filtered, method)
			continue
		}
		filtered[method] = selectedInstances
	}
	return filtered
}

func ConfigPcApplyEffectiveMethodFee(cfg *PaymentConfig, ml MethodLimits) MethodLimits {
	fee := cfg.EffectiveMethodFee(ml.PaymentType)
	ml.FixedFee = fee.FixedFee
	ml.FeeRate = fee.FeeRate
	return ml
}

func (s *ConfigService) ValidateMethodCurrencyConsistency(ctx context.Context, paymentType string) (string, error) {
	method := NormalizeVisibleMethod(paymentType)
	if method == "" || s == nil || s.store == nil {
		return DefaultPaymentCurrency, nil
	}

	instances, err := s.store.ListInstances(ctx, InstanceFilter{EnabledOnly: true})
	if err != nil {
		return "", fmt.Errorf("query provider instances: %w", err)
	}

	typeInstances := ConfigPcGroupByPaymentType(instances)
	typeInstances = s.ConfigPcApplyEnabledVisibleMethodInstances(ctx, typeInstances, instances)
	matching := typeInstances[method]
	if len(matching) == 0 {
		return DefaultPaymentCurrency, nil
	}

	currency, ok := s.ConfigPcAggregateMethodCurrency(matching)
	if !ok {
		return "", infraerrors.ServiceUnavailable(
			"PAYMENT_METHOD_CURRENCY_CONFLICT",
			"payment method has enabled provider instances with mixed currencies",
		).WithMetadata(map[string]string{"payment_type": method})
	}
	return currency, nil
}

func (s *ConfigService) ConfigPcAggregateMethodCurrency(instances []*ProviderInstance) (string, bool) {
	currency := ""
	for _, inst := range instances {
		next := s.ConfigPcInstancePaymentCurrency(inst)
		if next == "" {
			continue
		}
		if currency == "" {
			currency = next
			continue
		}
		if currency != next {
			return "", false
		}
	}
	if currency == "" {
		return DefaultPaymentCurrency, true
	}
	return currency, true
}

func (s *ConfigService) ConfigPcInstancePaymentCurrency(inst *ProviderInstance) string {
	if inst == nil {
		return DefaultPaymentCurrency
	}
	cfg := map[string]string{}
	if s != nil {
		decrypted := s.ConfigDecryptConfig(inst.Config)
		if decrypted != nil {
			cfg = decrypted
		}
	}
	return ProviderConfigCurrency(inst.ProviderKey, cfg)
}

func (s *ConfigService) ConfigPcAggregateMethodDisplayName(pt string, instances []*ProviderInstance, language string) string {
	pt = strings.TrimSpace(pt)
	if pt == "" {
		return ""
	}
	for _, inst := range instances {
		displayName := s.ConfigPcInstanceEasyPayCustomMethodDisplayName(inst, pt, language)
		if displayName != "" {
			return displayName
		}
	}
	return ""
}

func (s *ConfigService) ConfigPcInstanceEasyPayCustomMethodDisplayName(inst *ProviderInstance, pt string, language string) string {
	if inst == nil || inst.ProviderKey != TypeEasyPay {
		return ""
	}
	cfg := map[string]string{}
	if s != nil {
		decrypted := s.ConfigDecryptConfig(inst.Config)
		if decrypted != nil {
			cfg = decrypted
		}
	}
	raw := strings.TrimSpace(cfg["customMethods"])
	if raw == "" {
		return ""
	}

	var methods []ConfigEasyPayCustomMethodDisplayConfig
	if err := json.Unmarshal([]byte(raw), &methods); err != nil {
		return ""
	}
	for _, method := range methods {
		if strings.TrimSpace(method.Type) == pt {
			if method.DisplayNameLocalization != nil {
				value, _ := method.DisplayNameLocalization.Resolve(language)
				return value
			}
			return strings.TrimSpace(method.DisplayName)
		}
	}
	return ""
}

// ConfigPcGroupByPaymentType groups provider instances by visible payment method.
// Stripe has one Checkout entry; its Dashboard controls the methods shown inside Checkout.
// Each instance is counted once per group.
func ConfigPcGroupByPaymentType(instances []*ProviderInstance) map[string][]*ProviderInstance {
	typeInstances := make(map[string][]*ProviderInstance)
	seen := make(map[string]map[int64]bool)
	add := func(key string, inst *ProviderInstance) {
		if seen[key] == nil {
			seen[key] = make(map[int64]bool)
		}
		if !seen[key][inst.ID] {
			seen[key][inst.ID] = true
			typeInstances[key] = append(typeInstances[key], inst)
		}
	}
	for _, inst := range instances {
		if inst.ProviderKey == TypeStripe {
			add(TypeStripe, inst)
			continue
		}
		for _, t := range ConfigSplitTypes(inst.SupportedTypes) {
			add(t, inst)
		}
	}
	return typeInstances
}

// ConfigPcInstanceTypeLimits extracts per-type limits from a provider instance.
// Returns (limits, true) if configured; (zero, false) if unlimited.
// For Stripe instances, limits are stored under "stripe" key regardless of sub-types.
func ConfigPcInstanceTypeLimits(inst *ProviderInstance, pt string) (ChannelLimits, bool) {
	if inst.Limits == "" {
		return ChannelLimits{}, false
	}
	var limits InstanceLimits
	if err := json.Unmarshal([]byte(inst.Limits), &limits); err != nil {
		return ChannelLimits{}, false
	}
	cl, ok := limits[pt]
	return cl, ok
}

// ConfigUnionFloat merges a single limit value into the aggregate using UNION semantics.
//   - For "min" fields (wantMin=true): keeps the lowest non-zero value
//   - For "max"/"cap" fields (wantMin=false): keeps the highest non-zero value
//   - If any value is 0 (unlimited), the result is unlimited.
//
// Returns (aggregated value, still limited).
func ConfigUnionFloat(agg float64, limited bool, val float64, wantMin bool) (float64, bool) {
	if val == 0 {
		return agg, false
	}
	if !limited {
		return agg, false
	}
	if agg == 0 {
		return val, true
	}
	if wantMin && val < agg {
		return val, true
	}
	if !wantMin && val > agg {
		return val, true
	}
	return agg, true
}

// ConfigPcAggregateMethodLimits computes the UNION (least restrictive) of limits
// across all provider instances for a given payment type.
//
// Since the load balancer can route an order to any available instance,
// the user should see the widest possible range:
//   - SingleMin: lowest floor across instances; 0 if any is unlimited
//   - SingleMax: highest ceiling across instances; 0 if any is unlimited
//   - DailyLimit: highest cap across instances; 0 if any is unlimited
func ConfigPcAggregateMethodLimits(pt string, instances []*ProviderInstance) MethodLimits {
	ml := MethodLimits{PaymentType: pt}
	minLimited, maxLimited, dailyLimited := true, true, true

	for _, inst := range instances {
		cl, hasLimits := ConfigPcInstanceTypeLimits(inst, pt)
		if !hasLimits {
			return MethodLimits{PaymentType: pt} // any unlimited instance → all zeros
		}
		ml.SingleMin, minLimited = ConfigUnionFloat(ml.SingleMin, minLimited, cl.SingleMin, true)
		ml.SingleMax, maxLimited = ConfigUnionFloat(ml.SingleMax, maxLimited, cl.SingleMax, false)
		ml.DailyLimit, dailyLimited = ConfigUnionFloat(ml.DailyLimit, dailyLimited, cl.DailyLimit, false)
	}

	if !minLimited {
		ml.SingleMin = 0
	}
	if !maxLimited {
		ml.SingleMax = 0
	}
	if !dailyLimited {
		ml.DailyLimit = 0
	}
	return ml
}

// ConfigPcComputeGlobalRange computes the widest [min, max] across all methods.
// Uses the same union logic: lowest min, highest max, 0 if any is unlimited.
func ConfigPcComputeGlobalRange(methods map[string]MethodLimits) (globalMin, globalMax float64) {
	minLimited, maxLimited := true, true
	for _, ml := range methods {
		globalMin, minLimited = ConfigUnionFloat(globalMin, minLimited, ml.SingleMin, true)
		globalMax, maxLimited = ConfigUnionFloat(globalMax, maxLimited, ml.SingleMax, false)
	}
	if !minLimited {
		globalMin = 0
	}
	if !maxLimited {
		globalMax = 0
	}
	return globalMin, globalMax
}
