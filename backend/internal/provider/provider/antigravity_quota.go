package provider

import (
	"context"
	"errors"
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// AntigravityQuotaOptions binds quota queries and error parsing to the supplied body limit and proxy lookup.
func AntigravityQuotaOptions(limit int64, resolveProxy func(context.Context, int64) (string, bool)) provider.AntigravityQuotaOptions {
	return provider.AntigravityQuotaOptions{
		NewClient:    func(proxy string) (provider.AntigravityQuotaClient, error) { return antigravity.NewClient(proxy) },
		ReadLimit:    func() int64 { return limit },
		ResolveProxy: resolveProxy,
		ForbiddenBody: func(err error) (string, bool) {
			if forbidden, ok := errors.AsType[*antigravity.ForbiddenError](err); ok {
				return forbidden.Body, true
			}
			return "", false
		},
		ClassifyForbidden: antigravity.ClassifyForbiddenType,
		ValidationURL:     antigravity.ExtractValidationURL,
		Warn:              slog.Warn,
	}
}
