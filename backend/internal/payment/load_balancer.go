package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const (
	StrategyRoundRobin  Strategy = "round-robin"
	StrategyLeastAmount Strategy = "least-amount"

	wxpayJSAPIAppIDContextKey contextKey = "payment.wxpay.jsapi_app_id"
)

// Strategy represents a load balancing strategy for provider instance selection.
type Strategy string

// ChannelLimits holds limits for a single payment channel within a provider instance.
type ChannelLimits struct {
	DailyLimit float64 `json:"dailyLimit,omitempty"`
	SingleMin  float64 `json:"singleMin,omitempty"`
	SingleMax  float64 `json:"singleMax,omitempty"`
}

// InstanceLimits holds per-channel limits for a provider instance (JSON).
type InstanceLimits map[string]ChannelLimits

// LoadBalancer selects a provider instance for a given payment type.
type LoadBalancer interface {
	GetInstanceConfig(ctx context.Context, instanceID int64) (map[string]string, error)
	SelectInstance(ctx context.Context, providerKey string, paymentType PaymentType, strategy Strategy, orderAmount float64) (*InstanceSelection, error)
}

// DefaultLoadBalancer implements LoadBalancer using database queries.
type DefaultLoadBalancer struct {
	source        InstanceSource
	encryptionKey []byte
	counter       atomic.Uint64
	runtime       SelectionRuntime
}

type contextKey string

// instanceCandidate pairs an instance with its pre-fetched daily usage.
type instanceCandidate struct {
	inst      *ProviderInstance
	dailyUsed float64 // Includes pending and provider-processing orders.
}

// InstanceSource queries payment instances and their daily usage.
type InstanceSource interface {
	EnabledInstances(context.Context, string) ([]*ProviderInstance, error)
	Instance(context.Context, int64) (*ProviderInstance, error)
	DailyUsage(context.Context, []string, time.Time) (map[string]float64, error)
	PaidDailyAmount(context.Context, string, time.Time) (float64, error)
}

type (
	// SelectionObserver receives provider-selection log events.
	SelectionObserver func(string, string, ...any)
	// SelectionRuntime supplies the clock and logging callback for provider selection.
	SelectionRuntime struct {
		Now     func() time.Time
		Observe SelectionObserver
	}
)

// NewDefaultLoadBalancer creates a new load balancer.
func NewDefaultLoadBalancer(source InstanceSource, encryptionKey []byte, runtime ...SelectionRuntime) *DefaultLoadBalancer {
	options := SelectionRuntime{Now: time.Now}
	if len(runtime) > 0 {
		options = runtime[0]
		if options.Now == nil {
			options.Now = time.Now
		}
	}
	return &DefaultLoadBalancer{source: source, encryptionKey: encryptionKey, runtime: options}
}

func WithWxpayJSAPIAppID(ctx context.Context, appID string) context.Context {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return ctx
	}
	return context.WithValue(ctx, wxpayJSAPIAppIDContextKey, appID)
}

func wxpayJSAPIAppIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	appID, _ := ctx.Value(wxpayJSAPIAppIDContextKey).(string)
	return strings.TrimSpace(appID)
}

// SelectInstance picks an enabled instance for the given provider key and payment type.
//
// Flow:
//  1. Query all enabled instances for providerKey, filter by supported paymentType
//  2. Batch-read daily usage for PENDING, PROCESSING, PAID, COMPLETED, and RECHARGING orders.
//  3. Filter out instances where: single-min/max violated OR daily remaining < orderAmount
//  4. If every instance is filtered out, use the full candidate list.
//  5. Select by round-robin or least daily amount.
func (lb *DefaultLoadBalancer) SelectInstance(
	ctx context.Context,
	providerKey string,
	paymentType PaymentType,
	strategy Strategy,
	orderAmount float64,
) (*InstanceSelection, error) {
	instances, err := lb.queryEnabledInstances(ctx, providerKey, paymentType)
	if err != nil {
		return nil, err
	}

	candidates := lb.attachDailyUsage(ctx, instances)

	available := filterByLimits(candidates, paymentType, orderAmount, lb.runtime.Observe)
	if len(available) == 0 {
		lb.observe("warn", "all instances exceeded limits, using full candidate list",
			"provider", providerKey, "payment_type", paymentType,
			"order_amount", orderAmount, "count", len(candidates))
		available = candidates
	}

	selected := lb.pickByStrategy(available, strategy)
	return lb.buildSelection(selected.inst), nil
}

