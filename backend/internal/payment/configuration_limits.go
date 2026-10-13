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

// Visible payment limits describe the widest configured purchase range across
// eligible merchant instances. A missing/unreadable channel limit entry
// removes every bound for that method, while a zero value in a present entry
// removes only its corresponding bound; the retained limited flag
// distinguishes those states from an initial aggregate value.
//
// Official/EasyPay visible source resolution narrows or unions eligible
// Alipay/WeChat instances before aggregation. Mixed currencies omit a limits
// display or return a checkout error rather than combining their money values.
// Stripe groups by its provider key, while other initial groups use stored
// support labels. Custom EasyPay names choose the first usable instance
// display and its localization precedence. Instance, source and configuration
// reads remain separate observations, and these display unions do not reserve
// channel capacity.

// ConfigEasyPayCustomMethodDisplayConfig decodes the custom method identity
// and display choices in EasyPay configuration.
type ConfigEasyPayCustomMethodDisplayConfig struct {
	// DisplayNameLocalization takes precedence whenever nonnil, including an
	// empty resolved string. Its resolution error is discarded here; plain
	// DisplayName is used only when this object is absent.
	DisplayNameLocalization *locale.Update[string] `json:"displayNameLocalization,omitempty"`
	Type                    string                 `json:"type"` // custom payment method label
	// DisplayName is trimmed and returned for a matching custom method only when
	// DisplayNameLocalization is nil. The caller can then select the first
	// nonempty instance display.
	DisplayName string `json:"displayName"`
}

// GetAvailableMethodLimits returns method displays, currency-consistent bounds
// and their global widest range from current enabled instances.
// Instance/configuration reads can fail independently and abort the response;
// source-resolution errors omit their canonical visible method group.
//
// Stripe uses its provider group while other initial groups retain support
// labels. Alipay/WeChat source selection can narrow instances or retain
// cross-provider matches. A mixed-currency group is skipped, then limits,
// effective fees and the first custom display are assembled for each remaining
// key. The returned maps are newly built, and their unions describe configured
// availability without reserving an amount against daily usage.
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
		// A method group with mixed interpreted currencies cannot supply one numeric
		// display range. Omit that group here; checkout uses the separate
		// error-returning consistency operation for its request.
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

