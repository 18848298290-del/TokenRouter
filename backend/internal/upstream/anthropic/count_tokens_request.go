package anthropic

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// count_tokens_request.go builds ordinary and passthrough token-counting
// requests with authentication and beta-dependent body cleanup.
// Count-token requests share OAuth identity and beta preparation with Messages,
// but their bodies discard generation controls such as stream and max_tokens.
// The selected beta header governs which optional body fields survive before
// the HTTP request captures those bytes.
//
// Ordinary counting rewrites provider metadata, aligns the billing version with
// the selected User-Agent, and then assembles allowed client headers,
// fingerprint values, CLI defaults, and provider overrides. Passthrough
// counting keeps its client identity while replacing authentication and
// removing unsupported body fields. Both paths consult the provider beta
// override before sanitization so body compatibility follows the header that
// will be sent.

// BuildCountTokensRequestPassthrough prepares an API-key counting request using
// the endpoint supplied by options.URL. Its body removes deferred-tool cache
// markers and generation fields, then applies the beta-dependent field rules
// selected from client headers and the provider override.
//
// Allowed client headers keep their wire casing and authentication uses token.
// Canonical Header.Get checks select content-type and protocol-version defaults
// after raw header forwarding. Provider overrides run last. Callers supply URL,
// BetaOverride, and ApplyOverrides; the request retains its prepared body under
// ctx.
//
// The request reader and its GetBody snapshots can share prepared input
// storage. Callers keep those bytes unchanged while the transport can still
// read them.
func BuildCountTokensRequestPassthrough(ctx context.Context, body []byte, token string, options RequestOptions) (*http.Request, error) {
	body = StripDeferredToolCacheControl(body)
	targetURL, err := options.URL()
	if err != nil {
		return nil, err
	}
	body = SanitizeCountTokensRequestBody(body)

	// Count-token bodies still contain optional beta-gated inputs. Use the
	// selected header before request construction so those fields follow
	// outbound feature eligibility.
	clientBeta := ""
	if options.ClientHeaders != nil {
		clientBeta = GetHeaderRaw(options.ClientHeaders, "anthropic-beta")
	}
	// An enabled provider beta override becomes the final header during
	// override application. Sanitize against that value to align optional
	// fields with the sent request.
	if beta, ok := options.BetaOverride(); ok {
		clientBeta = beta
	}
	if sanitized, changed := SanitizeAnthropicBodyForBetaTokens(body, clientBeta); changed {
		body = sanitized
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if options.ClientHeaders != nil {
		for key, values := range options.ClientHeaders {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if !AllowedHeaders[lowerKey] {
				continue
			}
			wireKey := ResolveWireCasing(key)
			for _, v := range values {
				AddHeaderRaw(req.Header, wireKey, v)
			}
		}
	}

	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("cookie")
	SetAPIKeyAuthHeader(req.Header, options.APIKeyBearer, token)

	// Canonical lookup does not read the raw lowercase keys forwarded above.
	// These checks can add canonical defaults beside those entries before
	// provider overrides apply.
	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	// Provider values apply after forwarded headers, replacement credentials,
	// and protocol defaults, so the configured header choices are the ones sent
	// by the caller.
	options.ApplyOverrides(req.Header)

	return req, nil
}

// BuildCountTokensRequest returns the counting HTTP request and its prepared
// body. Callers supply endpoint, beta-policy, and header-override callbacks;
// fingerprint, forwarding, and capture services are optional. URL and
// request-construction errors stop preparation, while identity-rewrite failures
// retain the available body for counting.
//
// OAuth metadata rewriting and fingerprint header forwarding use separate
// switches. Billing markers follow the effective User-Agent, including forced
// mimic headers, before the counting beta set is selected. Sanitization removes
// beta-ineligible fields and generation controls, then header assembly aligns
// an existing session header with the metadata session. Provider overrides
// apply last and Capture sees the completed request.
//
// The request reader and its GetBody snapshots can share prepared input
// storage. Callers keep those bytes unchanged while the transport can still
// read them.
func BuildCountTokensRequest(ctx context.Context, body []byte, token, tokenType, modelID string, mimicClaudeCode bool, options RequestOptions) (*http.Request, []byte, error) {
	body = StripDeferredToolCacheControl(body)
	targetURL, err := options.URL()
	if err != nil {
		return nil, nil, err
	}
	clientHeaders := options.ClientHeaders
	// Counting retains separate switches for provider fingerprint headers and
	// metadata passthrough. Resolve both before selecting device/account
	// rewriting and the optional provider session mask.
	ctEnableFP, ctEnableMPT := true, false
	if options.Forwarding != nil {
		ctEnableFP, ctEnableMPT = options.Forwarding(ctx)
	}
	var ctFingerprint *Fingerprint
	if options.OAuth && options.Fingerprint != nil {
		fp, err := options.Fingerprint.GetOrCreateFingerprint(ctx, options.ProviderID, clientHeaders)
		// A cache read failure creates identity and a write failure retains it;
		// GetOrCreateFingerprint returns nil error for both. This guard accepts
		// that selected identity for optional metadata and header preparation.
		if err == nil {
			ctFingerprint = fp
			if !ctEnableMPT {
				providerUUID := options.AccountUUID
				if providerUUID != "" && fp.ClientID != "" {
					// Metadata rewriting is best effort for counting. Keep the
					// available body on failure, and let a successful nonempty
					// rewrite supply identity for later session alignment.
					if newBody, err := options.Fingerprint.RewriteUserIDWithMasking(ctx, body, options.ProviderID, options.MaskSession, providerUUID, fp.ClientID, fp.UserAgent); err == nil && len(newBody) > 0 {
						body = newBody
					}
				}
			}
		}
	}

	// Mimic headers can select the runtime CLI UA even when header unification
	// is disabled. Billing markers therefore use that effective UA before
	// request-body construction.
	var billingFingerprint *Fingerprint
	if ctEnableFP {
		billingFingerprint = ctFingerprint
	}
	if billingUA := EffectiveBillingUserAgent(tokenType, mimicClaudeCode, billingFingerprint); billingUA != "" {
		body = SyncBillingHeaderVersion(body, billingUA)
	}

	// Counting adds its own beta requirements to the selected authentication
	// path. Resolve that header before optional field removal and
	// generation-control cleanup capture the outbound body.
	ctEffectiveDropSet := MergeDropSets(options.FilterSet(ctx))
	finalBetaHeader, finalBetaShouldSet := ComputeFinalCountTokensAnthropicBeta(
		tokenType, mimicClaudeCode, modelID, clientHeaders, body, ctEffectiveDropSet, options.InjectAPIKeyBeta,
	)

	// An enabled provider beta override becomes the final header during
	// override application. Sanitize against that value to align optional
	// fields with the sent request.
	if beta, ok := options.BetaOverride(); ok {
		finalBetaHeader, finalBetaShouldSet = beta, true
	}

	if sanitized, changed := SanitizeAnthropicBodyForBetaTokens(body, finalBetaHeader); changed {
		body = sanitized
	}
	body = SanitizeCountTokensRequestBody(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	// The token type selects OAuth authorization or the configured API-key
	// scheme. Raw setters retain the intended header casing during this
	// credential insertion.
	if tokenType == "oauth" {
		SetHeaderRaw(req.Header, "authorization", "Bearer "+token)
	} else {
		SetAPIKeyAuthHeader(req.Header, options.APIKeyBearer, token)
	}

	// Counting permits allowed client headers even for mimic preparation.
	// Forward their selected wire forms before cached fingerprint and CLI
	// values take precedence.
	for key, values := range clientHeaders {
		lowerKey := strings.ToLower(key)
		if AllowedHeaders[lowerKey] {
			wireKey := ResolveWireCasing(key)
			for _, v := range values {
				AddHeaderRaw(req.Header, wireKey, v)
			}
		}
	}

	if ctEnableFP && ctFingerprint != nil {
		options.Fingerprint.ApplyFingerprint(req, ctFingerprint)
	}

	// Client input can omit the protocol headers. Supply missing content type
	// and version after identity forwarding so existing values remain selected
	// through this default phase.
	if GetHeaderRaw(req.Header, "content-type") == "" {
		SetHeaderRaw(req.Header, "content-type", "application/json")
	}
	if GetHeaderRaw(req.Header, "anthropic-version") == "" {
		SetHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}
	if tokenType == "oauth" {
		ApplyClaudeOAuthHeaderDefaults(req)
	}

	// Counting mimic installs CLI values after a cached fingerprint is applied,
	// matching the effective UA used earlier for system billing version
	// synchronization.
	if tokenType == "oauth" && mimicClaudeCode {
		ApplyClaudeCodeMimicHeaders(req, false)
	}

	// The forwarded beta can differ from counting's selected value. Remove all
	// case forms before writing the header that already governed body
	// sanitization.
	DeleteHeaderAllForms(req.Header, "anthropic-beta")
	if finalBetaShouldSet {
		SetHeaderRaw(req.Header, "anthropic-beta", finalBetaHeader)
	}

	// Provider identity rewriting may select another session. Align an existing
	// session header with the parsed metadata output before provider overrides
	// apply their final values.
	if sessionHeader := GetHeaderRaw(req.Header, "X-Claude-Code-Session-Id"); sessionHeader != "" {
		if uid := gjson.GetBytes(body, "metadata.user_id").String(); uid != "" {
			if parsed := ParseMetadataUserID(uid); parsed != nil {
				SetHeaderRaw(req.Header, "X-Claude-Code-Session-Id", parsed.SessionID)
			}
		}
	}

	// Enabled provider values receive precedence after client forwarding,
	// cached fingerprint headers, protocol defaults, and mimic selection,
	// completing the headers seen by Capture.
	options.ApplyOverrides(req.Header)

	if options.Capture != nil {
		options.Capture(req, body)
	}

	return req, body, nil
}

// SanitizeCountTokensRequestBody removes the listed top-level generation
// controls from a counting body and returns the resulting bytes. Each
// successful deletion feeds the next path; a failed deletion retains the
// current body and processing continues through the remaining controls.
func SanitizeCountTokensRequestBody(body []byte) []byte {
	out := body
	for _, path := range []string{
		"temperature",
		"top_p",
		"top_k",
		"stream",
		"stop_sequences",
		"stop",
		"max_tokens",
	} {
		// Count-token payloads use the input side of Messages. Remove this
		// generation control when the path edit succeeds; a failed deletion
		// retains current bytes for the remaining paths.
		if next, ok := DeleteJSONPathBytes(out, path); ok {
			out = next
		}
	}
	return out
}