// queryEnabledInstances returns enabled instances that support paymentType.
// When providerKey is non-empty, only instances with that provider key are considered.
// When providerKey is empty, instances across all providers are considered,
// enabling cross-provider load balancing (e.g. EasyPay + Alipay direct for "alipay").
func (lb *DefaultLoadBalancer) queryEnabledInstances(
	ctx context.Context,
	providerKey string,
	paymentType PaymentType,
) ([]*ProviderInstance, error) {
	instances, err := lb.source.EnabledInstances(ctx, providerKey)
	if err != nil {
		return nil, fmt.Errorf("query provider instances: %w", err)
	}

	var matched []*ProviderInstance
	expectedWxpayJSAPIAppID := wxpayJSAPIAppIDFromContext(ctx)
	for _, inst := range instances {
		// Match Stripe by provider key to support both legacy subtypes and the "stripe" method.
		if paymentType == TypeStripe {
			if inst.ProviderKey == TypeStripe {
				matched = append(matched, inst)
			}
		} else if InstanceSupportsType(inst.SupportedTypes, paymentType) {
			if expectedWxpayJSAPIAppID != "" && normalizeVisibleMethodSupportType(paymentType) == TypeWxpay && inst.ProviderKey == TypeWxpay {
				config := lb.decryptConfig(inst.Config)
				if resolveWxpayJSAPIAppID(config) != expectedWxpayJSAPIAppID {
					continue
				}
			}
			matched = append(matched, inst)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("no enabled instance for payment type %s", paymentType)
	}
	return matched, nil
}

// attachDailyUsage queries daily usage for each instance in a single pass.
// Usage includes pending and provider-processing orders.
func (lb *DefaultLoadBalancer) attachDailyUsage(
	ctx context.Context,
	instances []*ProviderInstance,
) []instanceCandidate {
	todayStart := startOfDay(lb.runtime.Now())

	ids := make([]string, len(instances))
	for i, inst := range instances {
		ids[i] = fmt.Sprintf("%d", inst.ID)
	}

	usageMap, err := lb.source.DailyUsage(ctx, ids, todayStart)
	if err != nil {
		lb.observe("warn", "batch daily usage query failed, treating all as zero", "error", err)
	}

	candidates := make([]instanceCandidate, len(instances))
	for i, inst := range instances {
		candidates[i] = instanceCandidate{
			inst:      inst,
			dailyUsed: usageMap[fmt.Sprintf("%d", inst.ID)],
		}
	}
	return candidates
}

// filterByLimits removes instances that cannot accommodate the order:
//   - orderAmount outside single-transaction [min, max]
//   - daily remaining capacity (limit - used) < orderAmount
func filterByLimits(candidates []instanceCandidate, paymentType PaymentType, orderAmount float64, observers ...SelectionObserver) []instanceCandidate {
	observe := selectionObserver(observers)
	var result []instanceCandidate
	for _, c := range candidates {
		cl := getInstanceChannelLimits(c.inst, paymentType)

		if cl.SingleMin > 0 && orderAmount < cl.SingleMin {
			observe("info", "order below instance single min, skipping",
				"instance_id", c.inst.ID, "order", orderAmount, "min", cl.SingleMin)
			continue
		}
		if cl.SingleMax > 0 && orderAmount > cl.SingleMax {
			observe("info", "order above instance single max, skipping",
				"instance_id", c.inst.ID, "order", orderAmount, "max", cl.SingleMax)
			continue
		}
		if cl.DailyLimit > 0 && c.dailyUsed+orderAmount > cl.DailyLimit {
			observe("info", "instance daily remaining insufficient, skipping",
				"instance_id", c.inst.ID, "used", c.dailyUsed,
				"order", orderAmount, "limit", cl.DailyLimit)
			continue
		}

		result = append(result, c)
	}
	return result
}

// getInstanceChannelLimits returns the channel limits for a specific payment type.
func getInstanceChannelLimits(inst *ProviderInstance, paymentType PaymentType) ChannelLimits {
	if inst.Limits == "" {
		return ChannelLimits{}
	}
	var limits InstanceLimits
	if err := json.Unmarshal([]byte(inst.Limits), &limits); err != nil {
		return ChannelLimits{}
	}
	// For Stripe, limits are stored under the provider key "stripe".
	lookupKey := paymentType
	if inst.ProviderKey == "stripe" {
		lookupKey = "stripe"
	}
	if cl, ok := limits[lookupKey]; ok {
		return cl
	}
	if aliasKey := legacyVisibleMethodAlias(lookupKey); aliasKey != "" {
		if cl, ok := limits[aliasKey]; ok {
			return cl
		}
	}
	return ChannelLimits{}
}

// pickByStrategy selects one instance from the available candidates.
func (lb *DefaultLoadBalancer) pickByStrategy(candidates []instanceCandidate, strategy Strategy) instanceCandidate {
	if strategy == StrategyLeastAmount && len(candidates) > 1 {
		return pickLeastAmount(candidates)
	}
	// Default: round-robin.
	idx := lb.counter.Add(1) % uint64(len(candidates))
	return candidates[idx]
}

// pickLeastAmount selects the instance with the lowest daily usage.
// attachDailyUsage supplies the daily totals before selection.
func pickLeastAmount(candidates []instanceCandidate) instanceCandidate {
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.dailyUsed < best.dailyUsed {
			best = c
		}
	}
	return best
}

