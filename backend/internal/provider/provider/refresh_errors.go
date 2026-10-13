package provider

import (
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Refresh error labels can describe a shared platform configuration failure or
// rejected credentials on one record. Qoder typed invalid-credential errors
// take precedence over generic client or scope text, and Grok's coarse
// entitlement label needs additional evidence before it identifies a permanent
// account denial.
//
// Ambiguous Grok errors retain that distinction when their message lacks
// recognized subscription wording or explicit denial codes inside the body
// fragment. The background attempt configuration combines these classifiers to
// contain shared or ambiguous failures at the platform cycle while handling
// explicit credential failures separately. These predicates inspect the
// supplied error and provider category; they perform no exchange or state write
// themselves.

// IsAmbiguousGrokEntitlementRefreshError reports a coarse entitlement-labelled
// error that lacks explicit account-denial evidence for a Grok OAuth record.
// Nil errors and other records return false, as do errors without the coarse
// label.
//
// Recognized subscription phrases anywhere in the message and specific denial
// codes after body: make the label conclusive here. Otherwise the error remains
// ambiguous for background cycle handling. Checking the body fragment
// separately keeps a generic coarse wrapper from serving as its own evidence of
// permanent subscription loss.
func IsAmbiguousGrokEntitlementRefreshError(value *provider.Record, err error) bool {
	if value == nil || !value.IsGrokOAuth() || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	coarseEntitlementLabel := strings.EqualFold(apperror.Reason(err), "GROK_OAUTH_ENTITLEMENT_DENIED") ||
		strings.Contains(msg, "grok_oauth_entitlement_denied")
	if !coarseEntitlementLabel {
		return false
	}

	for _, evidence := range []string{
		"subscription required",
		"no active grok subscription",
		"no active subscription",
		"grok subscription required",
		"provider is not entitled",
		"not entitled",
		"entitlement required",
		"subscription inactive",
		"subscription expired",
		"upgrade your plan",
	} {
		if strings.Contains(msg, evidence) {
			return false
		}
	}
	if _, after, ok := strings.Cut(msg, "body:"); ok {
		body := after
		for _, evidence := range []string{
			"entitlement_denied",
			"entitlement denied",
			"subscription_required",
			"no_active_subscription",
		} {
			if strings.Contains(body, evidence) {
				return false
			}
		}
	}
	return true
}

// IsSharedProviderRefreshError reports client or scope fragments used for
// shared platform failure handling. Nil errors return false, and typed Qoder
// invalid-credential errors return false before message matching.
//
// A rejected Qoder PAT or refresh token stays an account outcome even when its
// text contains invalid_client. RefreshAttemptService evaluates this predicate
// before NonRetryable, so the typed exclusion lets provider-health handling
// receive that credential failure while generic client and scope errors contain
// the platform cycle.
func IsSharedProviderRefreshError(err error) bool {
	if err == nil {
		return false
	}
	var qoderOpenAPIErr *qoder.OpenAPIError
	// A typed Qoder rejection identifies account credentials even when its message
	// mentions a client error. Exclude it from shared classification before the
	// attempt service decides permanent provider-health handling.
	if errors.As(err, &qoderOpenAPIErr) && qoderOpenAPIErr.InvalidCredentials() {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"invalid_client",
		"unauthorized_client",
		"invalid_scope",
		"unknown scope",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// IsNonRetryableRefreshError reports typed Qoder credential rejection or
// recognized permanent credential, client, scope, and subscription fragments.
// Nil errors return false, text matching ignores case, and background callers
// separately apply shared-error and Grok-ambiguity classification.
func IsNonRetryableRefreshError(err error) bool {
	if err == nil {
		return false
	}
	var qoderOpenAPIErr *qoder.OpenAPIError
	if errors.As(err, &qoderOpenAPIErr) && qoderOpenAPIErr.InvalidCredentials() {
		return true
	}
	msg := strings.ToLower(err.Error())
	nonRetryable := []string{
		"invalid_grant",                       // refresh token rejected
		"invalid_refresh_token",               // invalid refresh token response label
		"token_expired",                       // expired token response label
		"app_session_terminated",              // terminated application-session label
		"invalid_client",                      // invalid client configuration
		"unauthorized_client",                 // unauthorized client
		"access_denied",                       // access refused
		"refresh_token_reused",                // consumed OpenAI refresh token
		"refresh_token_invalidated",           // upstream refresh-token invalidation label
		"refresh token has already been used", // accept the message when the error body omits a code
		"missing_project_id",                  // missing project_id
		"no refresh token available",
		"grok_oauth_entitlement_denied",
		"entitlement_denied",
		"invalid_scope",
		"unknown scope",
		"subscription required",
		"no active grok subscription",
	}
	for _, needle := range nonRetryable {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
