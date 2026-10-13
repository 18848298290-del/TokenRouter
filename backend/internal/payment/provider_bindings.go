package payment

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// An order's saved instance and merchant snapshot identify which payment
// account receives its later query, callback or refund. Provider construction
// reads that instance's current configuration, so historical identity is
// retained while credentials remain deployment data. Explicit snapshots take
// precedence over legacy columns and ambiguous unbound orders cannot select an
// arbitrary merchant.
//
// Callback lookup can use the stored merchant number to narrow candidates, but
// an initial order read failure falls through to enabled candidate/registry
// handling. Official WeChat may need several candidates before signed
// decryption identifies its account. Other registry fallback checks the
// current enabled instance count. Registry initialization and refresh share
// one mutex: complete successful loads replace its table, list failures keep
// initialization retryable, and individual unusable instances can be skipped
// from a successful replacement.

// GetWebhookProviders returns provider candidates for HTTP signature
// verification, narrowing by exact merchant order number when that read
// succeeds. A pinned order resolves its saved instance and returns
// construction errors instead of switching accounts.
//
// Unbound orders can resolve a unique legacy instance, including disabled
// historical rows. Otherwise official WeChat enumerates current enabled
// instances because verification/decryption can need account trials. Other
// providers require a nonambiguous current enabled count before registry key
// lookup. An initial exact order read error is suppressed and proceeds to that
// candidate handling; the unsigned merchant hint only chooses verification
// candidates and does not itself authenticate a callback.
func (s *ProviderBindings) GetWebhookProviders(ctx context.Context, providerKey, outTradeNo string) ([]Provider, error) {
	if outTradeNo != "" {
		// Use the unsigned merchant hint only to narrow verification candidates. A
		// failed exact order read falls through to candidate discovery, while a
		// successful pinned resolution retains its merchant errors.
		order, err := s.store.OrderByTradeNumber(ctx, outTradeNo)
		if err == nil {
			if PsHasPinnedProviderInstance(order) {
				prov, err := s.GetPinnedOrderProvider(ctx, order)
				if err != nil {
					return nil, err
				}
				return []Provider{prov}, nil
			}
			inst, err := s.GetOrderProviderInstance(ctx, order)
			if err != nil {
				return nil, fmt.Errorf("load order provider instance: %w", err)
			}
			if inst != nil {
				prov, err := s.CreateProviderFromInstance(ctx, inst)
				if err != nil {
					return nil, err
				}
				return []Provider{prov}, nil
			}
			// Official WeChat can need several account decryptors before merchant
			// identity is known. Enumerate enabled candidates when historical
			// resolution is absent, then let HTTP verify their notifications.
			if strings.TrimSpace(providerKey) == TypeWxpay {
				return s.GetEnabledWebhookProvidersByKey(ctx, providerKey)
			}
			if !s.WebhookRegistryFallbackAllowed(ctx, providerKey) {
				return nil, fmt.Errorf("webhook provider fallback is ambiguous for %s", providerKey)
			}
			s.EnsureProviders(ctx)
			prov, err := s.registry.GetProviderByKey(providerKey)
			if err != nil {
				return nil, err
			}
			return []Provider{prov}, nil
		}
	}

	// With no usable exact order observation, official WeChat still needs its
	// enabled verification candidates. Other keys proceed through the current
	// enabled-count check before registry key lookup.
	if strings.TrimSpace(providerKey) == TypeWxpay {
		return s.GetEnabledWebhookProvidersByKey(ctx, providerKey)
	}

	if !s.WebhookRegistryFallbackAllowed(ctx, providerKey) {
		return nil, fmt.Errorf("webhook provider fallback is ambiguous for %s", providerKey)
	}

	s.EnsureProviders(ctx)
	prov, err := s.registry.GetProviderByKey(providerKey)
	if err != nil {
		return nil, err
	}
	return []Provider{prov}, nil
}