// buildSelection combines instance configuration and payment mode into a selection result.
func (lb *DefaultLoadBalancer) buildSelection(selected *ProviderInstance) *InstanceSelection {
	config := lb.decryptConfig(selected.Config)
	if config == nil {
		config = map[string]string{}
	}

	if selected.PaymentMode != "" {
		config["paymentMode"] = selected.PaymentMode
	}

	return &InstanceSelection{
		InstanceID:     fmt.Sprintf("%d", selected.ID),
		ProviderKey:    selected.ProviderKey,
		Config:         config,
		SupportedTypes: selected.SupportedTypes,
		PaymentMode:    selected.PaymentMode,
	}
}

// decryptConfig reads instance configuration, logging parse failures and returning nil.
func (lb *DefaultLoadBalancer) decryptConfig(stored string) map[string]string {
	config, ok := parseProviderConfig(stored, lb.encryptionKey)
	if !ok {
		lb.observe("warn", "payment provider config unreadable, treating as empty for re-entry",
			"stored_len", len(stored))
	}
	return config
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// InstanceSupportsType checks if the given supported types string includes the target type.
// An empty supportedTypes string means all types are supported.
func InstanceSupportsType(supportedTypes string, target PaymentType) bool {
	if supportedTypes == "" {
		return true
	}
	normalizedTarget := normalizeVisibleMethodSupportType(target)
	for t := range strings.SplitSeq(supportedTypes, ",") {
		supported := strings.TrimSpace(t)
		if supported == target || normalizeVisibleMethodSupportType(supported) == normalizedTarget {
			return true
		}
	}
	return false
}

func normalizeVisibleMethodSupportType(paymentType PaymentType) PaymentType {
	switch strings.TrimSpace(paymentType) {
	case TypeAlipay, TypeAlipayDirect:
		return TypeAlipay
	case TypeWxpay, TypeWxpayDirect:
		return TypeWxpay
	default:
		return strings.TrimSpace(paymentType)
	}
}

func legacyVisibleMethodAlias(paymentType PaymentType) PaymentType {
	switch normalizeVisibleMethodSupportType(paymentType) {
	case TypeAlipay:
		return TypeAlipayDirect
	case TypeWxpay:
		return TypeWxpayDirect
	default:
		return ""
	}
}

func resolveWxpayJSAPIAppID(config map[string]string) string {
	if appID := strings.TrimSpace(config["mpAppId"]); appID != "" {
		return appID
	}
	return strings.TrimSpace(config["appId"])
}

// GetInstanceConfig returns instance configuration or a writable empty map when parsing fails.
func (lb *DefaultLoadBalancer) GetInstanceConfig(ctx context.Context, instanceID int64) (map[string]string, error) {
	inst, err := lb.source.Instance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("get instance %d: %w", instanceID, err)
	}
	config := lb.decryptConfig(inst.Config)
	if config == nil {
		config = map[string]string{}
	}
	return config, nil
}

// observe sends the level, message, and fields to the injected observer.
func (lb *DefaultLoadBalancer) observe(level, message string, attrs ...any) {
	if lb.runtime.Observe != nil {
		lb.runtime.Observe(level, message, attrs...)
	}
}

func selectionObserver(values []SelectionObserver) SelectionObserver {
	if len(values) > 0 && values[0] != nil {
		return values[0]
	}
	return func(string, string, ...any) {}
}
