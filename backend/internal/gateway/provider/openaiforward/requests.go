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

// RequestTargetOptions supplies credential type, default URLs, and target resolution callbacks.
type RequestTargetOptions struct {
	OAuthTarget, APIKey  bool
	DefaultURL, CodexURL string
	BaseURL              func() string
	Validate             func(string) (string, error)
	FromBase             func(string) string
	AppendSuffix         func(string) string
}

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

// BuildResponsesRequest records the endpoint, normalizes the body, and builds the upstream request.
func BuildResponsesRequest(ctx context.Context, body []byte, key string, target RequestTargetOptions, observe func(string), normalize func([]byte) []byte, options func(string) openai.ResponsesRequestOptions) (*http.Request, error) {
	address, err := target.Resolve()
	if err != nil {
		return nil, err
	}
	if parsed, e := url.Parse(address); e == nil {
		observe(parsed.Path)
	}
	body = normalize(body)
	return openai.BuildResponsesRequest(ctx, body, key, options(address))
}

// BuildPassthroughRequest resolves the target and normalizes the body before constructing a passthrough request.
func BuildPassthroughRequest(ctx context.Context, body []byte, target RequestTargetOptions, normalize func([]byte) []byte, options func(string) openai.PassthroughRequestOptions) (*http.Request, error) {
	address, err := target.Resolve()
	if err != nil {
		return nil, err
	}
	body = normalize(body)
	return openai.BuildPassthroughRequest(ctx, body, options(address))
}

// ResponsesEndpoint selects DeepSeek's unversioned path or the standard versioned Responses path.
func ResponsesEndpoint(platform, base string) string {
	if platform == capability.PlatformDeepseek {
		return httpclient.BuildOpenAIEndpointURL(base, "/responses")
	}
	return httpclient.BuildOpenAIEndpointURL(base, "/v1/responses")
}

// NormalizeCNResponsesBody removes server-side state from native CN Responses requests.
func NormalizeCNResponsesBody(native bool, body []byte) []byte {
	if !native {
		return body
	}
	return protocolopenai.StatelessResponsesRequest(body)
}
