package anthropic

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

func BuildCountTokensRequestPassthrough(ctx context.Context, body []byte, token string, options RequestOptions) (*http.Request, error) {
	body = StripDeferredToolCacheControl(body)
	targetURL, err := options.URL()
	if err != nil {
		return nil, err
	}
	body = SanitizeCountTokensRequestBody(body)

	// Gate body fields against the final beta header, as in Messages passthrough.
	clientBeta := ""
	if options.ClientHeaders != nil {
		clientBeta = GetHeaderRaw(options.ClientHeaders, "anthropic-beta")
	}
	// Provider overrides determine the beta header used for body sanitization.
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

	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	// Provider overrides take precedence over headers assembled above.
	options.ApplyOverrides(req.Header)

	return req, nil
}

// BuildCountTokensRequest builds the upstream count_tokens request and returns its body.
func BuildCountTokensRequest(ctx context.Context, body []byte, token, tokenType, modelID string, mimicClaudeCode bool, options RequestOptions) (*http.Request, []byte, error) {
	body = StripDeferredToolCacheControl(body)
	targetURL, err := options.URL()
	if err != nil {
		return nil, nil, err
	}
	clientHeaders := options.ClientHeaders
	// OAuth fingerprint settings control header and user ID rewriting.
	// Session masking can replace the rewritten session component.
	ctEnableFP, ctEnableMPT := true, false
	if options.Forwarding != nil {
		ctEnableFP, ctEnableMPT = options.Forwarding(ctx)
	}
	var ctFingerprint *Fingerprint
	if options.OAuth && options.Fingerprint != nil {
		fp, err := options.Fingerprint.GetOrCreateFingerprint(ctx, options.ProviderID, clientHeaders)
		if err == nil {
			ctFingerprint = fp
			if !ctEnableMPT {
				providerUUID := options.AccountUUID
				if providerUUID != "" && fp.ClientID != "" {
					if newBody, err := options.Fingerprint.RewriteUserIDWithMasking(ctx, body, options.ProviderID, options.MaskSession, providerUUID, fp.ClientID, fp.UserAgent); err == nil && len(newBody) > 0 {
						body = newBody
					}
				}
			}
		}
	}

	// Mimic headers remain forced when fingerprint unification is off; billing must match their UA.
	var billingFingerprint *Fingerprint
	if ctEnableFP {
		billingFingerprint = ctFingerprint
	}
	if billingUA := EffectiveBillingUserAgent(tokenType, mimicClaudeCode, billingFingerprint); billingUA != "" {
		body = SyncBillingHeaderVersion(body, billingUA)
	}

	// Select the final beta header before sanitizing body fields.
	ctEffectiveDropSet := MergeDropSets(options.FilterSet(ctx))
	finalBetaHeader, finalBetaShouldSet := ComputeFinalCountTokensAnthropicBeta(
		tokenType, mimicClaudeCode, modelID, clientHeaders, body, ctEffectiveDropSet, options.InjectAPIKeyBeta,
	)

	// Provider overrides determine the beta header used for body sanitization.
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

	// Keep authentication header wire casing.
	if tokenType == "oauth" {
		SetHeaderRaw(req.Header, "authorization", "Bearer "+token)
	} else {
		SetAPIKeyAuthHeader(req.Header, options.APIKeyBearer, token)
	}

	// Restore wire casing for allowed client headers.
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

	// Fill required headers using their wire casing.
	if GetHeaderRaw(req.Header, "content-type") == "" {
		SetHeaderRaw(req.Header, "content-type", "application/json")
	}
	if GetHeaderRaw(req.Header, "anthropic-version") == "" {
		SetHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}
	if tokenType == "oauth" {
		ApplyClaudeOAuthHeaderDefaults(req)
	}

	// Mimic headers override the selected OAuth fingerprint.
	if tokenType == "oauth" && mimicClaudeCode {
		ApplyClaudeCodeMimicHeaders(req, false)
	}

	// Remove all header forms before writing the final beta value.
	DeleteHeaderAllForms(req.Header, "anthropic-beta")
	if finalBetaShouldSet {
		SetHeaderRaw(req.Header, "anthropic-beta", finalBetaHeader)
	}

	// Match an existing session header to the rewritten metadata session ID.
	if sessionHeader := GetHeaderRaw(req.Header, "X-Claude-Code-Session-Id"); sessionHeader != "" {
		if uid := gjson.GetBytes(body, "metadata.user_id").String(); uid != "" {
			if parsed := ParseMetadataUserID(uid); parsed != nil {
				SetHeaderRaw(req.Header, "X-Claude-Code-Session-Id", parsed.SessionID)
			}
		}
	}

	// Apply enabled provider header overrides.
	options.ApplyOverrides(req.Header)

	if options.Capture != nil {
		options.Capture(req, body)
	}

	return req, body, nil
}

func SanitizeCountTokensRequestBody(body []byte) []byte {
	out := body
	for _, path := range []string{
		"temperature",
		"top_p",
		"top_k",
		"stream",
		"stop_sequences",
		"stop",
		// count_tokens accepts input fields. Remove the generation limit that
		// OAuth mimic may have supplied for a Messages request.
		"max_tokens",
	} {
		if next, ok := DeleteJSONPathBytes(out, path); ok {
			out = next
		}
	}
	return out
}
