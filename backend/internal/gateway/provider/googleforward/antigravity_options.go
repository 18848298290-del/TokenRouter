package googleforward

import (
	"context"
	"io"
	"net/http"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

func (s *Antigravity) upstreamErrorBodyReadLimit() int64 {
	limit := int64(512 << 10)
	if s != nil && s.Options.Configured && s.Options.LogErrorBody && s.Options.LogErrorBodyMaxBytes > int(limit) {
		limit = int64(s.Options.LogErrorBodyMaxBytes)
	}
	return limit
}

func (s *Antigravity) readUpstreamErrorBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, s.upstreamErrorBodyReadLimit()))
	return body
}

// getLogConfig returns whether to log upstream error bodies
// and the maximum body length in bytes.
func (s *Antigravity) getLogConfig() (logBody bool, maxBytes int) {
	maxBytes = 2048 // Default byte limit.
	if !s.Options.Configured {
		return false, maxBytes
	}
	cfg := s.Options
	if cfg.LogErrorBodyMaxBytes > 0 {
		maxBytes = cfg.LogErrorBodyMaxBytes
	}
	return cfg.LogErrorBody, maxBytes
}

// getUpstreamErrorDetail returns the configured, truncated error-body detail for logging.
func (s *Antigravity) getUpstreamErrorDetail(body []byte) string {
	logBody, maxBytes := s.getLogConfig()
	if !logBody {
		return ""
	}
	return logredact.TruncateUTF8(string(body), maxBytes)
}

// getMappedModel resolves the upstream model.
// Provider mappings, including wildcards, take precedence over the default mapping.
func (s *Antigravity) getMappedModel(provider *gatewayprovider.ExecutionProvider, requestedModel string) string {
	return provideradapter.MapAntigravityModel(gatewayprovider.ExecutionRecord(provider), requestedModel)
}

func resolveAntigravityProjectID(provider *gatewayprovider.ExecutionProvider) (string, error) {
	return providercore.ResolveAntigravityProjectID(gatewayprovider.ExecutionRecord(provider), antigravity.ErrProjectIDRequired)
}

func (s *Antigravity) getClaudeTransformOptions(ctx context.Context) antigravity.TransformOptions {
	opts := antigravity.DefaultTransformOptions()
	if s.Gateway == nil {
		return opts
	}
	opts.EnableIdentityPatch = s.Gateway.IsIdentityPatchEnabled(ctx)
	opts.IdentityPatch = s.Gateway.GetIdentityPatchPrompt(ctx)
	return opts
}