// ConfigPcApplyEnabledVisibleMethodInstances returns a shallow group-map copy
// with canonical Alipay/WeChat groups resolved from enabled support matches.
// Empty group input returns its original map; other group slices and instance
// pointers remain shared.
//
// A source-resolution error removes that canonical group and is discarded. An
// absent source with matching instances keeps their union, while a selected
// key retains only its enabled support matches. Empty matches remove the
// group. Initial unrelated/raw support-label groups remain copied, so this
// operation does not normalize or remove every legacy label in the input.
func (s *ConfigService) ConfigPcApplyEnabledVisibleMethodInstances(ctx context.Context, typeInstances map[string][]*ProviderInstance, instances []*ProviderInstance) map[string][]*ProviderInstance {
	if len(typeInstances) == 0 {
		return typeInstances
	}

	filtered := make(map[string][]*ProviderInstance, len(typeInstances))
	maps.Copy(filtered, typeInstances)

	for _, method := range []string{TypeAlipay, TypeWxpay} {
		matching := ConfigFilterEnabledVisibleMethodInstances(instances, method)
		providerKey, err := s.ConfigResolveVisibleMethodProviderKey(ctx, method, matching)
		// A source-selection failure leaves this canonical method unavailable in the
		// limits display. Remove its group and continue other methods rather than
		// exposing an error from this filtering helper.
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

// ConfigPcApplyEffectiveMethodFee returns a value copy of method limits with
// its effective fixed/percentage fees. PaymentConfig.EffectiveMethodFee
// accepts nil configuration and selects enabled override or legacy rate
// according to the normalized method.
func ConfigPcApplyEffectiveMethodFee(cfg *PaymentConfig, ml MethodLimits) MethodLimits {
	fee := cfg.EffectiveMethodFee(ml.PaymentType)
	ml.FixedFee = fee.FixedFee
	ml.FeeRate = fee.FeeRate
	return ml
}

// ValidateMethodCurrencyConsistency returns one currency for the requested
// normalized method's selected enabled instance group. Blank method, nil
// dependencies or absent matching group returns default CNY; initial instance
// errors remain visible.
//
// Visible source handling can discard resolution errors by removing its
// canonical group, which then uses that absent-group default. A nonempty group
// with differing configured currencies returns
// PAYMENT_METHOD_CURRENCY_CONFLICT with method metadata. These observations
// precede checkout's actual instance selection and selected-currency recheck,
// so the result is a method-level observation rather than a persisted merchant
// binding.
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

// ConfigPcAggregateMethodCurrency reports one shared instance currency or
// false for a detected mismatch. Each row is interpreted through
// ConfigPcInstancePaymentCurrency, and an otherwise empty set uses default
// CNY.
//
// The first nonempty interpreted currency establishes the comparison value,
// and later differing values reject the group. Configuration absence or
// recoverable decoding failure can use ProviderConfigCurrency's defaults
// instead of removing the row. GetAvailableMethodLimits omits conflicting
// groups, while ValidateMethodCurrencyConsistency turns that same result into
// a checkout error.
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

// ConfigPcInstancePaymentCurrency resolves one instance's provider currency
// using current stored config interpretation. Nil instance returns CNY; nil
// service or recoverable config-decode absence supplies an empty map to
// ProviderConfigCurrency and its defaults.
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

// ConfigPcAggregateMethodDisplayName returns the first nonempty custom display
// found in the supplied instance order. Blank requested label returns empty;
// it does not sort instances or prefer a later localization once a nonempty
// earlier value wins.
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

// ConfigPcInstanceEasyPayCustomMethodDisplayName reads one exact EasyPay
// instance's custom method JSON and returns its matching display for pt.
// Missing/non-EasyPay input or unreadable configuration/method JSON yields
// empty.
//
// The first trimmed method Type equal to pt wins. A nonnil localization object
// takes precedence and returns its resolved value with resolution error
// discarded, including empty text; only its absence permits plain trimmed
// DisplayName. The outer aggregate may try another instance after an empty
// result, but this helper does not try a later duplicate custom method or
// plain-name fallback for that matched object.
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
	// Unreadable custom-method JSON supplies no display label for this instance.
	// Return empty so the outer ordered aggregate can still try a different
	// instance's usable custom name.
	if err := json.Unmarshal([]byte(raw), &methods); err != nil {
		return ""
	}
	for _, method := range methods {
		if strings.TrimSpace(method.Type) == pt {
			if method.DisplayNameLocalization != nil {
				// Presence of localization selects its resolved text even when resolution
				// reports an error or empty value. Keep that precedence over plain display
				// text for this matched object.
				value, _ := method.DisplayNameLocalization.Resolve(language)
				return value
			}
			return strings.TrimSpace(method.DisplayName)
		}
	}
	return ""
}

// ConfigPcGroupByPaymentType groups nonnil instance rows in input order,
// deduplicating numeric instance IDs within each group. Callers supply already
// eligible rows; enabled state is not inspected here.
//
// Exact Stripe provider identity contributes one Stripe checkout group
// independently of its supported subtype list. Other providers expand trimmed
// ConfigSplitTypes labels without visible alias normalization, preserving
// those raw group keys for later handling. Two rows with the same ID/key group
// contribute only the first pointer, while the same ID can appear under
// different keys. Source-resolution code separately updates the canonical
// Alipay/WeChat groups.
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
		// Stripe hosted subtype lists describe methods inside one checkout provider.
		// Group its instance once under stripe so limit lookup uses that persisted
		// provider-level entry.
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

// ConfigPcInstanceTypeLimits reads the exact supplied key from a nonnil
// instance's JSON limit map and reports whether it exists.
// Missing/empty/malformed JSON yields zero,false; a present all-zero entry
// returns true. Stripe grouping supplies stripe as pt rather than this helper
// substituting a key.
func ConfigPcInstanceTypeLimits(inst *ProviderInstance, pt string) (ChannelLimits, bool) {
	if inst.Limits == "" {
		return ChannelLimits{}, false
	}
	var limits InstanceLimits
	// Treat unreadable limit JSON as an absent exact entry for this display
	// calculation. ConfigPcAggregateMethodLimits removes all bounds because one
	// eligible instance contributes no usable configured limits.
	if err := json.Unmarshal([]byte(inst.Limits), &limits); err != nil {
		return ChannelLimits{}, false
	}
	cl, ok := limits[pt]
	return cl, ok
}

// ConfigUnionFloat merges one numeric bound with a separate limited flag,
// choosing lower minima or higher caps while zero disables that bound. Once
// limited is false, later nonzero values keep that disabled state and the
// intermediate aggregate value.
//
// An initial zero aggregate can therefore mean not yet seeded, whereas an
// incoming zero means unlimited. Callers clear the exposed value after
// observing false, so they do not publish a stale intermediate bound. Nonzero
// negative values are compared as supplied; this helper performs no validation
// of whether the configured bound is meaningful for purchases.
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

// ConfigPcAggregateMethodLimits computes the widest bound values for one exact
// group label and its nonnil instance rows. A missing/unreadable entry on any
// row returns all-zero bounds immediately, while present zero components
// disable only their corresponding aggregate.
//
// Lowest nonzero single minimum and highest nonzero single maximum/daily cap
// are accumulated with separate limited flags. Disabled components are set to
// zero after the loop, retaining unlimited state through later bounded rows.
// Empty input leaves zero values. This describes configured range, not
// currently remaining daily capacity, and exact limit lookup does not inherit
// an alias entry.
func ConfigPcAggregateMethodLimits(pt string, instances []*ProviderInstance) MethodLimits {
	ml := MethodLimits{PaymentType: pt}
	minLimited, maxLimited, dailyLimited := true, true, true

	for _, inst := range instances {
		cl, hasLimits := ConfigPcInstanceTypeLimits(inst, pt)
		// One instance with no usable exact limit entry broadens the method to
		// all-zero display bounds. Return before other instances can reintroduce a
		// restrictive configured range.
		if !hasLimits {
			return MethodLimits{PaymentType: pt}
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

// ConfigPcComputeGlobalRange returns the lowest single minimum and highest
// single maximum across displayed methods with zero meaning unlimited. Empty
// input returns zero values.
//
// Each side has its own limited flag, so a method unlimited only on one side
// does not disable the other side. Once a side is unlimited, later bounded
// methods leave that state intact and the final exposed value is cleared to
// zero. Currency groups are not converted here; callers already decide which
// individual methods can be displayed.
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
