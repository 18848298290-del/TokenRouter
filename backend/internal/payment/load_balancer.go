package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Instance channel limits compare the requested amount with single-order
// bounds and creation-day usage that includes pending/provider-processing
// orders. If every eligible instance exceeds those limits, selection
// deliberately uses the complete eligible list, so these checks guide
// distribution rather than reserve a hard payment ceiling.
//
// Candidates retain store order while the chosen strategy compares their
// observed usage or advances one shared counter. Missing usage entries
// contribute zero, and a logged batch-read error does not discard any returned
// partial totals. Stripe eligibility uses its provider key; official WeChat
// JSAPI also matches the callback app identity from context. Selected
// configuration is decoded independently of eligibility and gains the instance
// paymentMode, while historical ciphertext parsing follows the deployment key
// rules.

// StrategyRoundRobin and StrategyLeastAmount select candidate distribution,
// while wxpayJSAPIAppIDContextKey carries the expected official callback app
// identity through checkout selection.
const (
	// shared counter rotation strategy.
	StrategyRoundRobin Strategy = "round-robin"
	// minimum observed usage strategy.
	StrategyLeastAmount Strategy = "least-amount"

	// wxpayJSAPIAppIDContextKey names the official JSAPI app constraint read
	// during checkout instance selection.
	wxpayJSAPIAppIDContextKey contextKey = "payment.wxpay.jsapi_app_id"
)

// Strategy is the configured text selecting payment instance distribution
// behavior.
type Strategy string

// ChannelLimits contains numeric channel bounds compared with the requested
// amount and observed daily usage.
type ChannelLimits struct {
	// DailyLimit is a positive channel/day ceiling used by selection; nonpositive
	// values omit that comparison. Daily usage and requested amount are compared
	// as supplied numeric quantities.
	DailyLimit float64 `json:"dailyLimit,omitempty"`
	// SingleMin is the positive inclusive lower order bound. Nonpositive values
	// omit its check before DefaultLoadBalancer's all-filtered recovery choice.
	SingleMin float64 `json:"singleMin,omitempty"`
	// SingleMax is the positive inclusive upper order bound. Nonpositive values
	// omit its check before DefaultLoadBalancer's all-filtered recovery choice.
	SingleMax float64 `json:"singleMax,omitempty"`
}

// InstanceLimits maps persisted channel labels to their numeric payment
// selection bounds.
type InstanceLimits map[string]ChannelLimits

// LoadBalancer supplies instance configuration and provider/method-specific
// checkout selection to payment callers.
type LoadBalancer interface {
	// GetInstanceConfig reads an instance's current configuration and returns a
	// writable map for provider construction. DefaultLoadBalancer returns an
	// empty map for unreadable/empty configuration, while instance lookup
	// failures remain errors.
	GetInstanceConfig(ctx context.Context, instanceID int64) (map[string]string, error)
	// SelectInstance returns a selected eligible provider instance for the method
	// and supplied amount. Empty provider key permits cross-provider selection;
	// DefaultLoadBalancer treats channel limits as advisory when they exclude
	// every eligible candidate.
	SelectInstance(ctx context.Context, providerKey string, paymentType PaymentType, strategy Strategy, orderAmount float64) (*InstanceSelection, error)
}

// DefaultLoadBalancer observes instance eligibility and daily usage before
// applying channel limits and strategy.
type DefaultLoadBalancer struct {
	source InstanceSource // instance and usage reads
	// encryptionKey is retained by reference for historical config decoding.
	// Callers keep those bytes unchanged while this selector and its
	// configuration reads remain in use.
	encryptionKey []byte
	// counter is shared by all round-robin selections on this balancer. Its
	// increment occurs before modulo, so initial multi-candidate selection starts
	// at index one.
	counter atomic.Uint64
	runtime SelectionRuntime // selection clock and observations
}

// contextKey is the private typed key for payment context metadata.
type contextKey string

// instanceCandidate pairs an eligible shared instance with its observed daily
// usage.
type instanceCandidate struct {
	// inst retains the eligible store result by reference. Strategy selection
	// returns that instance's identity/configuration rather than copying the
	// persisted row.
	inst      *ProviderInstance
	dailyUsed float64 // observed creation-day PayAmount total
}

