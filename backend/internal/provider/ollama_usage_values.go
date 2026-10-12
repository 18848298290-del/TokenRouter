package provider

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

const (
	OllamaCloudUsageStatusOK           = "ok"
	OllamaCloudUsageStatusUnauthorized = "unauthorized"
	OllamaCloudUsageStatusFailed       = "failed"

	OllamaCloudUsageSessionExtraKey     = "ollama_cloud_usage_session"
	OllamaCloudUsageAutoRefreshExtraKey = "ollama_cloud_usage_auto_refresh"
	OllamaCloudUsageMinFetchInterval    = 15 * time.Minute
)

var (
	ErrOllamaCloudUsageUnavailable = apperror.ServiceUnavailable(
		"OLLAMA_CLOUD_USAGE_UNAVAILABLE", "Ollama Cloud usage is unavailable",
	)
	ErrOllamaCloudUsageProviderInvalid = apperror.BadRequest(
		"OLLAMA_CLOUD_USAGE_PROVIDER_INVALID", "provider must be an OpenAI or Anthropic API key provider using https://ollama.com",
	)
	ErrOllamaCloudUsageSessionRequired = apperror.BadRequest(
		"OLLAMA_CLOUD_USAGE_SESSION_REQUIRED", "an Ollama web session must be configured first",
	)
	ErrOllamaCloudUsageEncryptionKey = apperror.BadRequest(
		"OLLAMA_CLOUD_USAGE_ENCRYPTION_KEY_NOT_CONFIGURED", "cannot store an Ollama web session without a fixed TOTP_ENCRYPTION_KEY",
	)
	ErrOllamaCloudUsageIdentityChanged = apperror.Conflict(
		"OLLAMA_CLOUD_USAGE_IDENTITY_CHANGED", "provider identity or Ollama web session changed during refresh; retry",
	)
	ErrOllamaCloudUsageRefreshRateLimited = apperror.TooManyRequests(
		"OLLAMA_CLOUD_USAGE_REFRESH_RATE_LIMITED", "Ollama Cloud usage can be refreshed manually once every 30 seconds",
	)
)

// OllamaUsageFetchInput contains outbound usage-query parameters.
type OllamaUsageFetchInput struct {
	ProviderID  int64
	Concurrency int
	ProxyURL    string `json:"-"`
	Cookie      string `json:"-"`
	ObservedAt  time.Time
}

type OllamaUsageObservation = usageview.OllamaUsageObservation

// OllamaCloudUsageSettings controls optional activity-triggered refreshes.
//
// IntervalMinutes caps waiting when ongoing requests keep postponing debounce.
// DebounceMinutes is the quiet period after the group's latest request.
type OllamaCloudUsageSettings struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"` // Maximum wait during continuous activity, in minutes.
	DebounceMinutes int  `json:"debounce_minutes"` // Quiet period after the latest request, in minutes.
}

type OllamaCloudUsageData = usageview.OllamaCloudUsageData

// OllamaCloudUsageSnapshot is a usage observation stored in provider Extra.
// On success, scheduling uses activity, debounce, and the configured maximum wait, independent of
// NextRefreshAt.
// On failure, NextRefreshAt is the retry/backoff floor; scheduling uses the later of it and the activity-based
// due time.
type OllamaCloudUsageSnapshot struct {
	Status        string                `json:"status"`
	Data          *OllamaCloudUsageData `json:"data,omitempty"`
	FetchedAt     *time.Time            `json:"fetched_at,omitempty"`
	LastAttemptAt time.Time             `json:"last_attempt_at"`
	NextRefreshAt time.Time             `json:"next_refresh_at"`
	FailureCount  int                   `json:"failure_count,omitempty"`
	HTTPStatus    int                   `json:"http_status,omitempty"`
	LastError     string                `json:"last_error,omitempty"`
}

// OllamaCloudUsageState contains the usage state exposed to administrators.
type OllamaCloudUsageState struct {
	ProviderID              int64                     `json:"provider_id"`
	Eligible                bool                      `json:"eligible"`
	Configured              bool                      `json:"configured"`
	AutoRefreshEnabled      bool                      `json:"auto_refresh_enabled"`
	EncryptionKeyConfigured bool                      `json:"encryption_key_configured"`
	Snapshot                *OllamaCloudUsageSnapshot `json:"snapshot,omitempty"`
}
