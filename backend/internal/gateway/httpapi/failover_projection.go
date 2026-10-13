package httpapi

import (
	"net/http"
	"strings"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// A failed attempt carries upstream evidence and classified reasons used by
// final HTTP error output. Presentation derives safe credential responses and
// protocol-specific flags from that evidence, while response headers and bytes
// remain shared with the source failure.

// CredentialFailoverClientResponse chooses a safe client response for a
// classified credential failure. OpenAI access-state messages retain their
// supplied text and positive status, defaulting to 503; Antigravity rejection
// uses 502, and remaining cases use the shared Grok-unavailable response.
func CredentialFailoverClientResponse(failoverErr *forwardcore.UpstreamFailoverError) (int, string) {
	if failoverErr != nil && failoverErr.Reason == forwardcore.OpenAIUpstreamAccessStateReason && strings.TrimSpace(failoverErr.ClientMessage) != "" {
		status := failoverErr.ClientStatusCode
		if status <= 0 {
			status = http.StatusServiceUnavailable
		}
		return status, failoverErr.ClientMessage
	}
	if failoverErr != nil && failoverErr.Reason == forwardcore.AntigravityCredentialRejectedReason {
		return http.StatusBadGateway, forwardcore.AntigravityCredentialRejectedClientMessage
	}
	// Unrecognized credential reasons use the shared unavailable response, keeping
	// provider-specific authentication diagnostics out of the default client
	// message.
	return http.StatusServiceUnavailable, forwardcore.GrokCredentialUnavailableClientMessage
}

// ProjectOpenAIFailoverError extracts failure classifications for final HTTP
// error presentation. Nil input returns nil. Headers and body retain the source
// error storage, so consumers read them as attempt evidence; credential, size,
// continuation and warning flags come from their owning classifiers.
func ProjectOpenAIFailoverError(err *forwardcore.UpstreamFailoverError) *OpenAIFailoverError {
	if err == nil {
		return nil
	}
	credentialStatus, credentialMessage := CredentialFailoverClientResponse(err)
	return &OpenAIFailoverError{
		Status:                  err.StatusCode,
		ClientStatus:            err.ClientStatusCode,
		ClientMessage:           err.ClientMessage,
		CredentialStatus:        credentialStatus,
		CredentialMessage:       credentialMessage,
		Headers:                 err.ResponseHeaders,
		Body:                    err.ResponseBody,
		TooLarge:                gatewayprovider.IsOpenAIRequestBodyTooLarge(err),
		TooLargeMessage:         forwardcore.OpenAIRequestBodyTooLargeClientMessage,
		ContinuationUnsupported: err.Reason == forwardcore.OpenAIHTTPContinuationUnsupportedReason,
		Credential:              err.IsCredentialFailure(),
		CapacityShed:            gatewayprovider.IsOpenAICapacityShed(err),
		SilentRefusal:           forwardcore.IsOpenAISilentRefusalErrorBody(err.ResponseBody),
		SilentMessage:           forwardcore.OpenAISilentRefusalClientMessage(),
		CyberWarning:            gatewayprovider.IsOpenAICyberWarningPayload(err.ResponseBody, ""),
		CyberMessage:            gatewayprovider.ExtractOpenAICyberWarningMessage(err.ResponseBody, ""),
		UpstreamMessage:         upstream.ExtractErrorMessage(err.ResponseBody),
	}
}