// InstanceSource supplies persisted candidates and the two distinct
// payment-day usage observations.
type InstanceSource interface {
	// EnabledInstances returns enabled rows for an optional provider key in
	// storage order. Empty key permits all providers, and DefaultLoadBalancer
	// applies method/JSAPI app eligibility to the returned set.
	EnabledInstances(context.Context, string) ([]*ProviderInstance, error)
	// Instance reads a payment provider row by local numeric identity.
	Instance(context.Context, int64) (*ProviderInstance, error)
	// DailyUsage groups PayAmount for supplied text instance IDs from the
	// creation-time lower bound, including pending, processing, paid, recharging
	// and completed states in the PostgreSQL adapter. Callers can retain partial
	// returned totals even when the read reports an error.
	DailyUsage(context.Context, []string, time.Time) (map[string]float64, error)
	// PaidDailyAmount sums PayAmount confirmed from the supplied paid-time lower
	// bound for one text instance ID. The PostgreSQL adapter includes paid,
	// recharging and completed states; callers handle its error separately from
	// candidate usage.
	PaidDailyAmount(context.Context, string, time.Time) (float64, error)
}

type (
	// SelectionObserver receives payment instance selection diagnostics and their
	// structured attributes.
	SelectionObserver func(string, string, ...any)
	// SelectionRuntime supplies the clock and optional diagnostics used while
	// observing candidates.
	SelectionRuntime struct {
		// Now supplies the day calculation clock. NewDefaultLoadBalancer defaults a
		// missing callback, and startOfDay retains the returned time's location for
		// its lower bound.
		Now func() time.Time
		// Observe optionally receives candidate decisions and read/decode failures.
		// A missing observer suppresses diagnostics while leaving the selection
		// result governed by the same observations.
		Observe SelectionObserver
	}
)

// NewDefaultLoadBalancer binds instance reads, historical key bytes and the
// first optional runtime value. Missing clock defaults to time.Now;
// dependencies and key storage are retained, so callers own their usable
// lifetime and stable bytes.
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

// WithWxpayJSAPIAppID attaches a trimmed nonempty official JSAPI app identity
// to ctx for subsequent instance eligibility. Blank text returns the original
// context, allowing ordinary checkout selection without that additional app
// constraint.
func WithWxpayJSAPIAppID(ctx context.Context, appID string) context.Context {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return ctx
	}
	return context.WithValue(ctx, wxpayJSAPIAppIDContextKey, appID)
}

// wxpayJSAPIAppIDFromContext reads the trimmed string constraint stored by
// WithWxpayJSAPIAppID. Nil context, an absent key or a different value type
// returns empty text, leaving official app filtering inactive.
func wxpayJSAPIAppIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	appID, _ := ctx.Value(wxpayJSAPIAppIDContextKey).(string)
	return strings.TrimSpace(appID)
}

// SelectInstance chooses an enabled method-compatible instance from the
// requested provider key or all keys when empty. Eligibility errors return
// before any strategy result is built.
//
// Creation-day usage is attached, then positive single/daily limits filter
// candidates against the supplied numeric amount. If all candidates are
// excluded, the eligible list is reused, so this operation does not enforce a
// hard reservation. Least-amount uses observed totals and store-order ties;
// other strategy text uses the shared round-robin counter. Configuration is
// decoded after a candidate wins from the returned row's config text.
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
	// Every eligible instance exceeded observed channel limits. Reuse those
	// candidates to permit distribution despite the advisory bounds, retaining
	// the selected provider's later validation and persistence errors.
	if len(available) == 0 {
		lb.observe("warn", "all instances exceeded limits, using full candidate list",
			"provider", providerKey, "payment_type", paymentType,
			"order_amount", orderAmount, "count", len(candidates))
		available = candidates
	}

	selected := lb.pickByStrategy(available, strategy)
	return lb.buildSelection(selected.inst), nil
}

