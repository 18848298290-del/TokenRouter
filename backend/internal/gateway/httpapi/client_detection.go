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

var claudeCodeValidator = clientmeta.NewClaudeCodeValidator()

// ClientDetection contains the client identity result used by admission and platform policy checks.
type ClientDetection struct {
	ClaudeCode bool
	Version    string
}

// SetClaudeCodeClientContext stores client identity in the HTTP request context for later processing.
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
func DetectClaudeCodeRequest(c *gin.Context, body []byte, parsedReq *requeststate.ParsedRequest, probe bool) ClientDetection {
	if c == nil || c.Request == nil {
		return ClientDetection{}
	}
	ua := c.GetHeader("User-Agent")
	// Reject other user agents before decoding JSON.
	if !claudeCodeValidator.ValidateUserAgent(ua) {
		return ClientDetection{}
	}

	isClaudeCode := false
	if !strings.Contains(c.Request.URL.Path, "messages") {
		// Outside Messages routes, a matching user agent is sufficient for Claude Code detection.
		isClaudeCode = true
	} else {
		// For Messages, validate the body and headers after the user agent matches.
		bodyMap := ClaudeCodeBodyMapFromParsedRequest(parsedReq)
		if bodyMap == nil && len(body) > 0 {
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
			bodyMap["system"] = nil
		}
	}
	if parsedReq.MetadataUserID != "" {
		bodyMap["metadata"] = map[string]any{"user_id": parsedReq.MetadataUserID}
	}
	return bodyMap
}

// PrepareMessageClientContext resolves client, probe, and thinking state before routing and provider selection.
func PrepareMessageClientContext(c *gin.Context, body []byte, bounds func(context.Context) (string, string)) error {
	// Normalize stream in the identity-parsing copy; outbound forwarding uses the original body.
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
