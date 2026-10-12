package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// Claude Code format recognition begins with a CLI User-Agent, then applies
// route-specific evidence. Ordinary Messages requests need payload and header
// markers; other routes and the validator's auxiliary Messages cases can use
// the matching User-Agent. These requester-supplied values describe client format
// for routing restrictions and mimicry policy.
//
// Messages preparation obtains probe and thinking state from parsed input before
// provider selection. A compatibility copy coerces nonboolean stream values for
// that identity parse while outbound forwarding keeps the supplied body. Parsed
// request fields take precedence over re-decoding raw bytes, allowing callers
// that already parsed the request to reuse the same identity evidence.

// claudeCodeValidator supplies the stateless format recognizer reused by gateway
// request metadata checks before admission and platform-specific client policy.
var claudeCodeValidator = clientmeta.NewClaudeCodeValidator()

// ClientDetection contains the client identity result used by admission and platform policy checks.
type ClientDetection struct {
	// ClaudeCode records the format match, and Version is extracted only after
	// that match. Rejected or missing request data therefore leaves both fields at their zero values.
	ClaudeCode bool   // CLI format matched
	Version    string // extracted CLI version
}

// SetClaudeCodeClientContext stores client identity in the HTTP request context for later processing.
// Missing contexts or requests are ignored. The probe marker already in context
// participates in detection; a version is stored only for a recognized client
// with a nonempty extracted version. An existing version value remains in the
// context when this detection supplies no publishable version.
func SetClaudeCodeClientContext(c *gin.Context, body []byte, parsed *requeststate.ParsedRequest) {
	if c == nil || c.Request == nil {
		return
	}
	probe, _ := requeststate.IsMaxTokensOneHaikuRequestFromContext(c.Request.Context())
	result := DetectClaudeCodeRequest(c, body, parsed, probe)
	ctx := requeststate.SetClaudeCodeClient(c.Request.Context(), result.ClaudeCode)
	if result.ClaudeCode && result.Version != "" {
		ctx = requeststate.SetClaudeCodeVersion(ctx, result.Version)
	}
	c.Request = c.Request.WithContext(ctx)
}

// DetectClaudeCodeRequest detects Claude Code identity and version from request metadata.
// A nil context or request returns an empty detection, and an unmatched
// User-Agent prevents body inspection. Non-Messages paths accept the matching
// User-Agent immediately; Messages paths use the validator's payload and header rules.
//
// Existing parsed fields supply the Messages body map when available. Otherwise
// JSON decoding contributes any usable map and its error is left for protocol
// validation. The probe flag permits the validator's single-token Haiku exception.
// Version extraction follows successful recognition so a CLI-looking header
// that fails Messages validation cannot publish a positive identity with a version.
func DetectClaudeCodeRequest(c *gin.Context, body []byte, parsedReq *requeststate.ParsedRequest, probe bool) ClientDetection {
	if c == nil || c.Request == nil {
		return ClientDetection{}
	}
	ua := c.GetHeader("User-Agent")
	if !claudeCodeValidator.ValidateUserAgent(ua) {
		return ClientDetection{}
	}

	isClaudeCode := false
	if !strings.Contains(c.Request.URL.Path, "messages") {
		// Model lists and other non-Messages entry paths can omit a Messages
		// payload, so the matching CLI User-Agent supplies format identity on those routes.
		isClaudeCode = true
	} else {
		// For Messages, validate the body and headers after the user agent matches.
		bodyMap := ClaudeCodeBodyMapFromParsedRequest(parsedReq)
		if bodyMap == nil && len(body) > 0 {
			// Decode only when parsed fields are unavailable. The validator decides
			// recognition from the resulting map, while protocol handling owns structural JSON errors.
			_ = json.Unmarshal(body, &bodyMap)
		}
		isClaudeCode = claudeCodeValidator.Validate(clientmeta.ClaudeCodeValidationInput{Path: c.Request.URL.Path, UserAgent: ua, XApp: c.GetHeader("X-App"), AnthropicBeta: c.GetHeader("anthropic-beta"), AnthropicVersion: c.GetHeader("anthropic-version"), MaxTokensOneHaiku: probe}, bodyMap)
	}

	result := ClientDetection{ClaudeCode: isClaudeCode}
	if isClaudeCode {
		result.Version = claudeCodeValidator.ExtractVersion(ua)
	}
	return result
}

// ClaudeCodeBodyMapFromParsedRequest exposes the parsed fields needed by client validation.
// Nil input returns nil. A present but unavailable system value is represented
// as nil; model and nonempty metadata identity are copied into the result map.
// Any available system value is the value returned by the parsed request.
func ClaudeCodeBodyMapFromParsedRequest(parsedReq *requeststate.ParsedRequest) map[string]any {
	if parsedReq == nil {
		return nil
	}
	bodyMap := map[string]any{
		"model": parsedReq.Model,
	}
	if parsedReq.HasSystem {
		if system, ok := parsedReq.SystemValue(); ok {
			bodyMap["system"] = system
		} else {
			// Keep the supplied field present with an unusable value. Ordinary
			// Messages validation rejects that system value while model and metadata remain available.
			bodyMap["system"] = nil
		}
	}
	if parsedReq.MetadataUserID != "" {
		bodyMap["metadata"] = map[string]any{"user_id": parsedReq.MetadataUserID}
	}
	return bodyMap
}

// PrepareMessageClientContext resolves client, probe, and thinking state before routing and provider selection.
// Callers provide a request-bearing context and retain body for forwarding.
// Parse failures return before identity publication; recognized Claude Code
// clients are checked against bounds when the callback is present.
//
// A separate identity parse coerces nonboolean stream values while leaving the
// caller's outbound bytes intact. Probe state is installed before detection,
// and detected client, version, and thinking values are then published together
// before the optional version-range check. A range rejection returns an error
// after those observations are available for the caller's diagnostics.
func PrepareMessageClientContext(c *gin.Context, body []byte, bounds func(context.Context) (string, string)) error {
	// Coerce stream in the identity-parsing copy so client recognition can use
	// the strict parsed shape while outbound forwarding still receives the caller's supplied bytes.
	identityBody := body
	if value := gjson.GetBytes(body, "stream"); value.Exists() && value.Type != gjson.True && value.Type != gjson.False {
		var err error
		identityBody, err = sjson.SetBytes(body, "stream", value.Bool())
		if err != nil {
			return err
		}
	}
	parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(identityBody), "anthropic")
	if err != nil {
		return err
	}
	probe := clientmeta.IsHaikuProbe(parsed.Model, parsed.MaxTokens)
	ctx := requeststate.WithIsMaxTokensOneHaikuRequest(c.Request.Context(), probe)
	c.Request = c.Request.WithContext(ctx)
	detected := DetectClaudeCodeRequest(c, body, parsed, probe)
	ctx = requeststate.SetClaudeCodeClient(ctx, detected.ClaudeCode)
	ctx = requeststate.SetClaudeCodeVersion(ctx, detected.Version)
	ctx = requeststate.WithThinkingEnabled(ctx, parsed.ThinkingEnabled)
	c.Request = c.Request.WithContext(ctx)
	if detected.ClaudeCode && bounds != nil {
		minimum, maximum := bounds(ctx)
		if message := clientmeta.ClaudeVersionRejection(detected.Version, minimum, maximum); message != "" {
			return errors.New(message)
		}
	}
	return nil
}