// GetPinnedOrderProvider resolves the supplied nonnil order's saved instance
// and constructs its provider using current configuration. Resolution or
// missing-instance errors remain visible, so callback callers cannot replace a
// pinned merchant with a registry candidate.
func (s *ProviderBindings) GetPinnedOrderProvider(ctx context.Context, o *Order) (Provider, error) {
	inst, err := s.GetOrderProviderInstance(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("load order provider instance: %w", err)
	}
	if inst == nil {
		return nil, fmt.Errorf("order %d provider instance is missing", o.ID)
	}
	return s.CreateProviderFromInstance(ctx, inst)
}

// WebhookRegistryFallbackAllowed reports whether a trimmed nonempty key has at
// most one currently enabled instance. Nil service/store or count errors
// returns false, logging read failure; the check is separate from any
// historical disabled instance resolution or registry publication.
func (s *ProviderBindings) WebhookRegistryFallbackAllowed(ctx context.Context, providerKey string) bool {
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" || s == nil || s.store == nil {
		return false
	}

	count, err := s.store.CountEnabledInstances(ctx, providerKey)
	if err != nil {
		s.warn("payment webhook fallback instance count failed", "provider", providerKey, "error", err)
		// A count read failure cannot establish a nonambiguous merchant choice. Keep
		// fallback disabled so the callback caller receives its unresolved-account
		// error instead of selecting a registry entry.
		return false
	}
	return count <= 1
}

// PsHasPinnedProviderInstance reports whether an order has a recognized
// snapshot or nonblank instance column. A snapshot can be recognized by
// version alone, so presence here does not validate completeness of its
// merchant identity.
func PsHasPinnedProviderInstance(order *Order) bool {
	return order != nil && (PsOrderProviderSnapshot(order) != nil || (order.ProviderInstanceID != nil && strings.TrimSpace(*order.ProviderInstanceID) != ""))
}

// GetEnabledWebhookProvidersByKey constructs current enabled candidates in
// store sort order for a trimmed provider key. List errors return, while
// individual construction errors are logged and skipped; no usable candidates
// returns ErrProviderNotFound. The HTTP caller still validates each
// candidate's notification before handling its order.
func (s *ProviderBindings) GetEnabledWebhookProvidersByKey(ctx context.Context, providerKey string) ([]Provider, error) {
	providerKey = strings.TrimSpace(providerKey)
	instances, err := s.store.ListInstances(ctx, InstanceFilter{ProviderKey: providerKey, EnabledOnly: true, SortByOrder: true})
	if err != nil {
		return nil, fmt.Errorf("query webhook provider instances: %w", err)
	}
	if len(instances) == 0 {
		return nil, ErrProviderNotFound
	}

	providers := make([]Provider, 0, len(instances))
	for _, inst := range instances {
		prov, provErr := s.CreateProviderFromInstance(ctx, inst)
		// An unusable instance need not remove other verification candidates. Log
		// its construction error and continue, returning provider-not-found only if
		// no candidate survives.
		if provErr != nil {
			s.warn("skip webhook provider instance", "provider", providerKey, "instanceID", inst.ID, "error", provErr)
			continue
		}
		providers = append(providers, prov)
	}
	if len(providers) == 0 {
		return nil, ErrProviderNotFound
	}
	return providers, nil
}

// GetOrderProvider resolves a nonnil order's historical instance and
// constructs its provider with current configuration. A resolution error
// returns before fallback, and a resolved instance is used even when currently
// disabled.
//
// Registry handling is permitted only when the order lacks snapshot, instance
// column and provider key. It derives a provider key for the current
// enabled-count ambiguity check, initializes the registry if needed, then
// reads the method binding. That count and table are separate observations;
// this compatibility path does not assign a new persisted historical binding
// to the order.
func (s *ProviderBindings) GetOrderProvider(ctx context.Context, o *Order) (Provider, error) {
	inst, err := s.GetOrderProviderInstance(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("load order provider instance: %w", err)
	}
	if inst != nil {
		return s.CreateProviderFromInstance(ctx, inst)
	}
	// Historical binding failure is not permission to replace the merchant. Only
	// entirely unbound legacy orders proceed to the separate current count and
	// registry method checks.
	if !PaymentOrderAllowsRegistryFallback(o) {
		return nil, fmt.Errorf("order %d provider instance is unresolved", o.ID)
	}
	providerKey := PaymentOrderFallbackProviderKey(s.registry, o)
	if providerKey == "" {
		return nil, fmt.Errorf("order %d provider fallback key is missing", o.ID)
	}
	if !s.WebhookRegistryFallbackAllowed(ctx, providerKey) {
		return nil, fmt.Errorf("order %d provider fallback is ambiguous for %s", o.ID, providerKey)
	}
	s.EnsureProviders(ctx)
	return s.registry.GetProvider(o.PaymentType)
}

