package provider

import (
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

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

func IsSharedProviderRefreshError(err error) bool {
	if err == nil {
		return false
	}
	var qoderOpenAPIErr *qoder.OpenAPIError
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

// IsNonRetryableRefreshError reports credential and configuration failures that retries cannot resolve.
// These failures require reauthorization or configuration repair.
// missing_project_id denotes a missing project, rather than a transient project lookup failure.
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
		"invalid_refresh_token",               // invalid token, including deleted team workspaces
		"token_expired",                       // expired OpenAI refresh token
		"app_session_terminated",              // terminated OpenAI app session
		"invalid_client",                      // invalid client configuration
		"unauthorized_client",                 // unauthorized client
		"access_denied",                       // access refused
		"refresh_token_reused",                // consumed OpenAI refresh token
		"refresh_token_invalidated",           // refresh token revoked after the OpenAI session ended
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
