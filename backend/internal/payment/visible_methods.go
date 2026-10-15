package payment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// visible_methods.go resolves enabled visible payment methods to official or
// EasyPay provider sources.
// Visible Alipay and WeChat methods can be supplied by official adapters or
// EasyPay instances, so an enabled method alone does not determine its
// provider key. Official adapters infer their own method from blank
// supportedTypes, while EasyPay uses its explicit supported-method list.
//
// Resolution first groups eligible instances by provider key. A single
// distinct key wins directly; multiple keys consult the saved source setting
// and verify that it names a matching instance. An absent source with multiple
// matches produces an empty-key marker that selection interprets across
// providers. Instance filters check enabled state, but source normalization
// and direct key selection have their own caller preconditions; they do not
// independently prove that every input row is enabled.

// ConfigEnabledVisibleMethodsForProvider returns normalized supported visible
// methods for official Alipay/WeChat or EasyPay. Official blank support
// implies its own method; EasyPay expands the supplied list, and unsupported
// provider keys yield an empty result.
//
// A set collapses aliases and duplicates, then output places Alipay and WeChat
// first and follows remaining support order. The function reads configuration
// labels without an instance enabled flag. Callers that expose enabled methods
// check that flag separately through ConfigProviderSupportsVisibleMethod.
func ConfigEnabledVisibleMethodsForProvider(providerKey, supportedTypes string) []string {
	methodSet := make(map[string]struct{}, 2)
	addMethod := func(method string) {
		method = NormalizeVisibleMethod(method)
		if method != "" {
			methodSet[method] = struct{}{}
		}
	}

	switch strings.TrimSpace(providerKey) {
	case TypeAlipay:
		if strings.TrimSpace(supportedTypes) == "" {
			addMethod(TypeAlipay)
			break
		}
		for _, supportedType := range ConfigSplitTypes(supportedTypes) {
			if NormalizeVisibleMethod(supportedType) == TypeAlipay {
				addMethod(TypeAlipay)
				break
			}
		}
	case TypeWxpay:
		if strings.TrimSpace(supportedTypes) == "" {
			addMethod(TypeWxpay)
			break
		}
		for _, supportedType := range ConfigSplitTypes(supportedTypes) {
			if NormalizeVisibleMethod(supportedType) == TypeWxpay {
				addMethod(TypeWxpay)
				break
			}
		}
	case TypeEasyPay:
		for _, supportedType := range ConfigSplitTypes(supportedTypes) {
			addMethod(supportedType)
		}
	}

	methods := make([]string, 0, len(methodSet))
	for _, method := range []string{TypeAlipay, TypeWxpay} {
		if _, ok := methodSet[method]; ok {
			methods = append(methods, method)
			delete(methodSet, method)
		}
	}
	for _, supportedType := range ConfigSplitTypes(supportedTypes) {
		method := NormalizeVisibleMethod(supportedType)
		if _, ok := methodSet[method]; ok {
			methods = append(methods, method)
			delete(methodSet, method)
		}
	}
	return methods
}

// ConfigProviderSupportsVisibleMethod reports whether a nonnil enabled
// instance exposes the normalized visible method through its provider-specific
// support rules. It rejects disabled rows before method expansion, so
// filtering callers can retain only eligible instances.
func ConfigProviderSupportsVisibleMethod(inst *ProviderInstance, method string) bool {
	if inst == nil || !inst.Enabled {
		return false
	}
	method = NormalizeVisibleMethod(method)
	return slices.Contains(ConfigEnabledVisibleMethodsForProvider(inst.ProviderKey, inst.SupportedTypes), method)
}

// ConfigFilterEnabledVisibleMethodInstances returns enabled instances
// supporting the visible method in input order. The returned slice is new,
// while its instance pointers remain shared with the supplied collection.
func ConfigFilterEnabledVisibleMethodInstances(instances []*ProviderInstance, method string) []*ProviderInstance {
	filtered := make([]*ProviderInstance, 0, len(instances))
	for _, inst := range instances {
		if ConfigProviderSupportsVisibleMethod(inst, method) {
			filtered = append(filtered, inst)
		}
	}
	return filtered
}

