package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ForbiddenObservation carries the upstream 403 classification used by provider health rules.
type ForbiddenObservation struct {
	Message                  string
	Body                     []byte
	HTML                     bool
	Kind                     string
	ValidationURL            string
	ConcurrentRequestLimited bool
	ConcurrencyReason        string
}

func buildForbiddenErrorMessage(prefix string, upstreamMsg string, responseBody []byte, fallback string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix != "" && !strings.HasSuffix(prefix, " ") {
		prefix += " "
	}

	if msg := strings.TrimSpace(upstreamMsg); msg != "" {
		return prefix + msg
	}

	rawBody := bytes.TrimSpace(responseBody)
	if len(rawBody) > 0 {
		if json.Valid(rawBody) {
			var compact bytes.Buffer
			if err := json.Compact(&compact, rawBody); err == nil {
				return prefix + logredact.TruncateLine(compact.Bytes(), 512)
			}
		}
		return prefix + logredact.TruncateLine(rawBody, 512)
	}

	return prefix + fallback
}

// ApplyForbiddenObservation applies platform-specific health rules for HTTP 403 responses.
// Antigravity failures become account errors. OpenAI and CN providers use HTML exemptions
// and configured cooldown thresholds; Kimi concurrency failures use a temporary pause.
// Remaining platforms are marked as errored.
func (s *HealthService) ApplyForbiddenObservation(ctx context.Context, provider *Record, observation ForbiddenObservation) (shouldDisable bool) {
	if provider.Platform == capability.PlatformAntigravity {
		return s.handleAntigravity403(ctx, provider, observation)
	}
	// Kimi reports concurrency limits as 403. Use a temporary pause and keep the failover signal
	// so repeated capacity failures do not accumulate toward an account-error threshold.
	if provider.Platform == capability.PlatformKimi && observation.ConcurrentRequestLimited {
		s.ApplyCNConcurrencyLimit(ctx, provider, observation.ConcurrencyReason)
		return true
	}
	if provider.Platform == capability.PlatformOpenAI || provider.IsCNProvider() {
		return s.handleOpenAI403(ctx, provider, observation)
	}
	// Remaining platforms treat the failure as an account error.
	msg := buildForbiddenErrorMessage(
		"Access forbidden (403):",
		observation.Message,
		observation.Body,
		"provider may be suspended or lack permissions",
	)
	s.ApplyAuthenticationFailure(ctx, provider, msg)
	return true
}

func (s *HealthService) handleOpenAI403(ctx context.Context, provider *Record, observation ForbiddenObservation) (shouldDisable bool) {
	// An HTML 403 may come from a proxy or CDN before the request reaches the API.
	// Treating it as a credential failure could penalize every provider tried during failover.
	// Leave provider state unchanged here; the caller controls request retry behavior.
	if observation.HTML {
		s.options.Warn(
			"openai_403_html_body_skips_provider_penalty",
			"provider_id", provider.ID,
			"upstream_message", observation.Message,
		)
		return false
	}

	msg := buildForbiddenErrorMessage(
		"Access forbidden (403):",
		observation.Message,
		observation.Body,
		"provider may be suspended or lack permissions",
	)
	return s.ApplyForbidden(ctx, provider, msg)
}

// handleAntigravity403 marks the provider as errored with a classification-specific message.
// Validation failures include a verification URL when available so an administrator can act.
func (s *HealthService) handleAntigravity403(ctx context.Context, provider *Record, observation ForbiddenObservation) (shouldDisable bool) {
	fbType := observation.Kind

	switch fbType {
	case ForbiddenTypeValidation:
		// Google verification requires manual intervention before recovery.
		msg := buildForbiddenErrorMessage(
			"Validation required (403):",
			observation.Message,
			observation.Body,
			"provider needs Google verification",
		)
		if validationURL := observation.ValidationURL; validationURL != "" {
			msg += " | validation_url: " + validationURL
		}
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true

	case ForbiddenTypeViolation:
		// An account violation requires manual intervention before recovery.
		msg := buildForbiddenErrorMessage(
			"Provider violation (403):",
			observation.Message,
			observation.Body,
			"terms of service violation",
		)
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true

	default:
		msg := buildForbiddenErrorMessage(
			"Access forbidden (403):",
			observation.Message,
			observation.Body,
			"provider may be suspended or lack permissions",
		)
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	}
}
