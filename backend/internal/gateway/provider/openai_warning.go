package provider

import (
	"strings"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_warning.go detects cyber-policy warnings and carries their evidence
// alongside forwarding errors.
// Cyber-policy failures can appear as structured error events or as upstream
// warning text. Detection feeds both retry exclusion and policy recording,
// while the error wrapper keeps structured warning evidence alongside the
// underlying failure. Client message extraction sanitizes query secrets and
// limits its selected text before delivery.
//
// Warning wrapping copies the response bytes before attaching the warning to
// the underlying error, so later changes to the supplied byte slice cannot
// replace that evidence. Unwrap retains ordinary error-chain inspection.
// Structured policy events also retain available token counts for moderation
// recording; absent usage leaves those counts zero in the resulting mark.

// This interface assertion keeps the warning wrapper available to forwarding
// consumers that extract structured upstream warning evidence from an error.
var _ forwardcore.UpstreamWarningCarrier = (*openAIUpstreamWarningError)(nil)

// openAIUpstreamWarningError combines a cyber warning observation with its
// underlying error.
type openAIUpstreamWarningError struct {
	// warning is exposed by reference to forwarding readers. Its body is copied
	// by the wrapper constructor, and callers keep the returned observation immutable.
	warning *forwardcore.UpstreamWarning
	err     error // underlying forwarding error chain
}

// ParseOpenAICyberPolicyEvent detects cyber-policy evidence and captures a
// byte-limited body plus observed aggregate usage. Unrecognized payloads
// return nil; oversized bodies are reduced to a valid UTF-8 prefix, while
// shorter input bytes remain as supplied.
func ParseOpenAICyberPolicyEvent(payload []byte, upstreamStatus int, usage *openai.ForwardUsage) *moderationflow.Mark {
	hit, code, message := upstreamopenai.DetectOpenAICyberPolicy(payload)
	if !hit {
		return nil
	}
	// Bound the diagnostic payload, repairing UTF-8 when the byte cap truncates it;
	// input already within the cap is retained as supplied. Usage observations
	// remain separate integers used by the completion capture path.
	mark := &moderationflow.Mark{
		Code: code, Message: message,
		Body: logredact.TruncateUTF8(string(payload), 4096), UpstreamStatus: upstreamStatus,
	}
	if usage != nil {
		mark.UpstreamInTok = usage.InputTokens
		mark.UpstreamOutTok = usage.OutputTokens
	}
	return mark
}

// Error returns the underlying error message, or a generic warning label when
// unavailable.
func (e *openAIUpstreamWarningError) Error() string {
	if e == nil || e.err == nil {
		return "openai upstream warning"
	}
	return e.err.Error()
}

// OpenAIUpstreamWarning returns the wrapper's borrowed warning pointer or nil
// for a nil receiver. Consumers keep the warning and its body bytes stable
// while other error readers can access them.
func (e *openAIUpstreamWarningError) OpenAIUpstreamWarning() *forwardcore.UpstreamWarning {
	if e == nil {
		return nil
	}
	return e.warning
}

// Unwrap returns the underlying error for standard error-chain inspection, or
// nil when the wrapper receiver is absent. It does not alter the retained
// warning observation.
func (e *openAIUpstreamWarningError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// ExtractOpenAICyberWarningMessage selects sanitized client text from
// structured cyber evidence, supplied warning text, or extracted body text.
// Structured detection precedes recognized warning-text matches. If neither
// matches, nonblank warningText wins before an extracted fallback whose text
// does not start with an opening brace; absent usable text gets generic wording.
//
// Each selected value passes through TruncateLine with a 2048-input-byte cap.
// CR/LF escaping can make the returned string longer than that cap, and the
// byte cut can split UTF-8. Callers use this result as bounded captured evidence,
// rather than assuming a final encoded-response size or valid-rune guarantee.
func ExtractOpenAICyberWarningMessage(responseBody []byte, warningText string) string {
	if hit, _, message := upstreamopenai.DetectOpenAICyberPolicy(responseBody); hit && strings.TrimSpace(message) != "" {
		// The detected explanation uses the client evidence cap before CR/LF escapes
		// expand the captured prefix into a printable line.
		return logredact.TruncateLine([]byte(logredact.SanitizeUpstreamQueries(message)), 2048)
	}
	for _, candidate := range []string{
		strings.TrimSpace(warningText),
		strings.TrimSpace(moderation.ExtractCyberWarningText(responseBody)),
	} {
		if upstreamopenai.IsOpenAICyberWarningText(candidate) {
			// A recognized warning uses the same byte capture policy as structured
			// evidence; later newline escaping can expand its client text.
			return logredact.TruncateLine([]byte(logredact.SanitizeUpstreamQueries(candidate)), 2048)
		}
	}
	// Detection found no preferred cyber text. The supplied warning can still be
	// useful client context after sanitization; extracted JSON bodies are excluded below.
	if fallback := strings.TrimSpace(warningText); fallback != "" {
		// Unmatched supplied text is still client evidence. Bound its input bytes
		// before escaping line breaks, using the same capture cap as recognized text.
		return logredact.TruncateLine([]byte(logredact.SanitizeUpstreamQueries(fallback)), 2048)
	}
	if fallback := strings.TrimSpace(moderation.ExtractCyberWarningText(responseBody)); fallback != "" && !strings.HasPrefix(fallback, "{") {
		// This extracted fallback passed the opening-brace guard. Apply the client
		// byte cap before CR/LF escaping so this path follows the other evidence paths.
		return logredact.TruncateLine([]byte(logredact.SanitizeUpstreamQueries(fallback)), 2048)
	}
	return "OpenAI rejected this request because it may violate cyber safety policy."
}

// IsOpenAICyberWarningPayload recognizes warning text, structured cyber
// markers, or body text indicating cyber rejection. Empty bodies can still
// match supplied warning text, which is checked before body inspection.
func IsOpenAICyberWarningPayload(responseBody []byte, warningText string) bool {
	if upstreamopenai.IsOpenAICyberWarningText(warningText) {
		return true
	}
	if len(responseBody) == 0 {
		return false
	}
	if hit, _, _ := upstreamopenai.DetectOpenAICyberPolicy(responseBody); hit {
		return true
	}
	return upstreamopenai.IsOpenAICyberWarningText(moderation.ExtractCyberWarningText(responseBody)) ||
		upstreamopenai.IsOpenAICyberWarningText(string(responseBody))
}

// OpenAIUpstreamWarningIsCyber recognizes a cyber rejection from a warning's
// body or message. Nil warnings return false, allowing retries and
// cyber-recording callers to share the same detector.
func OpenAIUpstreamWarningIsCyber(warning *forwardcore.UpstreamWarning) bool {
	if warning == nil {
		return false
	}
	// WebSocket retries and cyber-policy records use the same rejection detector.
	return IsOpenAICyberWarningPayload(warning.ResponseBody, warning.Message)
}

// WrapOpenAIUpstreamWarningIfCyber attaches a warning with copied body bytes
// to a recognized cyber error. Nil errors remain nil, unrecognized errors
// return unchanged, and the wrapper retains the original error for
// errors.Is/As.
func WrapOpenAIUpstreamWarningIfCyber(statusCode int, responseBody []byte, message string, err error) error {
	if err == nil {
		return nil
	}
	warning := &forwardcore.UpstreamWarning{
		StatusCode:   statusCode,
		ResponseBody: append([]byte(nil), responseBody...),
		Message:      strings.TrimSpace(message),
	}
	if !OpenAIUpstreamWarningIsCyber(warning) {
		return err
	}
	return &openAIUpstreamWarningError{warning: warning, err: err}
}