// PaymentOrderAllowsRegistryFallback reports whether a nonnil order lacks
// snapshot, nonblank instance ID and nonblank provider key. Registry
// compatibility callers use that condition before considering a method-derived
// merchant, so any saved binding excludes arbitrary fallback.
func PaymentOrderAllowsRegistryFallback(order *Order) bool {
	if order == nil {
		return false
	}
	if PsOrderProviderSnapshot(order) != nil {
		return false
	}
	if strings.TrimSpace(refundStringValue(order.ProviderInstanceID)) != "" {
		return false
	}
	if strings.TrimSpace(refundStringValue(order.ProviderKey)) != "" {
		return false
	}
	return true
}

// PaymentOrderFallbackProviderKey chooses a current registry method key before
// deriving a base type from a nonnil order's trimmed method. Nil order returns
// empty; the result feeds the enabled-count ambiguity check and is not
// persisted as historical identity.
func PaymentOrderFallbackProviderKey(registry *Registry, order *Order) string {
	if order == nil {
		return ""
	}
	if registry != nil {
		if key := strings.TrimSpace(registry.GetProviderKey(order.PaymentType)); key != "" {
			return key
		}
	}
	// Without a usable registry key, derive the method's base provider label for
	// the ambiguity count. That derived value remains an observation rather than
	// a saved order binding.
	return strings.TrimSpace(GetBasePaymentType(strings.TrimSpace(order.PaymentType)))
}

// CreateProviderFromInstance reads current configuration for the supplied
// instance, adds its nonempty paymentMode and invokes the order factory with
// decimal instance identity. Nil instance, configuration read or factory
// errors return; callers provide a load balancer returning a writable map and
// a usable factory.
func (s *ProviderBindings) CreateProviderFromInstance(ctx context.Context, inst *ProviderInstance) (Provider, error) {
	if inst == nil {
		return nil, fmt.Errorf("payment provider instance is missing")
	}

	cfg, err := s.loadBalancer.GetInstanceConfig(ctx, inst.ID)
	if err != nil {
		return nil, fmt.Errorf("load provider instance config: %w", err)
	}
	if inst.PaymentMode != "" {
		cfg["paymentMode"] = inst.PaymentMode
	}

	instID := strconv.FormatInt(inst.ID, 10)
	prov, err := s.runtime.Factory(inst.ProviderKey, instID, cfg)
	if err != nil {
		return nil, fmt.Errorf("create provider from instance: %w", err)
	}
	return prov, nil
}

