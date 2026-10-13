package anthropic

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// API-key passthrough forwards allowed client request headers while replacing
// the credential used for the selected provider. Optional body fields still
// depend on the beta header selected by policy and provider overrides, so
// passthrough preparation can change the returned wire bytes.
//
// OriginalPolicy evaluates the supplied model and client beta before FastMode
// can revise the body or headers. The filtered or overridden beta then gates
// the body, and fast-mode eligibility is checked when that token remains.
// Authentication replacement follows allowed-header copying, required protocol
// headers receive defaults, and provider overrides take final precedence. The
// returned body lets the exchange synchronize parsed input before dispatch.

// BuildRequestPassthrough returns an API-key Messages request and its sanitized
// wire body. Callers provide URL, OriginalPolicy, FastMode, BetaOverride,
// CheckFastBeta, and ApplyOverrides; policy and fast-mode errors stop
// construction before dispatch.
//
// The initial beta-policy evaluation uses the client's model and header, while
// body sanitization uses the value selected after fast-mode processing and any
// provider beta override. Allowed headers are copied with wire casing, client
// credentials are replaced by token, and provider overrides run last. The
// returned bytes may differ from body and remain available to parsed-input
// synchronization.
//
// The request reader and its GetBody snapshots can share prepared input
// storage. Callers keep those bytes unchanged while the transport can still
// read them.
func BuildRequestPassthrough(ctx context.Context, body []byte, token string, options RequestOptions) (*http.Request, []byte, error) {
	body = StripDeferredToolCacheControl(body)
	targetURL, err := options.URL()
	if err != nil {
		return nil, nil, err
	}
	clientHeaders := options.ClientHeaders
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	filterSet, err := options.OriginalPolicy(ctx, GetHeaderRaw(clientHeaders, "anthropic-beta"), model)
	if err != nil {
		return nil, nil, err
	}
	body, clientHeaders, err = options.FastMode(ctx, body, clientHeaders)
	if err != nil {
		return nil, nil, err
	}
	clientBeta := StripBetaTokensWithSet(GetHeaderRaw(clientHeaders, "anthropic-beta"), filterSet)
	// The provider beta override will replace the forwarded value at final
	// header application. Use it now so optional body fields match the selected
	// outbound header.
	if beta, ok := options.BetaOverride(); ok {
		clientBeta = beta
	}
	if ContainsBetaToken(clientBeta, BetaFastMode) {
		if blockErr := options.CheckFastBeta(ctx); blockErr != nil {
			return nil, nil, blockErr
		}
	}
	if sanitized, changed := SanitizeAnthropicBodyForBetaTokens(body, clientBeta); changed {
		body = sanitized
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	for key, values := range clientHeaders {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if !AllowedHeaders[lowerKey] {
			continue
		}
		wireKey := ResolveWireCasing(key)
		for _, v := range values {
			AddHeaderRaw(req.Header, wireKey, v)
		}
	}
	// Allowed-header forwarding can retain client betas that policy removed.
	// Replace their case forms with the selected value used to sanitize this
	// request's body.
	DeleteHeaderAllForms(req.Header, "anthropic-beta")
	if clientBeta != "" {
		SetHeaderRaw(req.Header, "anthropic-beta", clientBeta)
	}

	// Allowed client headers are copied before credential selection. Remove
	// credential and cookie entries so the prepared request uses the selected
	// provider token before final overrides.
	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("cookie")
	SetAPIKeyAuthHeader(req.Header, options.APIKeyBearer, token)

	if GetHeaderRaw(req.Header, "content-type") == "" {
		SetHeaderRaw(req.Header, "content-type", "application/json")
	}
	if GetHeaderRaw(req.Header, "anthropic-version") == "" {
		SetHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}

	// Provider overrides run after forwarded values, policy-selected beta,
	// credentials, and defaults. Enabled values therefore define the final
	// header choices retained in this request.
	options.ApplyOverrides(req.Header)

	return req, body, nil
}
