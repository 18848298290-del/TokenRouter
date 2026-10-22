package grokforward

import (
	"context"
	"fmt"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// composer.go describes images through an auxiliary Grok Responses request and
// returns the description usage.
// Composer turns an image into text through an auxiliary Responses exchange on
// a fixed vision model. The description and its token usage return to the main-
// request preparation step, while health observations and HTTP failure
// diagnostics still refer to the selected provider.

// DescribeImage sends one image-description request and returns its decoded
// text and usage. The supplied adapter already has the provider credential
// needed by Build, and this auxiliary request uses an empty cache identity with
// runtime route defaults disabled. ResponsesExecutor performs one exchange and
// closes the response after the custom reader finishes; response failures are
// observed before returning a generic, failover, or descriptive error.
func DescribeImage(ctx context.Context, p Ports, o Options, in Input, imageURL string, index int) (string, protocolopenai.ForwardUsage, error) {
	body, err := o.Codec.BuildGrokComposerImageDescriptionBody(imageURL, index)
	if err != nil {
		return "", protocolopenai.ForwardUsage{}, err
	}
	upstreamCtx, releaseUpstreamCtx := p.Detach(ctx)
	// An empty cache identity keeps the auxiliary image-description prompt outside
	// the main conversation identity; Build also disables runtime route defaults.
	upstreamReq, err := p.Build(upstreamCtx, body, "", false)
	// Release the detached-context handle after Build; the shared adapter
	// currently returns a no-op release, leaving the request context usable by
	// execution.
	releaseUpstreamCtx()
	if err != nil {
		return "", protocolopenai.ForwardUsage{}, fmt.Errorf("build grok composer image bridge request: %w", err)
	}
	p.ResolveProxy()
	var description string
	var usage protocolopenai.ForwardUsage
	target := &grok.ResponsesTarget{
		ProviderID:    in.ProviderID,
		Model:         ComposerVisionModel,
		Enter:         o.Enter,
		PassRawStream: true,
		Exchange: grok.ResponsesExchange{
			SingleExchange: true,
			Build:          func([]byte) (*http.Request, error) { return upstreamReq, nil },
			Do: func(req *http.Request) (*http.Response, error) {
				return p.Do(req)
			},
			ReadError: p.ReadError,
			AfterExchange: func(err error) error {
				if err != nil {
					return p.TransportError(ctx, err)
				}
				return nil
			},
		},
		BeforeResponse: func(resp *http.Response, _ []byte) (bool, error) {
			if resp.StatusCode >= 400 {
				respBody := p.ReadError(resp)
				upstreamMsg := p.ErrorMessage(respBody)
				if upstreamMsg == "" {
					upstreamMsg = fmt.Sprintf("xAI image bridge upstream returned status %d", resp.StatusCode)
				}
				decision := p.Health(ctx, resp.StatusCode, resp.Header, respBody, ComposerVisionModel, false)
				kind := "http_error"
				if decision.Failover {
					kind = "failover"
				}
				p.Observe(Notice{
					Platform:           in.Platform,
					ProviderID:         in.ProviderID,
					ProviderName:       in.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
					Kind:               kind,
					Message:            upstreamMsg,
				})
				if decision.Generic {
					return true, fmt.Errorf("grok composer image bridge upstream gateway error")
				}
				// Translate health-selected failover into the outer loop's typed failure so
				// auxiliary usage is not treated as a completed description.
				if kind == "failover" {
					retry := p.RetryMetadata(resp.StatusCode, respBody)
					return true, p.Failure(newFailure(resp, respBody, retry, decision.RetrySameProvider))
				}
				return true, fmt.Errorf("grok composer image bridge upstream error: %s", upstreamMsg)
			}
			p.ObserveSuccess(ctx, resp.Header, resp.StatusCode, ComposerVisionModel)
			return false, nil
		},
		ReadResponse: func(resp *http.Response, _ upstream.AttemptInput, _ upstream.OutputSink) (upstream.ResponsesObservation, error) {
			data, readErr := p.ReadBody(resp)
			if readErr != nil {
				return upstream.ResponsesObservation{}, fmt.Errorf("read grok composer image bridge response: %w", readErr)
			}
			var decodeErr error
			description, usage, decodeErr = grok.DecodeComposerDescription(data)
			return upstream.ResponsesObservation{Usage: &usage, HasUsage: p.HasTokens(&usage), Served: description != ""}, decodeErr
		},
	}
	_, err = (grok.ResponsesExecutor{}).Execute(upstreamCtx, upstream.AttemptInput{
		Protocol: protocol.ProtocolOpenAIResponses,
		Body:     body,
		Target:   target,
	}, nil)
	return description, usage, err
}
