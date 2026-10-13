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

// A Messages body can carry identity and optional fields that depend on its
// outbound User-Agent and beta header. Preparing these independently would let
// a billing version, session header, or gated body field disagree with the
// headers sent to the selected provider.
//
// BuildRequest applies fast-mode policy before selecting OAuth fingerprint
// behavior. Header forwarding and metadata rewriting have separate settings,
// while CLI mimic headers can override the cached fingerprint. Billing markers
// are synchronized before body capture, and the final beta value drives body
// sanitization. Authentication, allowed client headers, fingerprint values,
// required defaults, and provider overrides are then assembled in their defined
// order; debug and capture callbacks receive that completed request.

// RequestOptions supplies provider identity, request policy, and synchronous
// construction callbacks for Messages and counting. Each builder documents the
// callbacks it invokes; callers keep borrowed headers and services usable until
// preparation finishes.
type RequestOptions struct {
	// OriginalPolicy receives ctx, the client beta, then the initial model for
	// Messages passthrough. A returned block error ends construction before
	// fast-mode preparation.
	OriginalPolicy func(context.Context, string, string) (map[string]struct{}, error)
	// InjectAPIKeyBeta permits ordinary API-key builders to choose needed
	// default betas when the client supplies none; passthrough uses its
	// selected client value.
	InjectAPIKeyBeta bool
	ProviderID       int64 // selected provider record ID
	// OAuth enables provider fingerprint lookup when Fingerprint is configured,
	// independently of tokenType's authorization selection. MaskSession chooses
	// provider session masking when metadata rewriting is enabled and
	// account/device inputs are usable. APIKeyBearer chooses authorization
	// Bearer for API-key builders; tokenType determines whether ordinary
	// builders use OAuth authorization.
	OAuth, MaskSession, APIKeyBearer bool
	AccountUUID                      string // provider OAuth account UUID
	// ClientHeaders supplies inbound header values. FastMode can return revised
	// headers before ordinary Messages construction; builders copy allowed
	// values into their outbound request maps.
	ClientHeaders http.Header
	// Fingerprint supplies optional OAuth identity lookup and rewriting. An
	// absent service leaves those preparation paths inactive while required
	// header construction continues.
	Fingerprint *RequestFingerprint
	// URL resolves the selected endpoint before HTTP construction. Builders
	// require this callback and propagate its error before any request is
	// dispatched.
	URL func() (string, error)
	// FastMode prepares body and headers for Messages builders before beta
	// assembly. A returned error stops construction; count-token builders do
	// not invoke it.
	FastMode func(context.Context, []byte, http.Header) ([]byte, http.Header, error)
	// Forwarding selects fingerprint headers and metadata passthrough
	// separately. When absent, header unification is enabled and metadata
	// passthrough is disabled for ordinary OAuth builders.
	Forwarding func(context.Context) (bool, bool)
	// FilterSet supplies the current beta drop set for ordinary Messages and
	// counting. Those builders require the callback. Messages passthrough uses
	// OriginalPolicy; passthrough counting does not invoke a policy callback.
	FilterSet func(context.Context) map[string]struct{}
	// BetaOverride returns the provider's beta value and presence flag before
	// body sanitization. Callers keep it consistent with the beta value later
	// applied by ApplyOverrides so optional fields follow the final header.
	BetaOverride func() (string, bool)
	// CheckFastBeta checks eligibility when the selected Messages beta contains
	// fast mode. Its error ends request preparation; counting paths do not run
	// this check.
	CheckFastBeta func(context.Context) error
	// ApplyOverrides applies enabled provider headers after authentication and
	// platform defaults. Every builder requires this callback; configured
	// values receive final header precedence.
	ApplyOverrides func(http.Header)
	// Debug receives the completed ordinary Messages request, body, and
	// diagnostic fields when configured. Its inputs remain borrowed during this
	// synchronous callback.
	Debug func(*http.Request, []byte, map[string]string)
	// Capture receives completed ordinary Messages or count-token request bytes
	// after header overrides. The callback is optional and synchronous;
	// passthrough builders do not invoke it.
	Capture func(*http.Request, []byte)
}

// SetAPIKeyAuthHeader writes token with the selected Anthropic API-key scheme.
// bearer selects the raw authorization header; otherwise the raw x-api-key
// header is set. Callers remove conflicting authentication headers when
// replacing an existing credential set.
func SetAPIKeyAuthHeader(header http.Header, bearer bool, token string) {
	if bearer {
		SetHeaderRaw(header, "authorization", "Bearer "+token)
	} else {
		SetHeaderRaw(header, "x-api-key", token)
	}
}