// queryEnabledInstances reads enabled rows and retains method-compatible
// candidates in returned order. Empty provider key permits cross-provider
// rows, and an empty result becomes a selection error.
//
// Stripe method eligibility matches the Stripe provider key independently of
// supportedTypes. Other methods use normalized support aliases; official
// WeChat candidates also compare configured app identity when JSAPI context
// supplies one. EasyPay WeChat candidates do not receive that official app
// filter. Configuration decode failures can therefore remove an official
// constrained candidate while ordinary candidates remain eligible.
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
		// Stripe checkout eligibility follows its provider key because
		// supportedTypes can contain hosted subtype labels. Other methods retain
		// their support-list and official JSAPI app checks.
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

// attachDailyUsage pairs each nonnil instance with PayAmount observed from
// midnight in the injected clock's location. Instance IDs use decimal text to
// match persisted order bindings, and returned candidate pointers remain
// shared.
//
// A batch-read error is logged but any returned partial map remains usable.
// Missing IDs, including a nil result map, contribute zero. The PostgreSQL
// usage includes pending, processing, paid, recharging and completed by
// created_at, so it differs from confirmation-time totals and can include
// unfinished order amounts.
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
	// Keep any returned partial totals when the batch read reports an error. The
	// map lookup still gives zero for missing IDs, and diagnostics expose the
	// failed observation.
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

// filterByLimits returns input-ordered candidates satisfying positive single
// and daily limits for the requested numeric amount. Nonpositive bounds
// disable their comparison, and nil/absent observers use a silent callback.
//
// Daily eligibility combines observed usage with the new amount; it does not
// reserve either value. Missing or unreadable limit JSON is unlimited here.
// The result can be empty, which SelectInstance handles by restoring every
// eligible candidate, so callers cannot treat this filter as universal
// rejection of channel-limit excess.
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

// getInstanceChannelLimits reads limits for a nonnil instance and requested
// method, returning zero limits for absent or malformed JSON. Stripe instances
// use the provider key, while other lookups try the supplied key and its
// supported legacy visible alias.
//
// A present exact entry wins even when all its values are zero. Only an absent
// exact entry permits the alias entry, so blank/unlimited configuration
// remains distinct from missing-key compatibility lookup. No currency
// conversion or enabled-state check is performed by this helper.
func getInstanceChannelLimits(inst *ProviderInstance, paymentType PaymentType) ChannelLimits {
	if inst.Limits == "" {
		return ChannelLimits{}
	}
	var limits InstanceLimits
	// Unreadable channel-limit JSON contributes zero limits so selection can
	// continue. The resulting lack of bounds remains advisory because
	// SelectInstance also restores an all-excluded eligible list.
	if err := json.Unmarshal([]byte(inst.Limits), &limits); err != nil {
		return ChannelLimits{}
	}
	// Stripe subtype requests share the provider's stripe limit entry. Map that
	// lookup before exact/legacy alias reads so configured hosted subtypes use
	// the same numeric channel bounds.
	lookupKey := paymentType
	if inst.ProviderKey == "stripe" {
		lookupKey = "stripe"
	}
	if cl, ok := limits[lookupKey]; ok {
		return cl
	}
	// Only a missing exact entry tries the historical visible alias. An existing
	// zero-valued entry stays unlimited instead of inheriting a separately stored
	// legacy bound.
	if aliasKey := legacyVisibleMethodAlias(lookupKey); aliasKey != "" {
		if cl, ok := limits[aliasKey]; ok {
			return cl
		}
	}
	return ChannelLimits{}
}

// pickByStrategy selects from a nonempty slice using least amount only for
// that exact strategy and multiple candidates. Other cases advance the
// balancer's shared counter before modulo, including one-candidate selections.
//
// Each call uses the current slice length, so its indexed result combines the
// global increment with that call's candidate ordering. Equal usage follows
// pickLeastAmount's first-seen rule. Unknown strategy text uses rotation, and
// callers keep candidate membership stable during the selection.
func (lb *DefaultLoadBalancer) pickByStrategy(candidates []instanceCandidate, strategy Strategy) instanceCandidate {
	if strategy == StrategyLeastAmount && len(candidates) > 1 {
		return pickLeastAmount(candidates)
	}
	// Advance the shared counter before indexing the current candidate list.
	// Concurrent selections get different increments, while changing list sizes
	// and single-candidate calls still affect later rotation.
	idx := lb.counter.Add(1) % uint64(len(candidates))
	return candidates[idx]
}

