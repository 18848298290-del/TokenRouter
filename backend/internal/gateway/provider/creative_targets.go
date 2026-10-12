package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativeprovider "github.com/TokenFlux/TokenRouter/internal/creative/provider"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
	geminicli "github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// CreativeTargets holds task dependencies and binds an execution target after each provider selection.
type CreativeTargets struct {
	Requests interface {
		ImagesURL(*ExecutionProvider, string) (string, error)
		ValidateBaseURL(string) (string, error)
		TLSProfile(*ExecutionProvider, ...egress.TLSFingerprintRouterMatchResult) *tlsfingerprint.Profile
	}
	Credentials  *providercore.OpenAIExecutionCredentials
	Identity     *ExecutionAgentIdentity
	Transport    httpclient.UpstreamTransport
	Routes       GrokRoutes
	GeminiTokens *providercore.GeminiTokenSource
	Enter        func() (func(), error)
}

// ForProvider binds execution functions that read credentials and send requests when called.
func (s *CreativeTargets) ForProvider(provider *ExecutionProvider) *creativeprovider.Target {
	target := &creativeprovider.Target{}
	if s == nil {
		return target
	}
	token := func(ctx context.Context) (string, error) {
		value, _, err := s.Credentials.Resolve(ctx, ExecutionRecord(provider))
		return value, err
	}
	// Creative requires image bytes, so URL downloads are independent of optional API image backfill.
	imageDownload := openai.ImageBackfillOptions{
		ValidateURL: func(raw string) (string, error) {
			return s.Requests.ValidateBaseURL(raw)
		},
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Transport.Do(req, creativeTargetProxyURL(provider), provider.Record.ID, provider.Record.Concurrency)
		},
	}
	target.OpenAI = &creativeprovider.OpenAIOptions{
		FetchImage: imageDownload.FetchBase64,
		Token:      token,
		URL: func(endpoint string) (string, error) {
			targetURL, err := s.Requests.ImagesURL(provider, endpoint)
			if err != nil {
				return "", creative.CreativeNonRetryableError("creative openai base url invalid: %s", err.Error())
			}
			return targetURL, nil
		},
		Prepare: func(req *http.Request) *http.Request {
			return req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
		},
		AuthHeaders: func(ctx context.Context, token string) (http.Header, error) {
			return s.Identity.Headers(ctx, provider, token)
		},
		ApplyHeaders: BindExecutionHeaders(provider),
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Transport.DoWithTLS(req, creativeTargetProxyURL(provider), provider.Record.ID, provider.Record.Concurrency, s.Requests.TLSProfile(provider))
		},
	}
	target.Grok = &creativeprovider.GrokOptions{
		OAuth: provider.View().IsGrokOAuth(),
		Token: token,
		URL: func(endpoint grok.GrokMediaEndpoint) (string, error) {
			return s.Routes.Media(provider, endpoint, "")
		},
		Prepare: func(req *http.Request) *http.Request {
			return req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileGrok))
		},
		ApplyHeaders: BindExecutionHeaders(provider),
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Transport.Do(req, creativeTargetProxyURL(provider), provider.Record.ID, provider.Record.Concurrency)
		},
	}
	target.Gemini = func(model string) gemininative.ImageOptions {
		options := gemininative.ImageOptions{
			Mode:                  gemininative.CredentialMode(provider.Record.Type),
			Model:                 model,
			ProjectID:             provider.View().GetCredential("project_id"),
			APIKey:                func() string { return provider.View().GetCredential("api_key") },
			BaseURL:               func() string { return provider.View().GetGeminiBaseURL(geminicli.AIStudioBaseURL) },
			ValidateURL:           s.Requests.ValidateBaseURL,
			ValidateGeminiBaseURL: s.validateGeminiBaseURL,
			VertexURL: func() (string, error) {
				return vertex.BuildVertexGeminiURL(ExecutionProtocolRecord(provider).VertexProjectID(vertex.ServiceAccountProjectID), ExecutionProtocolRecord(provider).VertexLocation(model), model, "generateContent", false)
			},
			ApplyHeaders: BindExecutionHeaders(provider),
			Do: func(req *http.Request) (*http.Response, error) {
				return s.Transport.Do(req, creativeTargetProxyURL(provider), provider.Record.ID, provider.Record.Concurrency)
			},
			HTTPError:    func(status int, message string) error { return creative.CreativeHTTPStatusError(status, message) },
			Invalid:      func(format string, args ...any) error { return creative.CreativeNonRetryableError(format, args...) },
			ErrorMessage: upstream.ExtractErrorMessage,
			Enter:        s.Enter,
		}
		if s.GeminiTokens != nil {
			options.Token = func(ctx context.Context) (string, error) {
				return ExecutionToken(ctx, s.GeminiTokens, provider)
			}
		}
		return options
	}
	return target
}

// validateGeminiBaseURL rejects invalid or empty target addresses.
func (s *CreativeTargets) validateGeminiBaseURL(raw string) (string, error) {
	validated, err := s.Requests.ValidateBaseURL(raw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(validated) == "" {
		return "", errors.New("gemini base url is empty")
	}
	return validated, nil
}

func creativeTargetProxyURL(value *ExecutionProvider) string {
	if value == nil || value.Record.ProxyID == nil || value.Record.Proxy == nil {
		return ""
	}
	return value.Record.Proxy.URL()
}
