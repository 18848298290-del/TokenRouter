package payment

import (
	"context"
	"fmt"
)

// visible_selection.go resolves visible Alipay and WeChat sources before
// delegating payment instance selection.
// A visible Alipay or WeChat method can be supplied by official or EasyPay
// instances. This wrapper resolves that source before the underlying selector
// chooses an instance, retaining cross-provider behavior when configuration
// returns its empty-key marker.
//
// Explicit provider keys and unrelated methods delegate directly, while
// missing enabled instances or invalid source settings return before
// delegation. The inner selector still owns supported methods, usage
// observations, strategy, and limit handling. Constructor guards can return
// the supplied selector unchanged when the configuration store is absent, so
// direct selection remains available to callers with that setup. Configuration
// reads also delegate to the same inner selector.

// visibleMethodLoadBalancer resolves visible Alipay/WeChat source before
// delegating instance selection.
type visibleMethodLoadBalancer struct {
	inner         LoadBalancer   // underlying instance selector
	configService *ConfigService // visible source configuration reader
}

// NewVisibleMethodLoadBalancer wraps a usable selector and instance-backed
// configuration reader for visible routing. A nil selector, configuration
// service, or configuration store returns inner as supplied, including a nil
// interface.
func NewVisibleMethodLoadBalancer(inner LoadBalancer, configService *ConfigService) LoadBalancer {
	if inner == nil || configService == nil || configService.store == nil {
		return inner
	}
	return &visibleMethodLoadBalancer{inner: inner, configService: configService}
}

// GetInstanceConfig delegates configuration reading to the wrapped instance
// selector unchanged.
func (lb *visibleMethodLoadBalancer) GetInstanceConfig(ctx context.Context, instanceID int64) (map[string]string, error) {
	return lb.inner.GetInstanceConfig(ctx, instanceID)
}

// SelectInstance resolves configured source for Alipay or WeChat when the
// caller supplied no provider key. Explicit provider requests and other
// methods delegate their original arguments directly.
//
// Visible resolution can report no enabled instance or an invalid source. A
// matching empty-key marker requests the inner selector's cross-provider
// behavior, while a resolved adapter key narrows selection to that provider.
// The delegated method, strategy, and amount remain the caller's values; this
// wrapper selects the source, and the inner selector applies its candidate and
// limit rules.
func (lb *visibleMethodLoadBalancer) SelectInstance(ctx context.Context, providerKey string, paymentType PaymentType, strategy Strategy, orderAmount float64) (*InstanceSelection, error) {
	visibleMethod := NormalizeVisibleMethod(paymentType)
	if providerKey != "" || (visibleMethod != TypeAlipay && visibleMethod != TypeWxpay) {
		return lb.inner.SelectInstance(ctx, providerKey, paymentType, strategy, orderAmount)
	}

	// Resolve visible source before delegation so a configured official or
	// EasyPay choice narrows provider candidates. An empty-key marker
	// deliberately retains cross-provider selection for absent source
	// configuration.
	inst, err := lb.configService.ConfigResolveEnabledVisibleMethodInstance(ctx, visibleMethod)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, fmt.Errorf("visible payment method %s has no enabled provider instance", visibleMethod)
	}
	return lb.inner.SelectInstance(ctx, inst.ProviderKey, paymentType, strategy, orderAmount)
}
