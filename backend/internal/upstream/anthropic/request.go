package anthropic

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

type RequestOptions struct {
	OriginalPolicy                   func(context.Context, string, string) (map[string]struct{}, error)
	InjectAPIKeyBeta                 bool
	ProviderID                       int64
	OAuth, MaskSession, APIKeyBearer bool
	AccountUUID                      string
	ClientHeaders                    http.Header
	Fingerprint                      *RequestFingerprint
	URL                              func() (string, error)
	FastMode                         func(context.Context, []byte, http.Header) ([]byte, http.Header, error)
	Forwarding                       func(context.Context) (bool, bool)
	FilterSet                        func(context.Context) map[string]struct{}
	BetaOverride                     func() (string, bool)
	CheckFastBeta                    func(context.Context) error
	ApplyOverrides                   func(http.Header)
	Debug                            func(*http.Request, []byte, map[string]string)
	Capture                          func(*http.Request, []byte)
}

func SetAPIKeyAuthHeader(header http.Header, bearer bool, token string) {
	if bearer {
		SetHeaderRaw(header, "authorization", "Bearer "+token)
	} else {
		SetHeaderRaw(header, "x-api-key", token)
	}
}

func BuildRequest(ctx context.Context, body []byte, token, tokenType, modelID string, reqStream, mimicClaudeCode bool, options RequestOptions) (*http.Request, []byte, error) {
	body = StripDeferredToolCacheControl(body)
	targetURL, err := options.URL()
	if err != nil {
		return nil, nil, err
	}
	clientHeaders := options.ClientHeaders
	body, clientHeaders, err = options.FastMode(ctx, body, clientHeaders)
	if err != nil {
		return nil, nil, err
	}

	// Forwarding settings independently control fingerprint headers and metadata rewriting.
	var fingerprint *Fingerprint
	enableFP, enableMPT := true, false
	if options.Forwarding != nil {
		enableFP, enableMPT = options.Forwarding(ctx)
	}
	if options.OAuth && options.Fingerprint != nil {
		fp, err := options.Fingerprint.GetOrCreateFingerprint(ctx, options.ProviderID, clientHeaders)
		if err != nil {
			logger.LegacyPrintf("service.gateway", "Warning: failed to get fingerprint for provider %d: %v", options.ProviderID, err)
			// Keep the client headers when fingerprint lookup fails.
		} else {
			if enableFP {
				fingerprint = fp
			}

			// Rewrite identity components unless metadata passthrough is enabled.
			// Optional masking replaces the session component after ordinary rewriting.
			if !enableMPT {
				providerUUID := options.AccountUUID
				if providerUUID != "" && fp.ClientID != "" {
					if newBody, err := options.Fingerprint.RewriteUserIDWithMasking(ctx, body, options.ProviderID, options.MaskSession, providerUUID, fp.ClientID, fp.UserAgent); err == nil && len(newBody) > 0 {
						body = newBody
					}
				}
			}
		}
	}

	// Billing markers must match the final UA, including a later mimic override.
	if billingUA := EffectiveBillingUserAgent(tokenType, mimicClaudeCode, fingerprint); billingUA != "" {
		body = SyncBillingHeaderVersion(body, billingUA)
	}

	// Finalize betas before sanitizing the body and constructing the HTTP request.
	// Header forwarding and fingerprint injection below must use this same beta value.
	policyFilterSet := options.FilterSet(ctx)
	effectiveDropSet := MergeDropSets(policyFilterSet)
	finalBetaHeader, finalBetaShouldSet := ComputeFinalAnthropicBeta(
		tokenType, mimicClaudeCode, modelID, clientHeaders, body, effectiveDropSet, options.InjectAPIKeyBeta,
	)

	// Sanitize against the provider override because it becomes the outbound beta
	// header; using another value can leave fields the upstream schema rejects.
	if beta, ok := options.BetaOverride(); ok {
		finalBetaHeader, finalBetaShouldSet = beta, true
	}
	if ContainsBetaToken(finalBetaHeader, BetaFastMode) {
		if blockErr := options.CheckFastBeta(ctx); blockErr != nil {
			return nil, nil, blockErr
		}
	}

	if sanitized, changed := SanitizeAnthropicBodyForBetaTokens(body, finalBetaHeader); changed {
		body = sanitized
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	// Preserve authentication header wire casing.
	if tokenType == "oauth" {
		SetHeaderRaw(req.Header, "authorization", "Bearer "+token)
	} else {
		SetAPIKeyAuthHeader(req.Header, options.APIKeyBearer, token)
	}

	// Mimic requests use a consistent CLI fingerprint. Forwarding client SDK, UA,
	// and session headers here could conflict with the injected identity.
	if tokenType != "oauth" || !mimicClaudeCode {
		for key, values := range clientHeaders {
			lowerKey := strings.ToLower(key)
			if AllowedHeaders[lowerKey] {
				wireKey := ResolveWireCasing(key)
				for _, v := range values {
					AddHeaderRaw(req.Header, wireKey, v)
				}
			}
		}
	}

	// Cached fingerprint headers override forwarded client values.
	if fingerprint != nil {
		options.Fingerprint.ApplyFingerprint(req, fingerprint)
	}

	// Fill required headers with their wire casing.
	if GetHeaderRaw(req.Header, "content-type") == "" {
		SetHeaderRaw(req.Header, "content-type", "application/json")
	}
	if GetHeaderRaw(req.Header, "anthropic-version") == "" {
		SetHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}
	if tokenType == "oauth" {
		ApplyClaudeOAuthHeaderDefaults(req)
	}

	// Mimic headers take precedence over the cached OAuth fingerprint.
	if tokenType == "oauth" && mimicClaudeCode {
		ApplyClaudeCodeMimicHeaders(req, reqStream)
	}

	// Remove forwarded beta values before applying the final filtered header.
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

	// Apply provider overrides last so configured values take precedence.
	options.ApplyOverrides(req.Header)

	if options.Debug != nil {
		options.Debug(req, body, map[string]string{"url": req.URL.String(), "token_type": tokenType, "mimic_claude_code": strconv.FormatBool(mimicClaudeCode), "fingerprint_applied": strconv.FormatBool(fingerprint != nil), "enable_fp": strconv.FormatBool(enableFP), "enable_mpt": strconv.FormatBool(enableMPT)})
	}
	if options.Capture != nil {
		options.Capture(req, body)
	}
	return req, body, nil
}
