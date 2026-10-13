package openaiforward

import (
	"context"
	"net/http"
	"net/url"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Responses endpoint selection depends on credential type and the provider's
// protocol-specific base URL. OAuth targets use the Codex address, while API
// Key targets validate custom bases before endpoint suffix construction; native
// CN bodies receive best-effort stateless patches before ordinary or passthrough
// request builders add headers.

// RequestTargetOptions supplies credential type, default URLs, and target resolution callbacks.
type RequestTargetOptions struct {
	// OAuthTarget chooses the Codex URL before APIKey custom-base selection. When
	// both flags are set, Resolve uses the OAuth target and skips BaseURL
	// validation.
	OAuthTarget, APIKey  bool   // OAuthTarget selects Codex; APIKey allows a custom base
	DefaultURL, CodexURL string // DefaultURL is the ordinary endpoint; CodexURL is the OAuth endpoint.
	// BaseURL supplies an API Key custom base; a blank value retains DefaultURL
	// and avoids validation and FromBase.
	BaseURL func() string
	// Validate checks a nonblank API Key base before endpoint construction; any
	// error stops Resolve before AppendSuffix runs.
	Validate func(string) (string, error)
	FromBase func(string) string // validated base endpoint builder
	// AppendSuffix receives every selected endpoint after credential-dependent
	// base resolution; callers provide it for defaults as well as custom targets.
	AppendSuffix func(string) string
}

// Resolve returns the credential-selected Responses URL with its request
// suffix. OAuth takes precedence over API Key selection; nonblank API Key bases
// are validated, and all selected targets require AppendSuffix.
func (o RequestTargetOptions) Resolve() (string, error) {
	target := o.DefaultURL
	if o.OAuthTarget {
		target = o.CodexURL
	} else if o.APIKey {
		if base := o.BaseURL(); base != "" {
			validated, err := o.Validate(base)
			if err != nil {
				return "", err
			}
			target = o.FromBase(validated)
		}
	}
	return o.AppendSuffix(target), nil
}

// BuildResponsesRequest resolves the target, records its parsable path,
// normalizes body bytes, and delegates request construction. Callers provide
// synchronous observation, normalization, and option callbacks; resolution
// failures occur before any of those preparation effects.
func BuildResponsesRequest(ctx context.Context, body []byte, key string, target RequestTargetOptions, observe func(string), normalize func([]byte) []byte, options func(string) openai.ResponsesRequestOptions) (*http.Request, error) {
	address, err := target.Resolve()
	if err != nil {
		return nil, err
	}
	// Skip endpoint observation when URL parsing fails; request construction still
	// receives that address and can return its own validation failure.
	if parsed, e := url.Parse(address); e == nil {
		observe(parsed.Path)
	}
	body = normalize(body)
	return openai.BuildResponsesRequest(ctx, body, key, options(address))
}

// BuildPassthroughRequest resolves the target and normalizes the body before
// delegating passthrough request construction. Callers provide normalization
// and option callbacks for the resolved address; target errors return before
// either callback runs.
func BuildPassthroughRequest(ctx context.Context, body []byte, target RequestTargetOptions, normalize func([]byte) []byte, options func(string) openai.PassthroughRequestOptions) (*http.Request, error) {
	address, err := target.Resolve()
	if err != nil {
		return nil, err
	}
	body = normalize(body)
	return openai.BuildPassthroughRequest(ctx, body, options(address))
}

// ResponsesEndpoint selects the platform's Responses path from the supplied
// base URL. DeepSeek uses an unversioned suffix; the endpoint joiner handles a
// version already present in the base.
func ResponsesEndpoint(platform, base string) string {
	if platform == capability.PlatformDeepseek {
		return httpclient.BuildOpenAIEndpointURL(base, "/responses")
	}
	return httpclient.BuildOpenAIEndpointURL(base, "/v1/responses")
}

// NormalizeCNResponsesBody attempts stateless preparation for native CN
// execution. A failed storage edit returns supplied bytes, and a failed
// continuation deletion keeps the completed storage edit. Other routes borrow
// the supplied slice, so callers retain stable bytes while forwarding.
func NormalizeCNResponsesBody(native bool, body []byte) []byte {
	// The native-CN marker scopes removal of server-side continuation state; other
	// routes keep their supplied payload for their own protocol preparation.
	if !native {
		return body
	}
	return protocolopenai.StatelessResponsesRequest(body)
}
