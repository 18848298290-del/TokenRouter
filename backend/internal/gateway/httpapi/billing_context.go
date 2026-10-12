package httpapi

import (
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
)

// These context keys store funding authorization and subscription data for the request.
const (
	ContextKeyAPIKeyBilling = "api_key_billing"
	ContextKeySubscription  = "subscription"
)

// ContextGetter supports Gin context lookup and lightweight test substitutes.
type ContextGetter interface {
	Get(key string) (value any, exists bool)
}

// GetAPIKeyBillingContext reads the funding source saved by middleware.
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
