package usageview

import infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"

// Usage-query callers need a stable reason when credentials, response content,
// or transport operations fail. Shared application errors keep provider-key
// failures separate from wallet-token failures and carry status categories
// that administrator query handlers can return consistently.

// These errors classify adapter, credential, response, and transport failures
// before usage-query orchestration returns them to administrator clients.
var (
	// ErrUpstreamUsageUnsupported indicates a missing or unsupported query adapter
	// or endpoint.
	ErrUpstreamUsageUnsupported = infraerrors.New(infraerrors.Category(422),
		"UPSTREAM_USAGE_ADAPTER_UNSUPPORTED", "upstream usage adapter is unsupported",
	)
	// ErrUpstreamUsageAuthFailed indicates rejection of the provider key used for
	// the query.
	ErrUpstreamUsageAuthFailed = infraerrors.New(infraerrors.CategoryBadGateway,
		"UPSTREAM_USAGE_AUTH_FAILED", "upstream rejected the provider API key",
	)
	// ErrUpstreamUsageWalletUnavailable indicates that the selected query cannot
	// obtain a wallet balance.
	ErrUpstreamUsageWalletUnavailable = infraerrors.New(infraerrors.CategoryBadGateway,
		"UPSTREAM_USAGE_WALLET_UNAVAILABLE", "upstream wallet balance is unavailable",
	)
	// ErrUpstreamUsageWalletAuthFailed indicates rejection of the separate wallet
	// access token.
	ErrUpstreamUsageWalletAuthFailed = infraerrors.New(infraerrors.CategoryBadGateway,
		"UPSTREAM_USAGE_WALLET_AUTH_FAILED", "upstream rejected the wallet access token",
	)
	// ErrUpstreamUsageRateLimited indicates a rate-limited upstream usage-query
	// response.
	ErrUpstreamUsageRateLimited = infraerrors.ServiceUnavailable(
		"UPSTREAM_USAGE_RATE_LIMITED", "upstream usage query was rate limited",
	)
	// ErrUpstreamUsageTimeout indicates a usage-query operation or caller context
	// deadline.
	ErrUpstreamUsageTimeout = infraerrors.GatewayTimeout(
		"UPSTREAM_USAGE_TIMEOUT", "upstream usage query timed out",
	)
	// ErrUpstreamUsageInvalidResponse indicates missing, oversized, or invalid
	// upstream usage response data.
	ErrUpstreamUsageInvalidResponse = infraerrors.New(infraerrors.CategoryBadGateway,
		"UPSTREAM_USAGE_INVALID_RESPONSE", "upstream returned an invalid usage response",
	)
	// ErrUpstreamUsageRequestFailed indicates a query transport or read failure
	// outside deadline classification.
	ErrUpstreamUsageRequestFailed = infraerrors.New(infraerrors.CategoryBadGateway,
		"UPSTREAM_USAGE_REQUEST_FAILED", "upstream usage request failed",
	)
	// ErrUpstreamUsageConfigInvalid indicates invalid query configuration or
	// request header input.
	ErrUpstreamUsageConfigInvalid = infraerrors.BadRequest(
		"UPSTREAM_USAGE_CONFIG_INVALID", "upstream usage query configuration is invalid",
	)
)