// pickLeastAmount returns the lowest dailyUsed candidate from a nonempty
// slice. Strictly lower comparison leaves equal totals in first-seen order, so
// caller-provided instance ordering determines usage ties.
func pickLeastAmount(candidates []instanceCandidate) instanceCandidate {
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.dailyUsed < best.dailyUsed {
			best = c
		}
	}
	return best
}

// buildSelection decodes a nonnil winning instance's configuration and adds
// its nonempty paymentMode before returning selection identity. Nil/unreadable
// config becomes a writable empty map; the newly decoded map is supplied
// directly to later provider construction.
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

// decryptConfig reads plaintext or historical encrypted configuration and
// emits a warning when parsing reports unreadable. Recoverable decode failures
// return nil for caller defaulting; Decrypt's decoded-nonce precondition
// remains applicable to legacy text.
func (lb *DefaultLoadBalancer) decryptConfig(stored string) map[string]string {
	config, ok := parseProviderConfig(stored, lb.encryptionKey)
	// Retain unreadable configuration as nil and log its stored length for
	// diagnosis. Selection/configuration callers then choose their
	// writable-empty-map behavior before constructing a provider.
	if !ok {
		lb.observe("warn", "payment provider config unreadable, treating as empty for re-entry",
			"stored_len", len(stored))
	}
	return config
}

// startOfDay returns midnight on the supplied date in its location.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// InstanceSupportsType reports whether comma-separated support includes the
// requested method or its exact visible alias normalization. An empty string
// accepts every target; each supplied label is trimmed before comparison, and
// other method spellings remain case-sensitive.
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

// normalizeVisibleMethodSupportType trims a method and maps only historical
// direct Alipay/WeChat aliases to their visible identities. Other strings
// retain their trimmed spelling, so support matching does not generalize
// arbitrary provider-prefixed labels.
func normalizeVisibleMethodSupportType(paymentType PaymentType) PaymentType {
	// Collapse historical direct labels before comparing support lists so
	// official visible methods and their stored aliases can match. Other labels
	// retain exact trimmed comparison.
	switch strings.TrimSpace(paymentType) {
	case TypeAlipay, TypeAlipayDirect:
		return TypeAlipay
	case TypeWxpay, TypeWxpayDirect:
		return TypeWxpay
	default:
		return strings.TrimSpace(paymentType)
	}
}

// legacyVisibleMethodAlias returns the historical direct alias for normalized
// Alipay/WeChat, or empty text for other methods. Channel-limit lookup uses
// that result only after the requested exact entry is absent.
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

// resolveWxpayJSAPIAppID returns trimmed mpAppId when nonempty, then trimmed
// appId. Official JSAPI eligibility and provider setup use that precedence; a
// nil map returns empty text for both reads.
func resolveWxpayJSAPIAppID(config map[string]string) string {
	if appID := strings.TrimSpace(config["mpAppId"]); appID != "" {
		return appID
	}
	// An absent official-account app identity leaves merchant appId as the JSAPI
	// app choice. This compatibility read is shared with eligibility so
	// construction can use the matched app.
	return strings.TrimSpace(config["appId"])
}

// GetInstanceConfig reads the addressed row and returns its current decoded
// string map for provider construction. Row lookup failure is an error;
// nil/unreadable configuration becomes a writable empty map, and parsing
// diagnostics use the injected observer.
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

// observe sends level, message and attributes through the optional injected
// selection observer. A missing callback suppresses diagnostics, allowing
// selection and configuration reads to retain their ordinary result handling.
func (lb *DefaultLoadBalancer) observe(level, message string, attrs ...any) {
	if lb.runtime.Observe != nil {
		lb.runtime.Observe(level, message, attrs...)
	}
}

// selectionObserver returns the first nonnil observer when present, otherwise
// a silent callback. Later supplied observers are ignored, so filter callers
// can invoke the result unconditionally while configuring at most one sink.
func selectionObserver(values []SelectionObserver) SelectionObserver {
	if len(values) > 0 && values[0] != nil {
		return values[0]
	}
	return func(string, string, ...any) {}
}
