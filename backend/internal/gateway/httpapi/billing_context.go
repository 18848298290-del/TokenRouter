package httpapi

import (
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
)

// billing_context.go exposes the funding object stored in Gin request context.
// Funding admission saves its chosen source in Gin context before execution
// captures completion data. HTTP consumers retrieve the same billing object
// through a lookup interface, allowing request adapters and test contexts to
// share the context-key spelling.

// Billing context keys identify funding authorization and the selected subscription
// saved by middleware for downstream execution and completion capture.
const (
	ContextKeyAPIKeyBilling = "api_key_billing" // resolved request funding
	ContextKeySubscription  = "subscription"    // selected billing subscription
)

// ContextGetter supports Gin context lookup and lightweight test substitutes.
type ContextGetter interface {
	// Get returns the stored value and reports whether key is present.
	// Consumers inspect the value's dynamic type independently of existence; a
	// present key can still contain a nil pointer or another value type.
	Get(key string) (value any, exists bool)
}

// GetAPIKeyBillingContext reads the funding source saved by middleware.
// A nil interface, missing key, or another dynamic type returns false. Successful
// lookup returns the stored pointer, including a typed nil pointer, so consumers
// check both the flag and pointer before reading funding details.
func GetAPIKeyBillingContext(c ContextGetter) (*billingcore.APIKeyBillingContext, bool) {
	if c == nil {
		return nil, false
	}
	value, exists := c.Get(string(ContextKeyAPIKeyBilling))
	if !exists {
		return nil, false
	}
	billing, ok := value.(*billingcore.APIKeyBillingContext)
	return billing, ok
}