// BuildRequest returns a Messages request and the exact prepared body backing
// its HTTP reader. Callers provide URL, FastMode, FilterSet, BetaOverride,
// CheckFastBeta, and ApplyOverrides for the paths that invoke them. Model and
// stream values select beta and mimic behavior; ctx is attached to the request.
//
// Cache read failure during OAuth fingerprint selection creates an identity
// from client headers and defaults; cache write failures remain diagnostic.
// Metadata rewriting stays independent of fingerprint header forwarding.
// Billing version and session alignment use the rewritten identity before
// capture. The selected provider beta override governs body sanitization, while
// later provider overrides take precedence over the assembled headers. Debug
// and Capture run after these preparation steps and before the caller
// dispatches the request.
//
// The request reader and its GetBody snapshots can share prepared input
// storage. Callers keep those bytes unchanged while the transport can still
// read them.
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

	// Provider identity has two consumers: forwarded headers and
	// metadata.user_id. Resolve their switches separately so disabling header
	// unification can still leave metadata rewriting active.
	var fingerprint *Fingerprint
	enableFP, enableMPT := true, false
	if options.Forwarding != nil {
		enableFP, enableMPT = options.Forwarding(ctx)
	}
	if options.OAuth && options.Fingerprint != nil {
		fp, err := options.Fingerprint.GetOrCreateFingerprint(ctx, options.ProviderID, clientHeaders)
		if err != nil {
			logger.LegacyPrintf("service.gateway", "Warning: failed to get fingerprint for provider %d: %v", options.ProviderID, err)
			// GetOrCreateFingerprint absorbs cache failures into identity
			// selection and returns nil error. This guard's client-header path
			// is inactive for those handled cache failures.
		} else {
			if enableFP {
				fingerprint = fp
			}

			// Metadata passthrough keeps the client identity string. Otherwise
			// provider account and cached device values select the ordinary
			// rewrite before optional provider-wide session masking.
			if !enableMPT {
				providerUUID := options.AccountUUID
				if providerUUID != "" && fp.ClientID != "" {
					// A failed metadata edit retains the current body for
					// request preparation. Successful nonempty output replaces
					// it before billing and session-header alignment read the
					// selected identity.
					if newBody, err := options.Fingerprint.RewriteUserIDWithMasking(ctx, body, options.ProviderID, options.MaskSession, providerUUID, fp.ClientID, fp.UserAgent); err == nil && len(newBody) > 0 {
						body = newBody
					}
				}
			}
		}
	}

	// CLI mimic headers can override a cached fingerprint later in preparation.
	// Select that effective UA now so system billing markers use the same
	// version.
	if billingUA := EffectiveBillingUserAgent(tokenType, mimicClaudeCode, fingerprint); billingUA != "" {
		body = SyncBillingHeaderVersion(body, billingUA)
	}

	// Body feature eligibility depends on the selected beta set. Resolve policy
	// and model betas before creating the reader that captures the sanitized
	// wire bytes.
	policyFilterSet := options.FilterSet(ctx)
	effectiveDropSet := MergeDropSets(policyFilterSet)
	finalBetaHeader, finalBetaShouldSet := ComputeFinalAnthropicBeta(
		tokenType, mimicClaudeCode, modelID, clientHeaders, body, effectiveDropSet, options.InjectAPIKeyBeta,
	)

	// Sanitize against the provider override because it becomes the outbound
	// beta header; using another value can leave fields the upstream schema
	// rejects.
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

	// OAuth and API-key paths select different credential headers. Raw setters
	// keep the package's chosen wire casing while the builder inserts upstream
	// credentials.
	if tokenType == "oauth" {
		SetHeaderRaw(req.Header, "authorization", "Bearer "+token)
	} else {
		SetAPIKeyAuthHeader(req.Header, options.APIKeyBearer, token)
	}

	// Mimic requests use a consistent CLI fingerprint. Forwarding client SDK,
	// UA, and session headers here could conflict with the injected identity.
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

	// The selected provider fingerprint replaces forwarded UA and SDK values so
	// these identity headers follow the provider cache selected for this
	// request.
	if fingerprint != nil {
		options.Fingerprint.ApplyFingerprint(req, fingerprint)
	}

	// Allowed client headers may omit protocol defaults. Fill missing JSON and
	// version headers after forwarding, retaining any supplied values in their
	// selected wire form.
	if GetHeaderRaw(req.Header, "content-type") == "" {
		SetHeaderRaw(req.Header, "content-type", "application/json")
	}
	if GetHeaderRaw(req.Header, "anthropic-version") == "" {
		SetHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}
	if tokenType == "oauth" {
		ApplyClaudeOAuthHeaderDefaults(req)
	}

	// OAuth mimic selects the package's CLI headers after fingerprint
	// application, matching the effective UA already used to synchronize system
	// billing markers.
	if tokenType == "oauth" && mimicClaudeCode {
		ApplyClaudeCodeMimicHeaders(req, reqStream)
	}

	// Allowed-header copying can leave another beta value in the map. Remove
	// its case forms before installing the value already used for body
	// sanitization.
	DeleteHeaderAllForms(req.Header, "anthropic-beta")
	if finalBetaShouldSet {
		SetHeaderRaw(req.Header, "anthropic-beta", finalBetaHeader)
	}

	// Metadata rewriting can change the session component. Align a supplied
	// session header with the parsed output identity so both locations describe
	// the same session.
	if sessionHeader := GetHeaderRaw(req.Header, "X-Claude-Code-Session-Id"); sessionHeader != "" {
		if uid := gjson.GetBytes(body, "metadata.user_id").String(); uid != "" {
			if parsed := ParseMetadataUserID(uid); parsed != nil {
				SetHeaderRaw(req.Header, "X-Claude-Code-Session-Id", parsed.SessionID)
			}
		}
	}

	// Provider header values are applied after forwarding, cached identity, and
	// platform defaults, giving enabled overrides the final values captured for
	// dispatch.
	options.ApplyOverrides(req.Header)

	if options.Debug != nil {
		options.Debug(req, body, map[string]string{"url": req.URL.String(), "token_type": tokenType, "mimic_claude_code": strconv.FormatBool(mimicClaudeCode), "fingerprint_applied": strconv.FormatBool(fingerprint != nil), "enable_fp": strconv.FormatBool(enableFP), "enable_mpt": strconv.FormatBool(enableMPT)})
	}
	if options.Capture != nil {
		options.Capture(req, body)
	}
	return req, body, nil
}
