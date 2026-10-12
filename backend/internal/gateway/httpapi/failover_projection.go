package httpapi

import (
	"net/http"
	"strings"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// CredentialFailoverClientResponse returns a client status and message for a classified credential failure.
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
	return http.StatusServiceUnavailable, forwardcore.GrokCredentialUnavailableClientMessage
}

// ProjectOpenAIFailoverError extracts classified failure details for HTTP error-rule processing.
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