// ResolveSnapshotOrderProviderInstance validates saved snapshot/column
// identity and reads that numeric instance without an enabled filter. Nil
// service/store, order or snapshot returns nil,nil; callers decide whether
// that absence permits later handling.
//
// A blank trimmed snapshot instance uses the column, but absence of both is an
// error. When both raw snapshot identity and trimmed column are present, their
// case-insensitive comparison must agree. The selected ID is parsed and its
// row/key are checked against the saved key with the implemented trimming
// rules. Missing or contradictory rows return errors. Snapshot credentials are
// not stored here; later provider construction reads the selected instance's
// current configuration.
func (s *ProviderBindings) ResolveSnapshotOrderProviderInstance(ctx context.Context, order *Order, snapshot *OrderProviderSnapshot) (*ProviderInstance, error) {
	if s == nil || s.store == nil || order == nil || snapshot == nil {
		return nil, nil
	}

	snapshotInstanceID := strings.TrimSpace(snapshot.ProviderInstanceID)
	columnInstanceID := strings.TrimSpace(refundStringValue(order.ProviderInstanceID))
	// A historical snapshot can omit its instance ID while the order column
	// retains it. Use the column before absence/parsing checks so identity
	// survives that split representation.
	if snapshotInstanceID == "" {
		snapshotInstanceID = columnInstanceID
	}
	if snapshotInstanceID == "" {
		return nil, fmt.Errorf("order %d provider snapshot is missing provider_instance_id", order.ID)
	}
	if columnInstanceID != "" && snapshot.ProviderInstanceID != "" && !strings.EqualFold(columnInstanceID, snapshot.ProviderInstanceID) {
		return nil, fmt.Errorf("order %d provider snapshot instance mismatch: snapshot=%s order=%s", order.ID, snapshot.ProviderInstanceID, columnInstanceID)
	}

	instID, err := strconv.ParseInt(snapshotInstanceID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("order %d provider snapshot instance id is invalid: %s", order.ID, snapshotInstanceID)
	}

	inst, err := s.store.Instance(ctx, instID)
	if err != nil {
		if s.store.IsNotFound(err) {
			return nil, fmt.Errorf("order %d provider snapshot instance %s is missing", order.ID, snapshotInstanceID)
		}
		return nil, err
	}

	if snapshot.ProviderKey != "" && !strings.EqualFold(strings.TrimSpace(inst.ProviderKey), snapshot.ProviderKey) {
		return nil, fmt.Errorf("order %d provider snapshot key mismatch: snapshot=%s instance=%s", order.ID, snapshot.ProviderKey, inst.ProviderKey)
	}

	return inst, nil
}

