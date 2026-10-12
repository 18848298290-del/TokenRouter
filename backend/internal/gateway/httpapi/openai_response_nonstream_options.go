package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	responseprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func (p *OpenAIResponseOutput) failedResponseTerminal(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, mappedModel string, terminalPayload []byte, msg string) error {
	policyStatus, decision := p.ApplyStreamFailurePolicy(
		ctx, provider, mappedModel, resp.Header, terminalPayload, msg,
	)
	if decision.ShouldReturnGenericError() {
		MarkResponseCommitted(c)
		writeOpenAIPassthroughErrorEnvelope(c, http.StatusInternalServerError, resp.Header, "Upstream gateway error")
		return fmt.Errorf("upstream compact response failed: status=%d (not in custom error codes)", policyStatus)
	}
	if !IsResponseCommitted(c) && decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus, openai.OpenAIStreamFailedEventShouldFailover(terminalPayload, msg)) {
		MarkOpenAIResponseFailureEffects(c, policyStatus, decision.StopScheduling)
		return p.NewStreamPolicyFailure(
			c, provider, false, strings.TrimSpace(resp.Header.Get("x-request-id")), resp.Header,
			policyStatus, terminalPayload, msg,
			gatewayprovider.OpenAIStreamFailureRetryable(provider, terminalPayload, msg),
		)
	}
	err := p.ProtocolError(resp, c, msg)
	return gatewayprovider.WrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, terminalPayload, msg, err)
}

func (p *OpenAIResponseOutput) NonStreamOptions(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider) openai.NonStreamOptions {
	return openai.NonStreamOptions{
		OAuthProvider:       provider != nil && provider.Record.Type == capability.ProviderTypeOAuth,
		GrokCompact:         provider != nil && provider.View().IsGrok() && IsOpenAIResponsesCompactPath(c),
		PreserveContentType: p.Options.Configured && !p.Options.ResponseHeadersEnabled,
		ReadBody: func(reader io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(reader, p.Options.ReadLimit, c, OpenAIResponseTooLarge)
		},
		ObserveTier:        func(body []byte) { ObserveOpenAIServiceTierInContext(c, body, "response.completed") },
		ObserveSSE:         func(body string) { ObserveOpenAISSEBody(c, body) },
		ConvertCompact:     (grok.BodyCodec{NewID: uuid.NewString}).ConvertGrokResponseToOpenAICompact,
		RestoreClientTools: func(body []byte) ([]byte, error) { return RestoreGrokResponsesClientToolPayload(c, body) },
		RestoreOpenAITools: func(body []byte) ([]byte, error) { return RestoreOpenAIResponsesClientToolPayload(c, body) },
		RestoreNamespace:   func(body []byte) ([]byte, error) { return RestoreOpenAIResponsesNamespacePayload(c, body) },
		RestoreToolNames:   func(body []byte) []byte { return RestoreCodexToolNamesFromContext(c, body) },
		CorrectToolCalls:   p.Corrector.CorrectResponseBody,
		ResponseHeaders: func(output, input http.Header) {
			egressadapter.WriteFilteredHeaders(output, input, p.Headers)
			p.Turns.Relay(c, provider, input)
		},
		WriteCompactBridge: func(status int, body []byte) bool {
			return WriteOpenAICompactSSEBridge(c, status, body, MarkOpsStreamError)
		},
		CountJSONSearch: grok.CountGrokNativeSearchCallsFromJSONBytes,
		CountSSESearch:  grok.CountGrokNativeSearchCallsFromSSEBody,
		ExtractError:    openai.ExtractOpenAISSEErrorMessage,
		CompactFallback: func(body []byte, message string) error { return NewOpenAICompactFailure(c, body, message) },
		TerminalFailover: func(resp *http.Response, event string, body []byte, message, model string) error {
			//nolint:revive // Returning the concrete pointer directly would wrap a nil pointer in a non-nil error.
			if failure := p.nonStreamingTerminalFailure(c, resp, provider, false, event, body, message, model); failure != nil {
				return failure
			}
			return nil
		},
		ProtocolError: func(resp *http.Response, message string) error {
			return p.ProtocolError(resp, c, message)
		},
		FailedTerminal: func(resp *http.Response, model string, body []byte, message string) error {
			return p.failedResponseTerminal(ctx, c, provider, resp, model, body, message)
		},
		SupplementCompaction: func(body []byte, stream string) []byte {
			return responseprotocol.SupplementCompactionItemFromSSE(IsOpenAIResponsesCompactPath(c), body, stream)
		},
	}
}
