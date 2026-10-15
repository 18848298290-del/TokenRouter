package openaiforward

import "net/http"

// passthrough_failure.go decides whether captured passthrough failures qualify
// for provider failover.
// Passthrough failures can overlap content, context, credential, capacity, and
// server classifications. Content and context rejections stop failover before
// any retryable category is considered, while access-state, payload-size, pool-
// policy, and capacity evidence can permit another provider independently of
// credential type.
//
// Ordinary server and proxy-edge statuses enter failover only for API Key
// providers after the earlier categories have been checked. The predicate
// reports eligibility to its caller; the caller still applies health effects
// and output-window rules before another exchange can occur. Predicate
// callbacks inspect the same captured body in their declared order.

// PassthroughFailureOptions supplies ordered predicates for classifying captured passthrough HTTP failures.
type PassthroughFailureOptions struct {
	Cyber         func([]byte) bool      // content-policy denial predicate
	ContextWindow func([]byte) bool      // context-capacity denial predicate
	AccessState   func(int, []byte) bool // credential-access failure predicate
	BodyTooLarge  func(int, []byte) bool // request-size incompatibility predicate
	PoolRetryable func(int) bool         // provider pool retry predicate
	APIKey        bool                   // API Key credential indicator
}

// ShouldFailoverPassthrough reports whether captured HTTP failure evidence is
// eligible for another provider attempt. All predicate callbacks are required,
// and content-policy or context-window classifications take precedence over
// access-state, body-size, pool retry, and capacity categories.
//
// Rate limits and overload status permit failover after the vetoes for either
// credential type. API Key providers additionally accept the listed server and
// proxy-edge failures; other statuses return false. The execution caller owns
// health observations and determines whether response commitment still allows
// using this eligibility result.
func ShouldFailoverPassthrough(status int, body []byte, o PassthroughFailureOptions) bool {
	// Content and context denials describe request-specific failures, so evaluate
	// both vetoes before credential, size, or capacity evidence can permit another
	// provider.
	if o.Cyber(body) {
		return false
	}
	if o.ContextWindow(body) {
		return false
	}
	if o.AccessState(status, body) {
		return true
	}
	if o.BodyTooLarge(status, body) {
		return true
	}
	if o.PoolRetryable(status) {
		return true
	}
	switch status {
	case http.StatusTooManyRequests, 529:
		return true
	}
	if !o.APIKey {
		return false
	}
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 521, 522, 523, 524:
		return true
	default:
		return false
	}
}
