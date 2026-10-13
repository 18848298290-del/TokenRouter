package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A 403 classification can identify a quota-like concurrency response, an HTML
// denial or a provider account error. Platform and classification determine
// which health scope applies: Antigravity uses its classified account
// diagnostic, Kimi concurrency takes its temporary command first, and OpenAI/CN
// HTML observations leave this provider-health decision unhandled.
//
// Diagnostic construction prefers an adapter message, then a bounded body
// representation, then a supplied fallback. JSON bodies are compacted before
// bounded line conversion; adapter messages are trimmed at their edges and
// otherwise remain untruncated. Antigravity validation can append its
// verification URL; other classifications keep ordinary diagnostics. The
// selected health command owns policy, storage and logged outcomes, so handled
// results remain separate from confirmation of a persisted row.

// ForbiddenObservation holds upstream denial evidence and adapter
// classifications for provider health selection.
type ForbiddenObservation struct {
	Message string // classified upstream diagnostic
	// Body remains borrowed while diagnostic construction reads it. Keep its bytes
	// stable until the synchronous health call finishes formatting the selected
	// message.
	Body []byte
	// HTML supplies handleOpenAI403's provider-penalty exemption.
	// ApplyForbiddenObservation considers Antigravity and Kimi concurrency before
	// reaching that OpenAI/CN path.
	HTML bool
	// Kind and ValidationURL determine Antigravity's account diagnostic together.
	// A validation classification can append its URL; violation and unrecognized
	// kinds use their own ordinary message paths.
	Kind          string // Antigravity denial kind
	ValidationURL string // account verification URL
	// ConcurrentRequestLimited and ConcurrencyReason select Kimi's temporary
	// capacity command before HTML/default handling. The reason is passed to that
	// command for its stored diagnostic.
	ConcurrentRequestLimited bool   // Kimi concurrency classification
	ConcurrencyReason        string // temporary capacity diagnostic
}

// buildForbiddenErrorMessage joins a normalized prefix with the first usable
// diagnostic source. A trimmed nonblank adapter message wins and remains
// untruncated; otherwise TruncateLine limits a compact JSON or raw body to 512
// input bytes before escaping line breaks. An empty body selects fallback. The
// diagnostic prefix and escapes can increase output length, and a body cut can
// split UTF-8.
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
		// Compact valid JSON before bounded line conversion so whitespace consumes
		// less of the diagnostic budget. If compaction fails, retain the raw-body
		// path rather than lose the available evidence.
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

// ApplyForbiddenObservation chooses a health scope from the classified 403 and
// provider platform. Supply a usable service and record with stable observation
// data. Antigravity account handling runs first; Kimi concurrency selects its
// temporary command before other CN denial policy.
//
// OpenAI and remaining CN observations reach handleOpenAI403, where HTML can
// return false and non-HTML responses enter administrator cooldown/threshold
// handling. Other platforms receive an account-error diagnostic. Each true
// result denotes selected response handling even when the delegated health
// write fails, while the HTML false result lets the caller retain its separate
// request retry decision.
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
	msg := buildForbiddenErrorMessage(
		"Access forbidden (403):",
		observation.Message,
		observation.Body,
		"provider may be suspended or lack permissions",
	)
	s.ApplyAuthenticationFailure(ctx, provider, msg)
	return true
}

// handleOpenAI403 applies the OpenAI/CN HTML exemption before administrator 403
// policy. HTML observations are logged and return false; other observations
// build a diagnostic and delegate their handled result to ApplyForbidden.
func (s *HealthService) handleOpenAI403(ctx context.Context, provider *Record, observation ForbiddenObservation) (shouldDisable bool) {
	// The HTML exemption skips both shared 403 counting and provider writes. Keep
	// that omission local to health handling while the caller retains its own
	// response retry decision.
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

// handleAntigravity403 builds an account-error diagnostic from the supplied
// denial kind. Validation appends a nonempty verification URL, violation uses
// its own fallback, and other kinds use ordinary access-forbidden text.
// ApplyAuthenticationFailure owns persistence, and this method returns true
// after selecting that handling.
func (s *HealthService) handleAntigravity403(ctx context.Context, provider *Record, observation ForbiddenObservation) (shouldDisable bool) {
	fbType := observation.Kind

	switch fbType {
	case ForbiddenTypeValidation:
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