// GetOrderProviderInstance resolves an order snapshot before its instance
// column or unique legacy matching. Nil dependencies/order returns nil,nil,
// and recognized snapshots retain their resolution errors.
//
// Without a snapshot, a blank column triggers legacy matching across saved
// fields and can include disabled rows. A nonblank column is parsed as int64;
// parse failure becomes nil,nil for the caller to interpret, while storage
// errors propagate. Presence tests and GetOrderProvider fallback rules
// separately prevent an invalid saved binding from silently choosing an
// unrelated current merchant.
func (s *ProviderBindings) GetOrderProviderInstance(ctx context.Context, o *Order) (*ProviderInstance, error) {
	if s == nil || s.store == nil || o == nil {
		return nil, nil
	}

	if snapshot := PsOrderProviderSnapshot(o); snapshot != nil {
		return s.ResolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(refundStringValue(o.ProviderInstanceID))
	// A legacy order without an instance column can still identify one historical
	// account through method/provider fields. Resolve uniqueness before
	// considering any permitted registry compatibility path.
	if instIDStr == "" {
		return s.ResolveUniqueLegacyOrderProviderInstance(ctx, o)
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	// The legacy column parser exposes malformed identity as unresolved rather
	// than propagating its parse error. GetOrderProvider separately rejects saved
	// bindings before registry compatibility can be attempted.
	if err != nil {
		return nil, nil
	}
	return s.store.Instance(ctx, instID)
}

// GetRefundOrderProviderInstance requires a snapshot or saved numeric instance
// column for refund merchant resolution. Nil service/store/order or blank
// column returns nil,nil; snapshot, parse and missing-row errors remain
// visible. It reads the historical row regardless of enabled state and does
// not try legacy uniqueness or registry fallback.
func (s *ProviderBindings) GetRefundOrderProviderInstance(ctx context.Context, o *Order) (*ProviderInstance, error) {
	if s == nil || s.store == nil || o == nil {
		return nil, nil
	}

	if snapshot := PsOrderProviderSnapshot(o); snapshot != nil {
		return s.ResolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(refundStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return nil, nil
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("order %d refund provider instance id is invalid: %s", o.ID, instIDStr)
	}
	inst, err := s.store.Instance(ctx, instID)
	if err != nil {
		if s.store.IsNotFound(err) {
			return nil, fmt.Errorf("order %d refund provider instance %s is missing", o.ID, instIDStr)
		}
		return nil, err
	}
	return inst, nil
}

// ResolveUniqueLegacyOrderProviderInstance returns one uniquely matching
// historical instance for a nonnil unbound order, or nil when none/multiple
// match. A saved provider key narrows the store read; otherwise a nonempty
// base method considers all instances.
//
// These reads include disabled rows because legacy orders may need the
// merchant that handled them. Method matching excludes Stripe from non-Stripe
// requests, then uses direct provider identity or normalized supported types.
// No chosen instance is persisted by this read, and ambiguous results stay
// unresolved for GetOrderProvider's stricter saved-key/fallback decisions.
func (s *ProviderBindings) ResolveUniqueLegacyOrderProviderInstance(ctx context.Context, o *Order) (*ProviderInstance, error) {
	paymentType := GetBasePaymentType(strings.TrimSpace(o.PaymentType))
	providerKey := strings.TrimSpace(refundStringValue(o.ProviderKey))
	if providerKey != "" {
		instances, err := s.store.ListInstances(ctx, InstanceFilter{ProviderKey: providerKey})
		if err != nil {
			return nil, err
		}
		matched := PsFilterLegacyOrderProviderInstances(paymentType, instances)
		if len(matched) == 1 {
			return matched[0], nil
		}
		return nil, nil
	}

	if paymentType == "" {
		return nil, nil
	}

	instances, err := s.store.ListInstances(ctx, InstanceFilter{})
	if err != nil {
		return nil, err
	}

	matched := PsFilterLegacyOrderProviderInstances(paymentType, instances)
	if len(matched) == 1 {
		return matched[0], nil
	}
	return nil, nil
}

// PsFilterLegacyOrderProviderInstances returns historical method matches in
// input order with shared row pointers. Empty input returns nil, while blank
// method returns the original slice; enabled state is not filtered by this
// compatibility helper.
func PsFilterLegacyOrderProviderInstances(orderPaymentType string, instances []*ProviderInstance) []*ProviderInstance {
	if len(instances) == 0 {
		return nil
	}
	if strings.TrimSpace(orderPaymentType) == "" {
		return instances
	}
	var matched []*ProviderInstance
	for _, inst := range instances {
		if PsLegacyOrderMatchesInstance(orderPaymentType, inst) {
			matched = append(matched, inst)
		}
	}
	return matched
}

// PsLegacyOrderMatchesInstance reports whether a nonnil historical instance
// matches the normalized order method. Stripe matches only Stripe provider
// identity and is excluded from other methods; direct key equality or
// InstanceSupportsType then accepts other provider/method combinations
// regardless of enabled state.
func PsLegacyOrderMatchesInstance(orderPaymentType string, inst *ProviderInstance) bool {
	if inst == nil {
		return false
	}

	baseType := GetBasePaymentType(strings.TrimSpace(orderPaymentType))
	instanceProviderKey := strings.TrimSpace(inst.ProviderKey)
	if baseType == "" {
		return false
	}

	if baseType == TypeStripe {
		return instanceProviderKey == TypeStripe
	}
	if instanceProviderKey == TypeStripe {
		return false
	}
	if instanceProviderKey == baseType {
		return true
	}
	return InstanceSupportsType(inst.SupportedTypes, baseType)
}

// GetRefundProvider constructs the supplied nonnil order's saved refund
// merchant using its current configuration. An absent or invalid historical
// binding returns an error; this path delegates refund resolution and does not
// select a registered replacement merchant.
func (s *ProviderBindings) GetRefundProvider(ctx context.Context, o *Order) (Provider, error) {
	inst, err := s.GetRefundOrderProviderInstance(ctx, o)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, fmt.Errorf("refund provider instance is unavailable for order %d", o.ID)
	}
	return s.CreateProviderFromInstance(ctx, inst)
}

// EnsureProviders initializes the registry once per successful loaded flag
// while holding providerMu through loading and publication. Individual invalid
// instances can be skipped by an otherwise successful load.
//
// An instance-list error returns with initialization still retryable and the
// prior table retained. A successful load replaces the complete registry
// before marking loaded, including an empty usable list. The mutex coordinates
// this initializer with checked refresh; registry lookups use the registry's
// own lock and returned provider objects remain shared references.
func (s *ProviderBindings) EnsureProviders(ctx context.Context) {
	// Hold the initialization mutex through load and whole-table publication. The
	// loaded flag changes only after replacement, keeping concurrent lazy calls
	// aligned with checked refresh.
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if s.providersLoaded {
		return
	}
	providers, err := s.LoadProviders(ctx)
	// A list failure leaves the prior registry and unloaded flag intact. Return
	// so a later caller can retry initialization instead of publishing an
	// incomplete candidate observation.
	if err != nil {
		return
	}
	s.registry.Replace(providers)
	s.providersLoaded = true
}

// RefreshProviders invokes checked registry refresh and discards its returned
// load error for compatibility callers. A failed list read retains the
// previous table and makes future initialization retryable; settings callers
// can instead use RefreshProvidersChecked.
func (s *ProviderBindings) RefreshProviders(ctx context.Context) {
	// Compatibility refresh callers discard the checked load error. The checked
	// method still retains the prior table on failure and clears readiness so
	// another initialization can retry.
	_ = s.RefreshProvidersChecked(ctx)
}

// RefreshProvidersChecked loads and publishes a complete current provider
// table under providerMu, returning list errors to settings callers.
// Individual unusable instances are skipped by LoadProviders.
//
// A list failure clears providersLoaded and retains the registry table,
// allowing a later EnsureProviders attempt. Success replaces the table before
// marking loaded, even when no usable provider remains. Publication
// coordinates with lazy initialization, while existing provider references can
// survive replacement and retain their own configuration/state.
func (s *ProviderBindings) RefreshProvidersChecked(ctx context.Context) error {
	// Serialize refresh loading and publication with lazy initialization. A
	// failed list read clears readiness but keeps the previous registry table
	// available for current readers and later retry.
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	providers, err := s.LoadProviders(ctx)
	if err != nil {
		s.providersLoaded = false
		return err
	}
	s.registry.Replace(providers)
	s.providersLoaded = true
	return nil
}

// LoadProviders constructs registry candidates from current enabled instance
// rows, using current configuration and decimal instance IDs. It needs usable
// store, load balancer and registry factory dependencies.
//
// An initial list failure returns an error, allowing callers to retain the
// previous registry. Configuration read and factory failures for individual
// instances are logged and skipped while usable candidates continue. Nonempty
// paymentMode overrides that config field before construction. The result has
// not been published; EnsureProviders or RefreshProvidersChecked owns
// whole-table replacement and loaded-state coordination.
func (s *ProviderBindings) LoadProviders(ctx context.Context) ([]Provider, error) {
	instances, err := s.store.ListInstances(ctx, InstanceFilter{EnabledOnly: true})
	if err != nil {
		s.warn("[PaymentService] failed to query provider instances", "error", err)
		return nil, err
	}
	providers := make([]Provider, 0, len(instances))
	for _, inst := range instances {
		cfg, err := s.loadBalancer.GetInstanceConfig(ctx, inst.ID)
		// A current configuration read failure excludes only this registry
		// candidate. Keep other enabled instances available for the later
		// complete-table publication and record its diagnostic identity.
		if err != nil {
			s.warn("[PaymentService] failed to decrypt config for instance", "instanceID", inst.ID, "error", err)
			continue
		}
		if inst.PaymentMode != "" {
			cfg["paymentMode"] = inst.PaymentMode
		}
		instID := fmt.Sprintf("%d", inst.ID)
		p, err := s.runtime.RegistryFactory(inst.ProviderKey, instID, cfg)
		// A factory failure leaves this instance out of the replacement table while
		// other providers continue loading. The caller treats the completed
		// candidate list as a successful publication input.
		if err != nil {
			s.warn("[PaymentService] failed to create provider for instance", "instanceID", inst.ID, "key", inst.ProviderKey, "error", err)
			continue
		}
		providers = append(providers, p)
	}
	return providers, nil
}
