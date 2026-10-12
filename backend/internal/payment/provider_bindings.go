package payment

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// GetWebhookProviders returns provider candidates that can verify the webhook.
// Official WeChat Pay may require multiple candidates because the callback body
// cannot be bound to a merchant before decryption.
func (s *ProviderBindings) GetWebhookProviders(ctx context.Context, providerKey, outTradeNo string) ([]Provider, error) {
	if outTradeNo != "" {
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

func (s *ProviderBindings) WebhookRegistryFallbackAllowed(ctx context.Context, providerKey string) bool {
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" || s == nil || s.store == nil {
		return false
	}

	count, err := s.store.CountEnabledInstances(ctx, providerKey)
	if err != nil {
		s.warn("payment webhook fallback instance count failed", "provider", providerKey, "error", err)
		return false
	}
	return count <= 1
}

func PsHasPinnedProviderInstance(order *Order) bool {
	return order != nil && (PsOrderProviderSnapshot(order) != nil || (order.ProviderInstanceID != nil && strings.TrimSpace(*order.ProviderInstanceID) != ""))
}

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

// GetOrderProvider constructs a provider from the order's bound instance and its current configuration.
// Unbound legacy orders may use the registry when the provider is unambiguous.
func (s *ProviderBindings) GetOrderProvider(ctx context.Context, o *Order) (Provider, error) {
	inst, err := s.GetOrderProviderInstance(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("load order provider instance: %w", err)
	}
	if inst != nil {
		return s.CreateProviderFromInstance(ctx, inst)
	}
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

func PaymentOrderFallbackProviderKey(registry *Registry, order *Order) string {
	if order == nil {
		return ""
	}
	if registry != nil {
		if key := strings.TrimSpace(registry.GetProviderKey(order.PaymentType)); key != "" {
			return key
		}
	}
	return strings.TrimSpace(GetBasePaymentType(strings.TrimSpace(order.PaymentType)))
}

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

func (s *ProviderBindings) ResolveSnapshotOrderProviderInstance(ctx context.Context, order *Order, snapshot *OrderProviderSnapshot) (*ProviderInstance, error) {
	if s == nil || s.store == nil || order == nil || snapshot == nil {
		return nil, nil
	}

	snapshotInstanceID := strings.TrimSpace(snapshot.ProviderInstanceID)
	columnInstanceID := strings.TrimSpace(refundStringValue(order.ProviderInstanceID))
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

// GetOrderProviderInstance looks up the provider instance that processed this order.
// For legacy orders without provider_instance_id, it resolves only when the
// historical instance is uniquely identifiable from the stored order fields.
func (s *ProviderBindings) GetOrderProviderInstance(ctx context.Context, o *Order) (*ProviderInstance, error) {
	if s == nil || s.store == nil || o == nil {
		return nil, nil
	}

	if snapshot := PsOrderProviderSnapshot(o); snapshot != nil {
		return s.ResolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(refundStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return s.ResolveUniqueLegacyOrderProviderInstance(ctx, o)
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, nil
	}
	return s.store.Instance(ctx, instID)
}

// GetRefundOrderProviderInstance resolves the provider instance for refund paths.
// Refunds require a saved historical instance binding.
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

// GetRefundProvider constructs a provider from the order's saved instance binding.
// An unresolved binding is an error; refunds cannot select an arbitrary registered merchant.
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

// EnsureProviders lazily initializes the provider registry on first call.
// An instance-list failure leaves initialization retryable; individual invalid providers are skipped.
func (s *ProviderBindings) EnsureProviders(ctx context.Context) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if s.providersLoaded {
		return
	}
	providers, err := s.LoadProviders(ctx)
	if err != nil {
		return
	}
	s.registry.Replace(providers)
	s.providersLoaded = true
}

// RefreshProviders replaces the registry after loading candidates and retains it when the instance query fails.
func (s *ProviderBindings) RefreshProviders(ctx context.Context) {
	_ = s.RefreshProvidersChecked(ctx)
}

// RefreshProvidersChecked refreshes the registry and returns load errors to settings callers.
func (s *ProviderBindings) RefreshProvidersChecked(ctx context.Context) error {
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

func (s *ProviderBindings) LoadProviders(ctx context.Context) ([]Provider, error) {
	instances, err := s.store.ListInstances(ctx, InstanceFilter{EnabledOnly: true})
	if err != nil {
		s.warn("[PaymentService] failed to query provider instances", "error", err)
		return nil, err
	}
	providers := make([]Provider, 0, len(instances))
	for _, inst := range instances {
		cfg, err := s.loadBalancer.GetInstanceConfig(ctx, inst.ID)
		if err != nil {
			s.warn("[PaymentService] failed to decrypt config for instance", "instanceID", inst.ID, "error", err)
			continue
		}
		if inst.PaymentMode != "" {
			cfg["paymentMode"] = inst.PaymentMode
		}
		instID := fmt.Sprintf("%d", inst.ID)
		p, err := s.runtime.RegistryFactory(inst.ProviderKey, instID, cfg)
		if err != nil {
			s.warn("[PaymentService] failed to create provider for instance", "instanceID", inst.ID, "key", inst.ProviderKey, "error", err)
			continue
		}
		providers = append(providers, p)
	}
	return providers, nil
}