// ConfigFilterVisibleMethodInstancesByProviderKey returns enabled method
// matches whose trimmed keys match case-insensitively. It keeps input order
// and shared instance pointers while constructing a new filtered slice for
// availability calculations.
func ConfigFilterVisibleMethodInstancesByProviderKey(instances []*ProviderInstance, method string, providerKey string) []*ProviderInstance {
	filtered := make([]*ProviderInstance, 0, len(instances))
	for _, inst := range instances {
		if !ConfigProviderSupportsVisibleMethod(inst, method) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(inst.ProviderKey), strings.TrimSpace(providerKey)) {
			continue
		}
		filtered = append(filtered, inst)
	}
	return filtered
}

// ConfigDistinctVisibleMethodProviderKeys returns first-seen trimmed nonempty
// keys, collapsing case-insensitive duplicates. It skips nil rows but does not
// inspect enabled or support flags; callers supply the matching instance set.
func ConfigDistinctVisibleMethodProviderKeys(instances []*ProviderInstance) []string {
	seen := make(map[string]struct{}, len(instances))
	keys := make([]string, 0, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		key := strings.TrimSpace(inst.ProviderKey)
		if key == "" {
			continue
		}
		normalized := strings.ToLower(key)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// ConfigSelectVisibleMethodInstanceByProviderKey returns the first
// case-insensitive trimmed key match or nil for an empty/missing key. Inputs
// are nonnil selected instances; this helper relies on prior enabled/support
// filtering.
func ConfigSelectVisibleMethodInstanceByProviderKey(instances []*ProviderInstance, providerKey string) *ProviderInstance {
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return nil
	}
	for _, inst := range instances {
		if strings.EqualFold(strings.TrimSpace(inst.ProviderKey), providerKey) {
			return inst
		}
	}
	return nil
}

// ConfigValidateVisibleMethodEnablementConflicts accepts overlapping visible
// method claims and returns nil for every supplied combination. Source
// resolution chooses official/EasyPay routing later, so this compatibility
// validation does not reject multiple enabled claimants.
func (s *ConfigService) ConfigValidateVisibleMethodEnablementConflicts(
	ctx context.Context,
	excludeID int64,
	providerKey string,
	supportedTypes string,
	enabled bool,
) error {
	_, _, _, _, _ = ctx, excludeID, providerKey, supportedTypes, enabled
	return nil
}

// ConfigResolveVisibleMethodSourceProviderKey reads and normalizes the saved
// source for the requested visible method. Missing settings or dependencies
// act as an absent source; other read errors are returned. A normalized empty
// source returns an empty key, while unsupported configured source yields
// INVALID_PAYMENT_VISIBLE_METHOD_SOURCE.
func (s *ConfigService) ConfigResolveVisibleMethodSourceProviderKey(ctx context.Context, method string) (string, error) {
	method = NormalizeVisibleMethod(method)
	sourceKey := ResumeVisibleMethodSourceSettingKey(method)
	rawSource := ""
	if s != nil && s.settingRepo != nil && sourceKey != "" {
		value, err := s.settingRepo.GetValue(ctx, sourceKey)
		if err != nil {
			// An absent setting leaves source selection undecided so matching providers
			// can still be used. Retain other repository errors rather than
			// interpreting them as an absent choice.
			if !errors.Is(err, settings.ErrSettingNotFound) {
				return "", fmt.Errorf("get %s: %w", sourceKey, err)
			}
		} else {
			rawSource = value
		}
	}

	normalizedSource, err := NormalizeVisibleMethodSettingSource(method, rawSource, true)
	if err != nil {
		return "", err
	}
	if normalizedSource == "" {
		return "", nil
	}
	providerKey, ok := VisibleMethodProviderKeyForSource(method, normalizedSource)
	if !ok {
		return "", infraerrors.BadRequest(
			"INVALID_PAYMENT_VISIBLE_METHOD_SOURCE",
			fmt.Sprintf("%s source must be one of the supported payment providers", method),
		)
	}
	return providerKey, nil
}

// ConfigResolveVisibleMethodProviderKey resolves a provider key from the
// supplied matching instance set and, when needed, the saved visible source.
// Callers select enabled method matches before invoking it.
//
// Zero distinct keys returns empty, while one key wins without reading a
// potentially conflicting setting. Multiple keys require source resolution:
// absent source remains empty for cross-provider selection, and a configured
// source must match one supplied instance or return a bad-request error. The
// returned key is taken from that first matching row and trimmed.
func (s *ConfigService) ConfigResolveVisibleMethodProviderKey(
	ctx context.Context,
	method string,
	matching []*ProviderInstance,
) (string, error) {
	switch providerKeys := ConfigDistinctVisibleMethodProviderKeys(matching); len(providerKeys) {
	case 0:
		return "", nil
	case 1:
		return strings.TrimSpace(providerKeys[0]), nil
	default:
		providerKey, err := s.ConfigResolveVisibleMethodSourceProviderKey(ctx, method)
		if err != nil {
			return "", err
		}
		if providerKey == "" {
			return "", nil
		}
		selected := ConfigSelectVisibleMethodInstanceByProviderKey(matching, providerKey)
		if selected == nil {
			return "", infraerrors.BadRequest(
				"INVALID_PAYMENT_VISIBLE_METHOD_SOURCE",
				fmt.Sprintf("%s source has no enabled provider instance", method),
			)
		}
		return strings.TrimSpace(selected.ProviderKey), nil
	}
}

// ConfigResolveEnabledVisibleMethodInstance reads enabled, sort-ordered
// instances and resolves their visible method source. Nil service/store,
// invalid method or no matches returns nil,nil; read and source-validation
// errors remain visible.
//
// A resolved key selects the first matching instance. When eligible matches
// exist but source resolution returns no key, the result is an empty-key
// ProviderInstance marker rather than an actual row. That marker tells
// VisibleMethodLoadBalancer to request cross-provider selection, while
// configuration availability can union eligible instances. Actual returned row
// pointers remain owned by the store result.
func (s *ConfigService) ConfigResolveEnabledVisibleMethodInstance(
	ctx context.Context,
	method string,
) (*ProviderInstance, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}

	method = NormalizeVisibleMethod(method)
	if method == "" {
		return nil, nil
	}

	instances, err := s.store.ListInstances(ctx, InstanceFilter{EnabledOnly: true, SortByOrder: true})
	if err != nil {
		return nil, fmt.Errorf("query enabled payment providers: %w", err)
	}

	matching := ConfigFilterEnabledVisibleMethodInstances(instances, method)
	providerKey, err := s.ConfigResolveVisibleMethodProviderKey(ctx, method, matching)
	if err != nil {
		return nil, err
	}
	if providerKey == "" {
		if len(matching) == 0 {
			return nil, nil
		}
		// Keep the distinction between no matching instance and matches with no
		// selected source. The empty-key marker lets the load balancer choose across
		// eligible providers.
		return &ProviderInstance{ProviderKey: ""}, nil
	}
	return ConfigSelectVisibleMethodInstanceByProviderKey(matching, providerKey), nil
}

// UsesOfficialAlipayVisibleMethod reports whether enabled Alipay source
// resolution returns the official provider key. It returns resolution errors
// and reports false for absent or cross-provider marker results, allowing
// checkout configuration to choose its applicable policy.
func (s *ConfigService) UsesOfficialAlipayVisibleMethod(ctx context.Context) (bool, error) {
	instance, err := s.ConfigResolveEnabledVisibleMethodInstance(ctx, TypeAlipay)
	if err != nil {
		return false, err
	}
	return ConfigIsOfficialAlipayProviderInstance(instance), nil
}

// ConfigIsOfficialAlipayProviderInstance reports a nonnil instance's trimmed
// official Alipay key, case-insensitively.
func ConfigIsOfficialAlipayProviderInstance(instance *ProviderInstance) bool {
	return instance != nil && strings.EqualFold(strings.TrimSpace(instance.ProviderKey), TypeAlipay)
}
