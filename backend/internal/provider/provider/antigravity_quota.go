package provider

import (
	"context"
	"errors"
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// antigravity_quota.go supplies upstream clients and forbidden-response
// classifiers to provider quota queries. Antigravity quota responses can
// represent verification requirements or policy violations as well as
// remaining model capacity. The options connect the provider quota reader to
// bounded upstream queries and typed forbidden errors, with platform parsers
// supplying the corresponding display category and URL.

// AntigravityQuotaOptions supplies upstream quota clients and
// forbidden-response parsing.
// The returned ReadLimit callback captures limit exactly, while ResolveProxy
// retains the supplied resolver. Wrapped ForbiddenError values expose their body
// to the provider's quota-state display.
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
