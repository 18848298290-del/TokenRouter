package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"

	"github.com/TokenFlux/TokenRouter/internal/identity/authconfig"
	schedulerpolicy "github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	ippolicy "github.com/TokenFlux/TokenRouter/internal/server/clientip/policy"
	"github.com/TokenFlux/TokenRouter/internal/server/httpconfig"
)

const (
	// Usage-record queue overflow policies.
	UsageRecordOverflowPolicyDrop   = "drop"
	UsageRecordOverflowPolicySample = "sample"
	UsageRecordOverflowPolicySync   = "sync"

	// DefaultCSPPolicy is the default Content-Security-Policy.
	// SecurityHeaders replaces __CSP_NONCE__ with a per-request nonce.
	DefaultCSPPolicy = httpconfig.DefaultCSPPolicy

	// Upstream HTTP connection pools can be shared by proxy, provider, or both.
	// Pool isolation determines connection reuse and resource consumption.
	// ConnectionPoolIsolationProxy shares a pool among providers using the same proxy.
	// This reduces pool count when many providers use a few proxies.
	ConnectionPoolIsolationProxy = "proxy"
	// ConnectionPoolIsolationProvider gives each provider a separate pool.
	ConnectionPoolIsolationProvider = "provider"
	// ConnectionPoolIsolationProviderProxy isolates each provider/proxy pair by default.
	// Requests using the same provider and proxy share a pool.
	ConnectionPoolIsolationProviderProxy = "provider_proxy"

	// DefaultUpstreamResponseReadMaxBytes limits non-streaming upstream response bodies.
	// The 128 MiB limit accommodates large base64-encoded image responses.
	// Override it with gateway.upstream_response_read_max_bytes.
	DefaultUpstreamResponseReadMaxBytes int64 = 128 * 1024 * 1024

	// DefaultModelsListReadMaxBytes limits upstream model-list response bodies.
	// Override it with gateway.models_list_read_max_bytes.
	DefaultModelsListReadMaxBytes int64 = 8 * 1024 * 1024

	defaultWeChatConnectMode             = "open"
	defaultWeChatConnectScopes           = "snsapi_login"
	defaultWeChatConnectFrontendRedirect = "/auth/wechat/callback"

	MaxForwardedClientIPHeaders = ippolicy.MaxForwardedClientIPHeaders

	ImageConcurrencyOverflowModeReject = "reject"
	ImageConcurrencyOverflowModeWait   = "wait"

	// DefaultOpenAIWSClientFirstMessageTimeoutSeconds bounds the wait for the first inbound message.
	DefaultOpenAIWSClientFirstMessageTimeoutSeconds = 30
)

var (
	// ErrDingTalkV1AppTypeMismatch reports a conflict between the app type and enterprise restriction.
	ErrDingTalkV1AppTypeMismatch = authconfig.ErrDingTalkV1AppTypeMismatch

	// ErrDingTalkV4InvalidAppKind reports an invalid DingTalk app kind.
	ErrDingTalkV4InvalidAppKind = authconfig.ErrDingTalkV4InvalidAppKind
)

// RequestLogConfig sets retention in days for request summaries.
type RequestLogConfig struct {
	RetentionDays int `mapstructure:"retention_days"`
}

type Config struct {
	RequestLog        RequestLogConfig           `mapstructure:"request_log"`
	Server            ServerConfig               `mapstructure:"server"`
	Log               LogConfig                  `mapstructure:"log"`
	CORS              CORSConfig                 `mapstructure:"cors"`
	Security          SecurityConfig             `mapstructure:"security"`
	Billing           BillingConfig              `mapstructure:"billing"`
	Turnstile         TurnstileConfig            `mapstructure:"turnstile"`
	Database          DatabaseConfig             `mapstructure:"database"`
	Redis             RedisConfig                `mapstructure:"redis"`
	Ops               OpsConfig                  `mapstructure:"ops"`
	JWT               JWTConfig                  `mapstructure:"jwt"`
	Totp              TotpConfig                 `mapstructure:"totp"`
	WebAuthn          WebAuthnConfig             `mapstructure:"webauthn"`
	LinuxDo           LinuxDoConnectConfig       `mapstructure:"linuxdo_connect"`
	WeChat            WeChatConnectConfig        `mapstructure:"wechat_connect"`
	OIDC              OIDCConnectConfig          `mapstructure:"oidc_connect"`
	DingTalk          DingTalkConnectConfig      `mapstructure:"dingtalk_connect"`
	GitHubOAuth       EmailOAuthProviderConfig   `mapstructure:"github_oauth"`
	GoogleOAuth       EmailOAuthProviderConfig   `mapstructure:"google_oauth"`
	Default           DefaultConfig              `mapstructure:"default"`
	RateLimit         RateLimitConfig            `mapstructure:"rate_limit"`
	Pricing           PricingConfig              `mapstructure:"pricing"`
	Gateway           GatewayConfig              `mapstructure:"gateway"`
	APIKeyAuth        APIKeyAuthCacheConfig      `mapstructure:"api_key_auth_cache"`
	SubscriptionCache SubscriptionCacheConfig    `mapstructure:"subscription_cache"`
	Dashboard         DashboardCacheConfig       `mapstructure:"dashboard_cache"`
	DashboardAgg      DashboardAggregationConfig `mapstructure:"dashboard_aggregation"`
	UsageCleanup      UsageCleanupConfig         `mapstructure:"usage_cleanup"`
	Concurrency       ConcurrencyConfig          `mapstructure:"concurrency"`
	TokenRefresh      TokenRefreshConfig         `mapstructure:"token_refresh"`
	Timezone          string                     `mapstructure:"timezone"` // e.g. "Asia/Shanghai", "UTC"
	Gemini            GeminiConfig               `mapstructure:"gemini"`
	Update            UpdateConfig               `mapstructure:"update"`
	Idempotency       IdempotencyConfig          `mapstructure:"idempotency"`
	BatchImage        BatchImageConfig           `mapstructure:"batch_image"`
	Creative          CreativeConfig             `mapstructure:"creative"`
	Team              TeamConfig                 `mapstructure:"team"`
}

// TeamConfig sets the default availability and limits of team features.
type TeamConfig struct {
	Enabled            bool `mapstructure:"enabled"`
	SelfServiceEnabled bool `mapstructure:"self_service_enabled"`
	DefaultMemberLimit int  `mapstructure:"default_member_limit"`
}

type LogConfig struct {
	Level           string            `mapstructure:"level"`
	Format          string            `mapstructure:"format"`
	ServiceName     string            `mapstructure:"service_name"`
	Environment     string            `mapstructure:"env"`
	Caller          bool              `mapstructure:"caller"`
	StacktraceLevel string            `mapstructure:"stacktrace_level"`
	Output          LogOutputConfig   `mapstructure:"output"`
	Rotation        LogRotationConfig `mapstructure:"rotation"`
	Sampling        LogSamplingConfig `mapstructure:"sampling"`
}

type LogOutputConfig struct {
	ToStdout bool   `mapstructure:"to_stdout"`
	ToFile   bool   `mapstructure:"to_file"`
	FilePath string `mapstructure:"file_path"`
}

type LogRotationConfig struct {
	MaxSizeMB  int  `mapstructure:"max_size_mb"`
	MaxBackups int  `mapstructure:"max_backups"`
	MaxAgeDays int  `mapstructure:"max_age_days"`
	Compress   bool `mapstructure:"compress"`
	LocalTime  bool `mapstructure:"local_time"`
}

type LogSamplingConfig struct {
	Enabled    bool `mapstructure:"enabled"`
	Initial    int  `mapstructure:"initial"`
	Thereafter int  `mapstructure:"thereafter"`
}

type GeminiConfig struct {
	OAuth GeminiOAuthConfig `mapstructure:"oauth"`
	Quota GeminiQuotaConfig `mapstructure:"quota"`
}

type GeminiOAuthConfig struct {
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	Scopes       string `mapstructure:"scopes"`
}

type GeminiQuotaConfig struct {
	Tiers  map[string]GeminiTierQuotaConfig `mapstructure:"tiers"`
	Policy string                           `mapstructure:"policy"`
}

type GeminiTierQuotaConfig struct {
	ProRPD          *int64 `mapstructure:"pro_rpd" json:"pro_rpd"`
	FlashRPD        *int64 `mapstructure:"flash_rpd" json:"flash_rpd"`
	CooldownMinutes *int   `mapstructure:"cooldown_minutes" json:"cooldown_minutes"`
}

type UpdateConfig struct {
	// ProxyURL is the proxy address for GitHub access.
	// Supported schemes are http, https, socks5, and socks5h.
	// Examples: "http://127.0.0.1:7890", "socks5://127.0.0.1:1080".
	ProxyURL string `mapstructure:"proxy_url"`
}

type IdempotencyConfig struct {
	// ObserveOnly allows requests missing Idempotency-Key during observation.
	ObserveOnly bool `mapstructure:"observe_only"`
	// DefaultTTLSeconds is the default retention for write-operation idempotency records.
	DefaultTTLSeconds int `mapstructure:"default_ttl_seconds"`
	// SystemOperationTTLSeconds is the retention for system-operation idempotency records.
	SystemOperationTTLSeconds int `mapstructure:"system_operation_ttl_seconds"`
	// ProcessingTimeoutSeconds is the lock timeout for records in processing state.
	ProcessingTimeoutSeconds int `mapstructure:"processing_timeout_seconds"`
	// FailedRetryBackoffSeconds is the retry delay after a failed operation.
	FailedRetryBackoffSeconds int `mapstructure:"failed_retry_backoff_seconds"`
	// MaxStoredResponseLen limits persisted response bodies in bytes.
	MaxStoredResponseLen int `mapstructure:"max_stored_response_len"`
	// CleanupIntervalSeconds is the interval between expired-record cleanup runs.
	CleanupIntervalSeconds int `mapstructure:"cleanup_interval_seconds"`
	// CleanupBatchSize limits the records removed in one cleanup batch.
	CleanupBatchSize int `mapstructure:"cleanup_batch_size"`
}

type BatchImageConfig struct {
	Enabled                           bool   `mapstructure:"enabled"`
	MaxItemsPerJobDefault             int    `mapstructure:"max_items_per_job_default"`
	MaxItemsPerJobTrial               int    `mapstructure:"max_items_per_job_trial"`
	MaxOutputImagesPerJob             int    `mapstructure:"max_output_images_per_job"`
	MaxOutputImagesPerItem            int    `mapstructure:"max_output_images_per_item"`
	MaxPromptCharsPerItem             int    `mapstructure:"max_prompt_chars_per_item"`
	MaxReferenceImagesPerJob          int    `mapstructure:"max_reference_images_per_job"`
	MaxReferenceInlineBytesPerJob     int    `mapstructure:"max_reference_inline_bytes_per_job"`
	DefaultResponseMimeType           string `mapstructure:"default_response_mime_type"`
	DefaultImageSize                  string `mapstructure:"default_image_size"`
	MaxDownloadItemsZip               int    `mapstructure:"max_download_items_zip"`
	MaxDownloadBytesPerRequest        int64  `mapstructure:"max_download_bytes_per_request"`
	MaxDownloadDurationSeconds        int    `mapstructure:"max_download_duration_seconds"`
	MaxDownloadConcurrencyPerUser     int    `mapstructure:"max_download_concurrency_per_user"`
	InputRetentionAfterTerminalHours  int    `mapstructure:"input_retention_after_terminal_hours"`
	OutputRetentionAfterTerminalHours int    `mapstructure:"output_retention_after_terminal_hours"`
	OutputRetentionMaxDays            int    `mapstructure:"output_retention_max_days"`
	CleanupIntervalMinutes            int    `mapstructure:"cleanup_interval_minutes"`
	CleanupBatchSize                  int    `mapstructure:"cleanup_batch_size"`
	QueueEnabled                      bool   `mapstructure:"queue_enabled"`
	QueueReadyKey                     string `mapstructure:"queue_ready_key"`
	QueueDelayedKey                   string `mapstructure:"queue_delayed_key"`
	QueueActiveKey                    string `mapstructure:"queue_active_key"`
	InflightKeyPrefix                 string `mapstructure:"inflight_key_prefix"`
	LockKeyPrefix                     string `mapstructure:"lock_key_prefix"`
	IdempotencyKeyPrefix              string `mapstructure:"idempotency_key_prefix"`
	InflightTTLSeconds                int    `mapstructure:"inflight_ttl_seconds"`
	JobLockTTLSeconds                 int    `mapstructure:"job_lock_ttl_seconds"`
	DefaultRequeueDelaySeconds        int    `mapstructure:"default_requeue_delay_seconds"`
	ErrorRetryDelaySeconds            int    `mapstructure:"error_retry_delay_seconds"`
	LockConflictDelaySeconds          int    `mapstructure:"lock_conflict_delay_seconds"`
	StaleActiveAfterSeconds           int    `mapstructure:"stale_active_after_seconds"`
	DelayedMoverIntervalSeconds       int    `mapstructure:"delayed_mover_interval_seconds"`
	RecoveryIntervalSeconds           int    `mapstructure:"recovery_interval_seconds"`
	DelayedMoveLimit                  int    `mapstructure:"delayed_move_limit"`
	RecoverLimit                      int    `mapstructure:"recover_limit"`
	VertexEnabled                     bool   `mapstructure:"vertex_enabled"`
	VertexProjectID                   string `mapstructure:"vertex_project_id"`
	VertexLocation                    string `mapstructure:"vertex_location"`
	// VertexManagedGCSBucket is a server-owned bucket for batch JSONL input/output.
	// Disable Cloud Storage soft delete on this bucket to avoid retaining deleted batch objects.
	VertexManagedGCSBucket       string `mapstructure:"vertex_managed_gcs_bucket"`
	VertexManagedGCSPrefix       string `mapstructure:"vertex_managed_gcs_prefix"`
	VertexInputRetentionHours    int    `mapstructure:"vertex_input_retention_hours"`
	VertexOutputRetentionHours   int    `mapstructure:"vertex_output_retention_hours"`
	VertexBatchPredictionBaseURL string `mapstructure:"vertex_batch_prediction_base_url"`
	VertexGCSBaseURL             string `mapstructure:"vertex_gcs_base_url"`
}

type CreativeConfig struct {
	// Enabled controls Creative Studio HTTP API availability.
	Enabled bool `mapstructure:"enabled"`
	// QueueEnabled enables the Creative Studio worker queue.
	QueueEnabled bool `mapstructure:"queue_enabled"`
	// TransientTTLSeconds is the retention for input payloads and temporary output.
	TransientTTLSeconds int `mapstructure:"transient_ttl_seconds"`
	// MaxAssetBytes limits each uploaded source image or mask in bytes.
	MaxAssetBytes int64 `mapstructure:"max_asset_bytes"`
	// MaxTotalInputBytes limits the total input size of one task in bytes.
	MaxTotalInputBytes int64 `mapstructure:"max_total_input_bytes"`
	// MaxPromptChars limits prompt length in characters.
	MaxPromptChars              int    `mapstructure:"max_prompt_chars"`
	DefaultResponseMimeType     string `mapstructure:"default_response_mime_type"`
	DefaultImageSize            string `mapstructure:"default_image_size"`
	QueueReadyKey               string `mapstructure:"queue_ready_key"`
	QueueDelayedKey             string `mapstructure:"queue_delayed_key"`
	QueueActiveKey              string `mapstructure:"queue_active_key"`
	InflightKeyPrefix           string `mapstructure:"inflight_key_prefix"`
	LockKeyPrefix               string `mapstructure:"lock_key_prefix"`
	InflightTTLSeconds          int    `mapstructure:"inflight_ttl_seconds"`
	JobLockTTLSeconds           int    `mapstructure:"job_lock_ttl_seconds"`
	DefaultRequeueDelaySeconds  int    `mapstructure:"default_requeue_delay_seconds"`
	ErrorRetryDelaySeconds      int    `mapstructure:"error_retry_delay_seconds"`
	LockConflictDelaySeconds    int    `mapstructure:"lock_conflict_delay_seconds"`
	StaleActiveAfterSeconds     int    `mapstructure:"stale_active_after_seconds"`
	DelayedMoverIntervalSeconds int    `mapstructure:"delayed_mover_interval_seconds"`
	RecoveryIntervalSeconds     int    `mapstructure:"recovery_interval_seconds"`
	DelayedMoveLimit            int    `mapstructure:"delayed_move_limit"`
	RecoverLimit                int    `mapstructure:"recover_limit"`
	// ExecuteTimeoutSeconds bounds one upstream execution attempt.
	ExecuteTimeoutSeconds int `mapstructure:"execute_timeout_seconds"`
	// MaxExecuteAttempts limits attempts after transient provider errors, including the first attempt.
	MaxExecuteAttempts int `mapstructure:"max_execute_attempts"`
}

type LinuxDoConnectConfig = authconfig.LinuxDoConnectConfig

type WeChatConnectConfig = authconfig.WeChatConnectConfig

type OIDCConnectConfig = authconfig.OIDCConnectConfig

type DingTalkConnectConfig = authconfig.DingTalkConnectConfig

// EmailOAuthProviderConfig configures email-based OAuth login such as GitHub and Google.
type EmailOAuthProviderConfig = authconfig.EmailOAuthProviderConfig

// TokenRefreshConfig configures automatic OAuth token refresh.
type TokenRefreshConfig struct {
	// Enabled controls automatic token refresh.
	Enabled bool `mapstructure:"enabled"`
	// Interval between refresh checks, in minutes.
	CheckIntervalMinutes int `mapstructure:"check_interval_minutes"`
	// Hours before expiry at which a token becomes eligible for refresh.
	RefreshBeforeExpiryHours float64 `mapstructure:"refresh_before_expiry_hours"`
	// Maximum retry count.
	MaxRetries int `mapstructure:"max_retries"`
	// Base retry backoff, in seconds.
	RetryBackoffSeconds int `mapstructure:"retry_backoff_seconds"`
	// Maximum candidate providers fetched in one database page.
	CandidatePageSize int `mapstructure:"candidate_page_size"`
	// Concurrent refresh limit per platform.
	ProviderConcurrency int `mapstructure:"provider_concurrency"`
	// Refresh requests per second per platform in this process.
	ProviderQPS int `mapstructure:"provider_qps"`
	// Consecutive temporary failures that stop a platform's current refresh cycle.
	ProviderFailureThreshold int `mapstructure:"provider_failure_threshold"`
	// Timeout for one upstream refresh attempt, in seconds.
	AttemptTimeoutSeconds int `mapstructure:"attempt_timeout_seconds"`
	// Total timeout for one background refresh cycle, in seconds.
	CycleTimeoutSeconds int `mapstructure:"cycle_timeout_seconds"`
}

type PricingConfig struct {
	// Model price and attribute catalog URL in models.dev format.
	RemoteURL string `mapstructure:"remote_url"`
	// Local data directory.
	DataDir string `mapstructure:"data_dir"`
	// Optional custom price supplement; empty uses the catalog and embedded supplements.
	FallbackFile string `mapstructure:"fallback_file"`
	// Catalog and local-file check interval, in minutes.
	CheckIntervalMinutes int `mapstructure:"check_interval_minutes"`
}

type ServerConfig struct {
	Host                     string                        `mapstructure:"host"`
	Port                     int                           `mapstructure:"port"`
	Mode                     string                        `mapstructure:"mode"`                      // debug/release
	EnableServerTiming       bool                          `mapstructure:"enable_server_timing"`      // Return Server-Timing for admin page requests.
	FrontendURL              string                        `mapstructure:"frontend_url"`              // Frontend base URL used for external links in email.
	ReadHeaderTimeout        int                           `mapstructure:"read_header_timeout"`       // Request-header read timeout, in seconds.
	MaxHeaderBytes           int                           `mapstructure:"max_header_bytes"`          // Header size limit in bytes; maps to the HTTP/2 header-list limit.
	IdleTimeout              int                           `mapstructure:"idle_timeout"`              // Idle connection timeout, in seconds.
	TrustedProxies           []string                      `mapstructure:"trusted_proxies"`           // Trusted proxy CIDRs or IPs.
	TrustedProxiesConfigured bool                          `mapstructure:"-" json:"-" yaml:"-"`       // Reports whether trusted proxies were configured, including an empty list.
	MaxRequestBodySize       int64                         `mapstructure:"max_request_body_size"`     // Global request-body size limit, in bytes.
	H2C                      H2CConfig                     `mapstructure:"h2c"`                       // HTTP/2 cleartext settings.
	TLSFingerprintCollector  TLSFingerprintCollectorConfig `mapstructure:"tls_fingerprint_collector"` // TLS fingerprint collector settings.
}

// TLSFingerprintCollectorConfig configures the TLS fingerprint collector.
type TLSFingerprintCollectorConfig struct {
	Host                 string `mapstructure:"host"`                    // Collector listen address.
	Port                 int    `mapstructure:"port"`                    // Collector listen port.
	PublicBaseURL        string `mapstructure:"public_base_url"`         // Client-accessible collection URL prefix.
	CertFile             string `mapstructure:"cert_file"`               // Optional server certificate path.
	KeyFile              string `mapstructure:"key_file"`                // Optional server private-key path.
	SessionTTLSeconds    int    `mapstructure:"session_ttl_seconds"`     // Collection session lifetime, in seconds.
	MaxRecordsPerSession int    `mapstructure:"max_records_per_session"` // Maximum collection records retained per session.
}

// H2CConfig configures HTTP/2 cleartext serving.
type H2CConfig struct {
	Enabled                      bool   `mapstructure:"enabled"`                          // Enable H2C.
	MaxConcurrentStreams         uint32 `mapstructure:"max_concurrent_streams"`           // Maximum concurrent streams.
	IdleTimeout                  int    `mapstructure:"idle_timeout"`                     // Idle timeout, in seconds.
	MaxReadFrameSize             int    `mapstructure:"max_read_frame_size"`              // Maximum frame size, in bytes.
	MaxUploadBufferPerConnection int    `mapstructure:"max_upload_buffer_per_connection"` // Upload buffer per connection, in bytes.
	MaxUploadBufferPerStream     int    `mapstructure:"max_upload_buffer_per_stream"`     // Upload buffer per stream, in bytes.
}

type CORSConfig = httpconfig.CORSConfig

// WebAuthnConfig defines this deployment's fixed WebAuthn relying-party identity.
// RPID and RPOrigins must not be inferred from untrusted Host or Origin headers.
type WebAuthnConfig struct {
	Enabled       bool     `mapstructure:"enabled"`
	RPDisplayName string   `mapstructure:"rp_display_name"`
	RPID          string   `mapstructure:"rp_id"`
	RPOrigins     []string `mapstructure:"rp_origins"`
}

type ForwardedClientIPSettings struct {
	TrustForwardedIP bool
	Headers          []string
}

type SecurityConfig struct {
	URLAllowlist    URLAllowlistConfig   `mapstructure:"url_allowlist"`
	ResponseHeaders ResponseHeaderConfig `mapstructure:"response_headers"`
	CSP             CSPConfig            `mapstructure:"csp"`
	ProxyFallback   ProxyFallbackConfig  `mapstructure:"proxy_fallback"`
	ProxyProbe      ProxyProbeConfig     `mapstructure:"proxy_probe"`
	// TrustForwardedIPForAPIKeyACL lets raw forwarding headers determine client IPs.
	// When disabled, client IP consumers rely on server.trusted_proxies.
	TrustForwardedIPForAPIKeyACL  bool                                       `mapstructure:"trust_forwarded_ip_for_api_key_acl"`
	ForwardedClientIPHeaders      []string                                   `mapstructure:"forwarded_client_ip_headers" json:"forwarded_client_ip_headers" yaml:"forwarded_client_ip_headers"`
	forwardedClientIPSettingsLive *atomic.Pointer[ForwardedClientIPSettings] `mapstructure:"-" json:"-" yaml:"-"`
}

type URLAllowlistConfig struct {
	Enabled           bool     `mapstructure:"enabled"`
	UpstreamHosts     []string `mapstructure:"upstream_hosts"`
	PricingHosts      []string `mapstructure:"pricing_hosts"`
	CRSHosts          []string `mapstructure:"crs_hosts"`
	AllowPrivateHosts bool     `mapstructure:"allow_private_hosts"`
	// Allow HTTP URLs when the allowlist is disabled; the default permits HTTPS only.
	AllowInsecureHTTP bool `mapstructure:"allow_insecure_http"`
}

type ResponseHeaderConfig struct {
	Enabled           bool     `mapstructure:"enabled"`
	AdditionalAllowed []string `mapstructure:"additional_allowed"`
	ForceRemove       []string `mapstructure:"force_remove"`
}

type CSPConfig = httpconfig.CSPConfig

type ProxyFallbackConfig struct {
	// AllowDirectOnError permits direct connections after auxiliary-service proxy initialization fails.
	// It applies to these auxiliary services:
	//   - GitHub Release update checks
	//   - Pricing data downloads
	// AI gateway connections, including Claude, OpenAI, Gemini, and Antigravity,
	// return proxy errors rather than connecting directly.
	// The default is false to prevent proxy misconfiguration from exposing the server IP.
	AllowDirectOnError bool `mapstructure:"allow_direct_on_error"`
}

type ProxyProbeConfig struct {
	InsecureSkipVerify bool `mapstructure:"insecure_skip_verify"` // Unsupported: TLS certificate verification cannot be skipped.
	// URLs lists probe endpoints in priority order; empty uses the built-in list.
	URLs []ProbeURLConfig `mapstructure:"urls"`
}

// ProbeURLConfig describes a proxy-egress probe endpoint and its response parser.
type ProbeURLConfig struct {
	URL    string `mapstructure:"url"`
	Parser string `mapstructure:"parser"` // ip-api / ipify / chatgpt-trace
}

type BillingConfig struct {
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker"`
	// MinimumBalanceReserve is the conservative balance threshold before balance-funded forwarding.
	// Zero requires only balance > 0.
	MinimumBalanceReserve float64 `mapstructure:"minimum_balance_reserve"`
}

type CircuitBreakerConfig struct {
	Enabled             bool `mapstructure:"enabled"`
	FailureThreshold    int  `mapstructure:"failure_threshold"`
	ResetTimeoutSeconds int  `mapstructure:"reset_timeout_seconds"`
	HalfOpenRequests    int  `mapstructure:"half_open_requests"`
}

type ConcurrencyConfig struct {
	// PingInterval is the SSE ping interval while waiting for concurrency, in seconds.
	PingInterval int `mapstructure:"ping_interval"`
}

type ImageConcurrencyConfig struct {
	// Enabled controls the separate image-generation concurrency limit; disabled by default.
	Enabled bool `mapstructure:"enabled"`
	// MaxConcurrentRequests limits concurrent image requests in this process; zero is unlimited.
	MaxConcurrentRequests int `mapstructure:"max_concurrent_requests"`
	// OverflowMode selects reject or wait when the image concurrency limit is reached.
	OverflowMode string `mapstructure:"overflow_mode"`
	// WaitTimeoutSeconds bounds slot waiting when overflow_mode is wait.
	WaitTimeoutSeconds int `mapstructure:"wait_timeout_seconds"`
	// MaxWaitingRequests limits queued image requests in this process when overflow_mode is wait.
	MaxWaitingRequests int `mapstructure:"max_waiting_requests"`
}

// GatewayConfig configures the API gateway.
type GatewayConfig struct {
	// Upstream response-header timeout, in seconds; zero disables it.
	// This deadline ends when headers arrive and does not limit streaming duration.
	ResponseHeaderTimeout int `mapstructure:"response_header_timeout"`
	// OpenAIResponseHeaderTimeout bounds OpenAI/Codex header waits in seconds; zero disables it.
	// The default permits long upstream queue waits independently of the generic timeout.
	OpenAIResponseHeaderTimeout int `mapstructure:"openai_response_header_timeout"`
	// GrokResponseHeaderTimeout bounds the pre-first-byte wait for xAI/Grok.
	// A zero value uses the provider-safe default instead of the generic gateway timeout.
	GrokResponseHeaderTimeout int `mapstructure:"grok_response_header_timeout"`
	// OpenAIFirstOutputTimeoutSeconds bounds the first semantic output for native HTTP Responses; zero
	// disables it.
	OpenAIFirstOutputTimeoutSeconds int `mapstructure:"openai_first_output_timeout_seconds"`
	// OpenAIHighEffortFirstOutputTimeoutSeconds bounds the first semantic output for high/xhigh/max reasoning.
	// Zero uses OpenAIFirstOutputTimeoutSeconds.
	OpenAIHighEffortFirstOutputTimeoutSeconds int `mapstructure:"openai_high_effort_first_output_timeout_seconds"`
	// Maximum gateway request-body size, in bytes.
	MaxBodySize int64 `mapstructure:"max_body_size"`
	// TextMaxBodySize limits bodies on endpoints that disallow inline image or video payloads.
	TextMaxBodySize int64 `mapstructure:"text_max_body_size"`
	// Non-streaming upstream response limit in bytes, bounding memory used by body reads.
	UpstreamResponseReadMaxBytes int64 `mapstructure:"upstream_response_read_max_bytes"`
	// Upstream model-list response limit, in bytes.
	ModelsListReadMaxBytes int64 `mapstructure:"models_list_read_max_bytes"`
	// Proxy-probe response limit, in bytes.
	ProxyProbeResponseReadMaxBytes int64 `mapstructure:"proxy_probe_response_read_max_bytes"`
	// Log Gemini response headers for debugging; disabled by default to limit log volume.
	GeminiDebugResponseHeaders bool `mapstructure:"gemini_debug_response_headers"`
	// ConnectionPoolIsolation selects proxy, provider, or provider_proxy isolation.
	ConnectionPoolIsolation string `mapstructure:"connection_pool_isolation"`
	// ForceCodexCLI treats OpenAI /v1/responses requests as Codex CLI requests.
	// Use when a gateway removes or rewrites User-Agent; disabled by default.
	ForceCodexCLI bool `mapstructure:"force_codex_cli"`
	// CodexImageGenerationBridgeEnabled injects the image_generation tool and bridge instructions into Codex
	// /v1/responses.
	// Disabled by default; requests already containing the tool still follow group capabilities.
	CodexImageGenerationBridgeEnabled bool `mapstructure:"codex_image_generation_bridge_enabled"`
	// ForcedCodexInstructionsTemplateFile is the template that supplies final Codex instructions.
	// The rendered template replaces instructions; include {{ .ExistingInstructions }} to retain converted client system text.
	ForcedCodexInstructionsTemplateFile string `mapstructure:"forced_codex_instructions_template_file"`
	// ForcedCodexInstructionsTemplate caches the template read at startup.
	// It is excluded from configuration decoding so requests can use it without disk reads.
	ForcedCodexInstructionsTemplate string `mapstructure:"-"`
	// OpenAICompactModel supplies the global model for explicit compact requests when provider mapping misses.
	OpenAICompactModel string `mapstructure:"openai_compact_model"`
	// OpenAIPassthroughAllowTimeoutHeaders permits client timeout headers in OpenAI passthrough mode.
	// Disabled by default so headers such as x-stainless-timeout do not prematurely end streams.
	OpenAIPassthroughAllowTimeoutHeaders bool `mapstructure:"openai_passthrough_allow_timeout_headers"`
	// OpenAIWS configures OpenAI Responses WebSocket connections.
	OpenAIWS GatewayOpenAIWSConfig `mapstructure:"openai_ws"`
	// Live configures ChatGPT Frameless Live sessions.
	Live GatewayLiveConfig `mapstructure:"live"`
	// AdvancedScheduler configures scheduling across platforms.
	AdvancedScheduler GatewayAdvancedSchedulerConfig `mapstructure:"advanced_scheduler"`
	// OpenAIHTTP2 prefers HTTP/2 and can fall back to HTTP/1.1 for incompatible proxies.
	OpenAIHTTP2 GatewayOpenAIHTTP2Config `mapstructure:"openai_http2"`
	// OpenAIProxyStreamCircuit configures Responses SSE proxy-disconnection isolation.
	OpenAIProxyStreamCircuit GatewayOpenAIProxyStreamCircuitConfig `mapstructure:"openai_proxy_stream_circuit"`
	// ImageConcurrency configures the separate image concurrency limit; disabled by default.
	ImageConcurrency ImageConcurrencyConfig `mapstructure:"image_concurrency"`

	// Upstream HTTP connection-pool limits.
	// MaxIdleConns limits idle connections across all hosts.
	MaxIdleConns int `mapstructure:"max_idle_conns"`
	// MaxIdleConnsPerHost limits idle connections retained per host for reuse.
	MaxIdleConnsPerHost int `mapstructure:"max_idle_conns_per_host"`
	// MaxConnsPerHost limits active and idle connections per host; zero is unlimited.
	MaxConnsPerHost int `mapstructure:"max_conns_per_host"`
	// IdleConnTimeoutSeconds is the idle connection timeout.
	IdleConnTimeoutSeconds int `mapstructure:"idle_conn_timeout_seconds"`
	// MaxUpstreamClients limits cached upstream HTTP clients.
	// Pool isolation creates clients for distinct provider/proxy identities.
	// Exceeding the cache limit evicts the least recently used client.
	// Size this limit for the expected active provider/proxy identities.
	MaxUpstreamClients int `mapstructure:"max_upstream_clients"`
	// ClientIdleTTLSeconds is the idle threshold for client reclamation.
	// Clients unused beyond this threshold become eligible for reclamation.
	// Choose the threshold based on request frequency.
	ClientIdleTTLSeconds int `mapstructure:"client_idle_ttl_seconds"`
	// ConcurrencySlotTTLMinutes is the concurrency slot lifetime.
	// Set it longer than the longest LLM request so active slots do not expire.
	ConcurrencySlotTTLMinutes int `mapstructure:"concurrency_slot_ttl_minutes"`
	// SessionIdleTimeoutMinutes is the session idle timeout; the default is five minutes.
	// It applies to session-count limits for Anthropic OAuth and SetupToken providers.
	// Sessions idle past the timeout are released.
	SessionIdleTimeoutMinutes int `mapstructure:"session_idle_timeout_minutes"`

	// StreamDataIntervalTimeout limits gaps between stream data in seconds; zero disables it.
	StreamDataIntervalTimeout int `mapstructure:"stream_data_interval_timeout"`
	// StreamKeepaliveInterval sets streaming keepalive seconds; zero disables it.
	StreamKeepaliveInterval int `mapstructure:"stream_keepalive_interval"`
	// ImageStreamDataIntervalTimeout limits image-stream data gaps in seconds; zero disables it.
	ImageStreamDataIntervalTimeout int `mapstructure:"image_stream_data_interval_timeout"`
	// ImageStreamKeepaliveInterval sets image-stream keepalive seconds; zero disables it.
	ImageStreamKeepaliveInterval int `mapstructure:"image_stream_keepalive_interval"`
	// ImageNonstreamKeepaliveInterval sets non-streaming image JSON keepalive seconds; zero disables it.
	ImageNonstreamKeepaliveInterval int `mapstructure:"image_nonstream_keepalive_interval"`
	// MaxLineSize limits an upstream SSE line in bytes; zero uses the default.
	MaxLineSize int `mapstructure:"max_line_size"`

	// Log summaries of upstream error response bodies.
	LogUpstreamErrorBody bool `mapstructure:"log_upstream_error_body"`
	// Maximum logged upstream error-body bytes; longer bodies are truncated.
	LogUpstreamErrorBodyMaxBytes int `mapstructure:"log_upstream_error_body_max_bytes"`

	// Add required anthropic-beta values for API-key providers when absent from the client; disabled by default.
	InjectBetaForAPIKey bool `mapstructure:"inject_beta_for_apikey"`

	// Allow failover for selected HTTP 400 errors; disabled by default.
	FailoverOn400 bool `mapstructure:"failover_on_400"`

	// Maximum provider switches after upstream errors.
	MaxProviderSwitches int `mapstructure:"max_provider_switches"`
	// Separate maximum provider switches for Gemini.
	MaxProviderSwitchesGemini int `mapstructure:"max_provider_switches_gemini"`

	// Antigravity 429 cooldown in minutes when reset time cannot be parsed.
	AntigravityFallbackCooldownMinutes int `mapstructure:"antigravity_fallback_cooldown_minutes"`

	// Scheduling configures provider selection and waiting.
	Scheduling GatewaySchedulingConfig `mapstructure:"scheduling"`

	// TLSFingerprint configures upstream TLS handshake fingerprints.
	TLSFingerprint TLSFingerprintConfig `mapstructure:"tls_fingerprint"`

	// UsageRecord configures the bounded asynchronous usage-record queue and workers.
	UsageRecord GatewayUsageRecordConfig `mapstructure:"usage_record"`

	// UserGroupRateCacheTTLSeconds is the cache lifetime for user/group rate multipliers.
	UserGroupRateCacheTTLSeconds int `mapstructure:"user_group_rate_cache_ttl_seconds"`
	// ModelsListCacheTTLSeconds accepts legacy configuration; catalog queries do not use this cache.
	ModelsListCacheTTLSeconds int `mapstructure:"models_list_cache_ttl_seconds"`

	// UserMessageQueue configures user-message serialization.
	// It serializes role:"user" messages per provider and applies RPM-based delay.
	UserMessageQueue UserMessageQueueConfig `mapstructure:"user_message_queue"`

	// Grok configures Grok/xAI scheduling and the free-tier quota gate.
	Grok GatewayGrokConfig `mapstructure:"grok"`

	// CNProviders configures usage monitoring for Chinese OpenAI-compatible suppliers.
	CNProviders GatewayCNProvidersConfig `mapstructure:"cn_providers"`
}

// GatewayGrokConfig configures Grok scheduling and its free-tier quota gate.
//
// The quota gate uses a local rolling usage window.
// It applies to OAuth providers marked free.
// The token limit defines the nominal window allowance.
// The gate stops new scheduling at a configured percentage from 1 to 100.
// Window duration is measured in hours.
// Usage statistics are cached so requests do not wait for the database.
// A cache miss permits scheduling and refreshes statistics in the background.
type GatewayGrokConfig struct {
	// PasswordAuthEnabled enables the optional password-to-SSO OAuth flow; disabled by default.
	// POST /admin/grok/oauth/password performs authorization only when enabled.
	PasswordAuthEnabled bool `mapstructure:"password_auth_enabled"`
	// FreeQuotaSoftGateEnabled gates Grok OAuth providers marked free using local rolling usage.
	FreeQuotaSoftGateEnabled bool `mapstructure:"free_quota_soft_gate_enabled"`
	// FreeQuotaTokenLimit is the nominal token allowance for the rolling window.
	FreeQuotaTokenLimit int64 `mapstructure:"free_quota_token_limit"`
	// FreeQuotaSoftGatePercent is the allowance percentage that stops new scheduling.
	FreeQuotaSoftGatePercent int `mapstructure:"free_quota_soft_gate_percent"`
	// FreeQuotaWindowHours is the local usage window length.
	FreeQuotaWindowHours int `mapstructure:"free_quota_window_hours"`
	// FreeQuotaStatsCacheSeconds is the quota-statistics cache TTL; requests do not wait for usage_logs.
	// A cache miss permits scheduling and triggers an asynchronous refresh.
	FreeQuotaStatsCacheSeconds int `mapstructure:"free_quota_stats_cache_seconds"`
}

// GatewayCNProvidersConfig configures periodic balance and quota probes for Chinese suppliers.
// Disabled by default; when enabled it probes active API-key providers. Manual queries run independently.
type GatewayCNProvidersConfig struct {
	// MonitorEnabled enables background probes; manual queries remain available when disabled.
	MonitorEnabled      bool    `mapstructure:"monitor_enabled"`
	BalanceThreshold    float64 `mapstructure:"balance_threshold"`
	IntervalMinutes     int     `mapstructure:"interval_minutes"`
	Concurrency         int     `mapstructure:"concurrency"`
	ProbeTimeoutSeconds int     `mapstructure:"probe_timeout_seconds"`
	RoundTimeoutSeconds int     `mapstructure:"round_timeout_seconds"`
}

// GatewayLiveConfig defines ChatGPT Frameless Live session limits.
type GatewayLiveConfig struct {
	// MaxSessionDurationSeconds is the hard Live session duration limit.
	MaxSessionDurationSeconds int `mapstructure:"max_session_duration_seconds"`
}

// GatewayOpenAIHTTP2Config configures OpenAI upstream HTTP versions.
// HTTP/2 is preferred by default, with policy-controlled HTTP/1.1 fallback for incompatible proxies.
type GatewayOpenAIHTTP2Config struct {
	// Enabled enables the OpenAI HTTP/2 preference.
	Enabled bool `mapstructure:"enabled"`
	// AllowProxyFallbackToHTTP1 temporarily selects HTTP/1.1 after recognized proxy H2 compatibility errors.
	AllowProxyFallbackToHTTP1 bool `mapstructure:"allow_proxy_fallback_to_http1"`
	// FallbackErrorThreshold is the compatibility-error count that triggers fallback within the window.
	FallbackErrorThreshold int `mapstructure:"fallback_error_threshold"`
	// FallbackWindowSeconds is the compatibility-error counting window.
	FallbackWindowSeconds int `mapstructure:"fallback_window_seconds"`
	// FallbackTTLSeconds is the duration of HTTP/1.1 fallback.
	FallbackTTLSeconds int `mapstructure:"fallback_ttl_seconds"`
}

// GatewayOpenAIProxyStreamCircuitConfig configures bounded, process-local Responses SSE proxy isolation.
type GatewayOpenAIProxyStreamCircuitConfig struct {
	// Disabled turns off proxy-disconnection isolation; isolation is enabled by default.
	Disabled bool `mapstructure:"disabled"`
	// FailureThreshold is the disconnection count within the window that isolates a proxy.
	FailureThreshold int `mapstructure:"failure_threshold"`
	// WindowSeconds is the disconnection counting window.
	WindowSeconds int `mapstructure:"window_seconds"`
	// TTLSeconds is the proxy isolation duration.
	TTLSeconds int `mapstructure:"ttl_seconds"`
}

// UserMessageQueueConfig configures serial user-message delivery
// for Anthropic OAuth and SetupToken providers.
type UserMessageQueueConfig struct {
	// Mode selects the queue behavior:
	// "serialize" uses a provider lock and RPM-based delay;
	// "throttle" adds RPM-based delay while allowing concurrent requests;
	// "" disables the queue by default.
	Mode string `mapstructure:"mode"`
	// Enabled is deprecated; true is equivalent to mode "serialize".
	Enabled bool `mapstructure:"enabled"`
	// LockTTLMs is the serial lock lifetime in milliseconds; set it longer than the longest request.
	LockTTLMs int `mapstructure:"lock_ttl_ms"`
	// WaitTimeoutMs bounds lock acquisition in milliseconds.
	WaitTimeoutMs int `mapstructure:"wait_timeout_ms"`
	// MinDelayMs is the minimum RPM-based delay in milliseconds.
	MinDelayMs int `mapstructure:"min_delay_ms"`
	// MaxDelayMs is the maximum RPM-based delay in milliseconds.
	MaxDelayMs int `mapstructure:"max_delay_ms"`
	// CleanupIntervalSeconds is the orphan-lock cleanup interval; zero disables cleanup.
	CleanupIntervalSeconds int `mapstructure:"cleanup_interval_seconds"`
}

// GatewayOpenAIWSConfig supplies deployment defaults for Responses WS, overridable by admin settings.
type GatewayOpenAIWSConfig struct {
	ClientFirstMessageTimeoutSeconds            int     `mapstructure:"client_first_message_timeout_seconds"`
	IngressInterTurnIdleTimeoutSeconds          int     `mapstructure:"ingress_inter_turn_idle_timeout_seconds"`
	MaxIngressConnectionsPerAPIKey              int     `mapstructure:"max_ingress_connections_per_api_key"`
	IngressPreviousResponseRecoveryEnabled      bool    `mapstructure:"ingress_previous_response_recovery_enabled"`
	ClientReadLimitBytes                        int64   `mapstructure:"client_read_limit_bytes"`
	HTTPBridgeThresholdBytes                    int64   `mapstructure:"http_bridge_threshold_bytes"`
	MaxConnsPerProvider                         int     `mapstructure:"max_conns_per_provider"`
	MinIdlePerProvider                          int     `mapstructure:"min_idle_per_provider"`
	MaxIdlePerProvider                          int     `mapstructure:"max_idle_per_provider"`
	DynamicMaxConnsByProviderConcurrencyEnabled bool    `mapstructure:"dynamic_max_conns_by_provider_concurrency_enabled"`
	OAuthMaxConnsFactor                         float64 `mapstructure:"oauth_max_conns_factor"`
	APIKeyMaxConnsFactor                        float64 `mapstructure:"apikey_max_conns_factor"`
	DialTimeoutSeconds                          int     `mapstructure:"dial_timeout_seconds"`
	ReadTimeoutSeconds                          int     `mapstructure:"read_timeout_seconds"`
	WriteTimeoutSeconds                         int     `mapstructure:"write_timeout_seconds"`
	PoolTargetUtilization                       float64 `mapstructure:"pool_target_utilization"`
	QueueLimitPerConn                           int     `mapstructure:"queue_limit_per_conn"`
	PrewarmCooldownMS                           int     `mapstructure:"prewarm_cooldown_ms"`
	StickySessionTTLSeconds                     int     `mapstructure:"sticky_session_ttl_seconds"`
	SessionHashReadOldFallback                  bool    `mapstructure:"session_hash_read_old_fallback"`
	SessionHashDualWriteOld                     bool    `mapstructure:"session_hash_dual_write_old"`
	MetadataBridgeEnabled                       bool    `mapstructure:"metadata_bridge_enabled"`
	StickyResponseIDTTLSeconds                  int     `mapstructure:"sticky_response_id_ttl_seconds"`
	StickyPreviousResponseTTLSeconds            int     `mapstructure:"sticky_previous_response_ttl_seconds"`
}

// GatewayAdvancedSchedulerScoreWeights weights provider scores in the advanced scheduler.
type GatewayAdvancedSchedulerScoreWeights = schedulerpolicy.ConfigScoreWeights

// GatewayAdvancedSchedulerConfig configures the cross-platform advanced scheduler.
type GatewayAdvancedSchedulerConfig struct {
	// LBTopK is the number of highest-scoring candidates retained before weighted selection.
	LBTopK int `mapstructure:"lb_top_k"`
	// ScoreWeights weights candidate scores in the advanced scheduler.
	ScoreWeights GatewayAdvancedSchedulerScoreWeights `mapstructure:"score_weights"`
	// EWMAErrorRateAlpha smooths error-rate feedback; larger values favor recent samples.
	EWMAErrorRateAlpha float64 `mapstructure:"ewma_error_rate_alpha"`
	// EWMATTFTAlpha smooths first-token latency feedback; larger values favor recent samples.
	EWMATTFTAlpha float64 `mapstructure:"ewma_ttft_alpha"`
	// StickyEscapeEnabled permits leaving session_hash affinity when provider health worsens.
	StickyEscapeEnabled bool `mapstructure:"sticky_escape_enabled"`
	// StickyEscapeTTFTMs is the TTFT EWMA threshold in milliseconds for skipping affinity.
	StickyEscapeTTFTMs int `mapstructure:"sticky_escape_ttft_ms"`
	// StickyEscapeErrorRate is the error-rate EWMA threshold for skipping affinity.
	StickyEscapeErrorRate float64 `mapstructure:"sticky_escape_error_rate"`
}

// GatewayUsageRecordConfig configures the asynchronous usage-record queue.
type GatewayUsageRecordConfig struct {
	// WorkerCount is the initial worker concurrency, including when autoscaling is enabled.
	WorkerCount int `mapstructure:"worker_count"`
	// QueueSize is the bounded queue capacity.
	QueueSize int `mapstructure:"queue_size"`
	// TaskTimeoutSeconds bounds one usage-record task.
	TaskTimeoutSeconds int `mapstructure:"task_timeout_seconds"`
	// OverflowPolicy selects drop, sample, or sync when the queue is full.
	OverflowPolicy string `mapstructure:"overflow_policy"`
	// OverflowSamplePercent is the percentage written synchronously under sample policy, from 1 to 100.
	OverflowSamplePercent int `mapstructure:"overflow_sample_percent"`

	// AutoScaleEnabled enables worker autoscaling.
	AutoScaleEnabled bool `mapstructure:"auto_scale_enabled"`
	// AutoScaleMinWorkers is the minimum worker count.
	AutoScaleMinWorkers int `mapstructure:"auto_scale_min_workers"`
	// AutoScaleMaxWorkers is the maximum worker count.
	AutoScaleMaxWorkers int `mapstructure:"auto_scale_max_workers"`
	// AutoScaleUpQueuePercent is the queue occupancy percentage that triggers growth.
	AutoScaleUpQueuePercent int `mapstructure:"auto_scale_up_queue_percent"`
	// AutoScaleDownQueuePercent is the occupancy percentage below which workers shrink.
	AutoScaleDownQueuePercent int `mapstructure:"auto_scale_down_queue_percent"`
	// AutoScaleUpStep is the number of workers added per adjustment.
	AutoScaleUpStep int `mapstructure:"auto_scale_up_step"`
	// AutoScaleDownStep is the number of workers removed per adjustment.
	AutoScaleDownStep int `mapstructure:"auto_scale_down_step"`
	// AutoScaleCheckIntervalSeconds is the autoscaling check interval.
	AutoScaleCheckIntervalSeconds int `mapstructure:"auto_scale_check_interval_seconds"`
	// AutoScaleCooldownSeconds is the delay between autoscaling adjustments.
	AutoScaleCooldownSeconds int `mapstructure:"auto_scale_cooldown_seconds"`
}

// TLSFingerprintConfig configures upstream TLS handshake fingerprints.
// It can reproduce the TLS handshake characteristics of Claude CLI on Node.js.
type TLSFingerprintConfig struct {
	// Enabled enables TLS fingerprinting globally.
	Enabled bool `mapstructure:"enabled"`
	// Profiles contains named TLS fingerprint templates.
	// Keys are profile names such as "claude_cli_v2" or "chrome_120".
	Profiles map[string]TLSProfileConfig `mapstructure:"profiles"`
}

// TLSProfileConfig configures one TLS fingerprint template.
// Empty list fields use built-in defaults for Claude CLI 2.x / Node.js 20.x.
// Use the TLS fingerprint collector to capture a complete profile.
type TLSProfileConfig struct {
	// Name is the profile display name.
	Name string `mapstructure:"name"`
	// EnableGREASE enables GREASE extensions for profiles such as Chrome.
	EnableGREASE bool `mapstructure:"enable_grease"`
	// CipherSuites lists TLS cipher suite IDs.
	CipherSuites []uint16 `mapstructure:"cipher_suites"`
	// Curves lists supported elliptic curve IDs.
	Curves []uint16 `mapstructure:"curves"`
	// PointFormats lists supported point format IDs.
	PointFormats []uint16 `mapstructure:"point_formats"`
	// SignatureAlgorithms lists signature algorithm IDs.
	SignatureAlgorithms []uint16 `mapstructure:"signature_algorithms"`
	// ALPNProtocols lists protocols such as ["h2", "http/1.1"].
	ALPNProtocols []string `mapstructure:"alpn_protocols"`
	// SupportedVersions lists TLS versions, such as [0x0304, 0x0303] for TLS 1.3 and 1.2.
	SupportedVersions []uint16 `mapstructure:"supported_versions"`
	// KeyShareGroups lists curves sent in Key Share, such as [29] for X25519.
	KeyShareGroups []uint16 `mapstructure:"key_share_groups"`
	// PSKModes lists PSK exchange modes, such as [1] for psk_dhe_ke.
	PSKModes []uint16 `mapstructure:"psk_modes"`
	// Extensions lists TLS extension IDs in wire order.
	// Empty uses the built-in order [0,11,10,35,16,22,23,13,43,45,51].
	// GREASE IDs such as 0x0a0a insert GREASE extensions.
	Extensions []uint16 `mapstructure:"extensions"`
}

// GatewaySchedulingConfig configures provider selection, waiting, and snapshot refresh.
type GatewaySchedulingConfig struct {
	// Sticky-session queue limits.
	StickySessionMaxWaiting  int           `mapstructure:"sticky_session_max_waiting"`
	StickySessionWaitTimeout time.Duration `mapstructure:"sticky_session_wait_timeout"`

	// Queue limits when the sticky provider is unavailable.
	FallbackWaitTimeout time.Duration `mapstructure:"fallback_wait_timeout"`
	FallbackMaxWaiting  int           `mapstructure:"fallback_max_waiting"`

	// Selection mode: "last_used" orders by last use by default; "random" randomizes selection.
	FallbackSelectionMode string `mapstructure:"fallback_selection_mode"`

	// PreferSoonestReset makes load-aware selection favor providers whose session windows reset soonest.
	// This consumes allowances near reset before those with more time remaining.
	// Disabled by default, using priority, load ratio, and LRU ordering.
	PreferSoonestReset bool `mapstructure:"prefer_soonest_reset"`

	// Batched load calculation and its cache lifetime.
	LoadBatchEnabled    bool `mapstructure:"load_batch_enabled"`
	LoadBatchCacheTTLMS int  `mapstructure:"load_batch_cache_ttl_ms"`
	// MGET chunk size when reading snapshot buckets.
	SnapshotMGetChunkSize int `mapstructure:"snapshot_mget_chunk_size"`
	// Cache write chunk size when rebuilding snapshots.
	SnapshotWriteChunkSize int `mapstructure:"snapshot_write_chunk_size"`

	// Expired-slot cleanup interval; zero disables cleanup.
	SlotCleanupInterval time.Duration `mapstructure:"slot_cleanup_interval"`

	// Enable database reads when cached scheduling data is unavailable.
	DbFallbackEnabled bool `mapstructure:"db_fallback_enabled"`
	// Database-read timeout in seconds; zero adds no tighter deadline.
	DbFallbackTimeoutSeconds int `mapstructure:"db_fallback_timeout_seconds"`
	// Database-read QPS limit per process; zero is unlimited.
	DbFallbackMaxQPS int `mapstructure:"db_fallback_max_qps"`

	// Outbox polling and rebuild thresholds.
	// Outbox polling interval, in seconds.
	OutboxPollIntervalSeconds int `mapstructure:"outbox_poll_interval_seconds"`
	// Outbox lag warning threshold, in seconds.
	OutboxLagWarnSeconds int `mapstructure:"outbox_lag_warn_seconds"`
	// Outbox lag threshold for a forced rebuild, in seconds.
	OutboxLagRebuildSeconds int `mapstructure:"outbox_lag_rebuild_seconds"`
	// Consecutive excessive-lag observations required for a rebuild.
	OutboxLagRebuildFailures int `mapstructure:"outbox_lag_rebuild_failures"`
	// Outbox backlog row count that triggers a rebuild.
	OutboxBacklogRebuildRows int `mapstructure:"outbox_backlog_rebuild_rows"`

	// Periodic full snapshot rebuilds.
	// Full rebuild interval in seconds; zero disables periodic rebuilds.
	FullRebuildIntervalSeconds int `mapstructure:"full_rebuild_interval_seconds"`
}

// DatabaseConfig configures database connections and their pool.
// Pool limits control connection reuse and resource consumption.
type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
	SSLMode  string `mapstructure:"sslmode"`
	// Database connection-pool limits.
	// MaxOpenConns limits open database connections.
	MaxOpenConns int `mapstructure:"max_open_conns"`
	// MaxIdleConns limits idle connections retained for reuse.
	MaxIdleConns int `mapstructure:"max_idle_conns"`
	// ConnMaxLifetimeMinutes limits a connection's total lifetime in minutes.
	ConnMaxLifetimeMinutes int `mapstructure:"conn_max_lifetime_minutes"`
	// ConnMaxIdleTimeMinutes limits a connection's idle lifetime in minutes.
	ConnMaxIdleTimeMinutes int `mapstructure:"conn_max_idle_time_minutes"`
}

// RedisConfig configures Redis connections, pool limits, and timeouts.
// Timeouts bound connection setup, reads, and writes.
type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	// Redis connection-pool limits and timeouts.
	// DialTimeoutSeconds bounds connection establishment.
	DialTimeoutSeconds int `mapstructure:"dial_timeout_seconds"`
	// ReadTimeoutSeconds bounds connection reads.
	ReadTimeoutSeconds int `mapstructure:"read_timeout_seconds"`
	// WriteTimeoutSeconds bounds connection writes.
	WriteTimeoutSeconds int `mapstructure:"write_timeout_seconds"`
	// PoolSize configures the connection pool size.
	PoolSize int `mapstructure:"pool_size"`
	// MinIdleConns keeps idle connections ready for reuse.
	MinIdleConns int `mapstructure:"min_idle_conns"`
	// EnableTLS enables TLS connections.
	EnableTLS bool `mapstructure:"enable_tls"`
}

type OpsConfig struct {
	// Enabled controls whether ops features should run.
	//
	// The database setting ops_monitoring_enabled controls runtime availability
	// when this deployment-level switch is enabled.
	Enabled bool `mapstructure:"enabled"`

	// Cleanup controls periodic deletion of old ops data to prevent unbounded growth.
	Cleanup OpsCleanupConfig `mapstructure:"cleanup"`

	// MetricsCollectorCache controls Redis caching for expensive per-window collector queries.
	MetricsCollectorCache OpsMetricsCollectorCacheConfig `mapstructure:"metrics_collector_cache"`

	// Pre-aggregation configuration.
	Aggregation OpsAggregationConfig `mapstructure:"aggregation"`
}

type OpsCleanupConfig struct {
	Enabled      bool   `mapstructure:"enabled"`
	Schedule     string `mapstructure:"schedule"`
	BatchSize    int    `mapstructure:"batch_size"`
	BatchPauseMS int    `mapstructure:"batch_pause_ms"`

	// Retention in days: zero clears the target table; positive values retain recent data.
	// All Ops datasets default to 30 days of retention.
	ErrorLogRetentionDays      int `mapstructure:"error_log_retention_days"`
	SystemLogRetentionDays     int `mapstructure:"system_log_retention_days"`
	MinuteMetricsRetentionDays int `mapstructure:"minute_metrics_retention_days"`
	HourlyMetricsRetentionDays int `mapstructure:"hourly_metrics_retention_days"`
}

type OpsAggregationConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

type OpsMetricsCollectorCacheConfig struct {
	Enabled bool          `mapstructure:"enabled"`
	TTL     time.Duration `mapstructure:"ttl"`
}

type JWTConfig struct {
	Secret     string `mapstructure:"secret"`
	ExpireHour int    `mapstructure:"expire_hour"`
	// AccessTokenExpireMinutes is the access-token lifetime in minutes.
	// Positive values take precedence over ExpireHour.
	// Zero uses ExpireHour for compatibility.
	AccessTokenExpireMinutes int `mapstructure:"access_token_expire_minutes"`
	// RefreshTokenExpireDays is the refresh-token lifetime; the default is 30 days.
	RefreshTokenExpireDays int `mapstructure:"refresh_token_expire_days"`
	// RefreshWindowMinutes permits refresh this many minutes before access-token expiry.
	RefreshWindowMinutes int `mapstructure:"refresh_window_minutes"`
}

// TotpConfig configures TOTP two-factor authentication.
type TotpConfig struct {
	// EncryptionKey is the hex-encoded 32-byte AES-256 key used to encrypt TOTP secrets.
	// Empty generates a random development key at startup.
	EncryptionKey string `mapstructure:"encryption_key"`
	// EncryptionKeyConfigured reports whether the key was configured rather than generated.
	// Admin settings permit enabling TOTP only with a configured key.
	EncryptionKeyConfigured bool `mapstructure:"-"`
}

type TurnstileConfig struct {
	Required bool `mapstructure:"required"`
}

type DefaultConfig struct {
	AdminEmail      string  `mapstructure:"admin_email"`
	AdminPassword   string  `mapstructure:"admin_password"`
	UserConcurrency int     `mapstructure:"user_concurrency"`
	UserBalance     float64 `mapstructure:"user_balance"`
	APIKeyPrefix    string  `mapstructure:"api_key_prefix"`
	RateMultiplier  float64 `mapstructure:"rate_multiplier"`
}

type RateLimitConfig struct {
	OverloadCooldownMinutes int `mapstructure:"overload_cooldown_minutes"`  // HTTP 529 overload cooldown, in minutes.
	OAuth401CooldownMinutes int `mapstructure:"oauth_401_cooldown_minutes"` // OAuth HTTP 401 scheduling cooldown, in minutes.
}

// APIKeyAuthCacheConfig configures API-key authentication caches.
type APIKeyAuthCacheConfig struct {
	L1Size             int                    `mapstructure:"l1_size"`
	L1TTLSeconds       int                    `mapstructure:"l1_ttl_seconds"`
	L2TTLSeconds       int                    `mapstructure:"l2_ttl_seconds"`
	NegativeTTLSeconds int                    `mapstructure:"negative_ttl_seconds"`
	JitterPercent      int                    `mapstructure:"jitter_percent"`
	Singleflight       bool                   `mapstructure:"singleflight"`
	LookupConcurrency  int                    `mapstructure:"lookup_concurrency"`
	InvalidAbuse       InvalidAuthAbuseConfig `mapstructure:"invalid_abuse"`
}

type InvalidAuthAbuseConfig struct {
	Enabled       bool `mapstructure:"enabled"`
	Threshold     int  `mapstructure:"threshold"`
	WindowSeconds int  `mapstructure:"window_seconds"`
	BlockSeconds  int  `mapstructure:"block_seconds"`
	Capacity      int  `mapstructure:"capacity"`
}

// SubscriptionCacheConfig configures the subscription-authentication L1 cache.
type SubscriptionCacheConfig struct {
	L1Size        int `mapstructure:"l1_size"`
	L1TTLSeconds  int `mapstructure:"l1_ttl_seconds"`
	JitterPercent int `mapstructure:"jitter_percent"`
}

// DashboardCacheConfig configures dashboard statistics caching.
type DashboardCacheConfig struct {
	// Enabled enables dashboard caching.
	Enabled bool `mapstructure:"enabled"`
	// KeyPrefix isolates Redis keys across deployments.
	KeyPrefix string `mapstructure:"key_prefix"`
	// StatsFreshTTLSeconds is the period during which cached statistics are considered fresh.
	StatsFreshTTLSeconds int `mapstructure:"stats_fresh_ttl_seconds"`
	// StatsTTLSeconds is the total Redis cache lifetime.
	StatsTTLSeconds int `mapstructure:"stats_ttl_seconds"`
	// StatsRefreshTimeoutSeconds bounds asynchronous refreshes.
	StatsRefreshTimeoutSeconds int `mapstructure:"stats_refresh_timeout_seconds"`
}

// DashboardAggregationConfig configures dashboard preaggregation.
type DashboardAggregationConfig struct {
	// Enabled enables preaggregation jobs.
	Enabled bool `mapstructure:"enabled"`
	// IntervalSeconds is the aggregation refresh interval.
	IntervalSeconds int `mapstructure:"interval_seconds"`
	// LookbackSeconds is the aggregation lookback window.
	LookbackSeconds int `mapstructure:"lookback_seconds"`
	// BackfillEnabled permits full backfills.
	BackfillEnabled bool `mapstructure:"backfill_enabled"`
	// BackfillMaxDays limits the time span of a backfill.
	BackfillMaxDays int `mapstructure:"backfill_max_days"`
	// Retention sets each table's retention window in days.
	Retention DashboardAggregationRetentionConfig `mapstructure:"retention"`
	// RecomputeDays is the number of recent days recomputed at startup.
	RecomputeDays int `mapstructure:"recompute_days"`
}

// DashboardAggregationRetentionConfig sets retention windows in days.
type DashboardAggregationRetentionConfig struct {
	UsageLogsDays         int `mapstructure:"usage_logs_days"`
	UsageBillingDedupDays int `mapstructure:"usage_billing_dedup_days"`
	HourlyDays            int `mapstructure:"hourly_days"`
	DailyDays             int `mapstructure:"daily_days"`
}

// UsageCleanupConfig configures usage-record cleanup jobs.
type UsageCleanupConfig struct {
	// Enabled enables the cleanup executor.
	Enabled bool `mapstructure:"enabled"`
	// MaxRangeDays limits the time span of one cleanup task.
	MaxRangeDays int `mapstructure:"max_range_days"`
	// BatchSize limits rows deleted per batch.
	BatchSize int `mapstructure:"batch_size"`
	// WorkerIntervalSeconds is the background task polling interval.
	WorkerIntervalSeconds int `mapstructure:"worker_interval_seconds"`
	// TaskTimeoutSeconds bounds one cleanup task.
	TaskTimeoutSeconds int `mapstructure:"task_timeout_seconds"`
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeWeChatConnectMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "mp":
		return "mp"
	case "mobile":
		return "mobile"
	default:
		return defaultWeChatConnectMode
	}
}

func normalizeWeChatConnectStoredMode(openEnabled, mpEnabled, mobileEnabled bool, mode string) string {
	mode = normalizeWeChatConnectMode(mode)
	switch mode {
	case "open":
		if openEnabled {
			return "open"
		}
	case "mp":
		if mpEnabled {
			return "mp"
		}
	case "mobile":
		if mobileEnabled {
			return "mobile"
		}
	}
	switch {
	case openEnabled:
		return "open"
	case mpEnabled:
		return "mp"
	case mobileEnabled:
		return "mobile"
	default:
		return mode
	}
}

func defaultWeChatConnectScopesForMode(mode string) string {
	switch normalizeWeChatConnectMode(mode) {
	case "mp":
		return "snsapi_userinfo"
	case "mobile":
		return ""
	default:
		return defaultWeChatConnectScopes
	}
}

func normalizeWeChatConnectScopes(raw, mode string) string {
	switch normalizeWeChatConnectMode(mode) {
	case "mp":
		switch strings.TrimSpace(raw) {
		case "snsapi_base":
			return "snsapi_base"
		case "snsapi_userinfo":
			return "snsapi_userinfo"
		default:
			return defaultWeChatConnectScopesForMode(mode)
		}
	case "mobile":
		return ""
	default:
		return defaultWeChatConnectScopes
	}
}

func shouldApplyLegacyWeChatEnv(configKey, envKey string) bool {
	if viper.InConfig(configKey) {
		return false
	}
	_, hasNewEnv := os.LookupEnv(envKey)
	return !hasNewEnv
}

func hasExplicitConfigOrEnv(configKey, envKey string) bool {
	if viper.InConfig(configKey) {
		return true
	}
	_, ok := os.LookupEnv(envKey)
	return ok
}

func applyLegacyWeChatConnectEnvCompatibility(cfg *WeChatConnectConfig) {
	if cfg == nil {
		return
	}

	legacyOpenAppID := ""
	if shouldApplyLegacyWeChatEnv("wechat_connect.open_app_id", "WECHAT_CONNECT_OPEN_APP_ID") &&
		shouldApplyLegacyWeChatEnv("wechat_connect.app_id", "WECHAT_CONNECT_APP_ID") {
		legacyOpenAppID = strings.TrimSpace(os.Getenv("WECHAT_OAUTH_OPEN_APP_ID"))
		if legacyOpenAppID != "" {
			cfg.OpenAppID = legacyOpenAppID
		}
	}

	legacyOpenAppSecret := ""
	if shouldApplyLegacyWeChatEnv("wechat_connect.open_app_secret", "WECHAT_CONNECT_OPEN_APP_SECRET") &&
		shouldApplyLegacyWeChatEnv("wechat_connect.app_secret", "WECHAT_CONNECT_APP_SECRET") {
		legacyOpenAppSecret = strings.TrimSpace(os.Getenv("WECHAT_OAUTH_OPEN_APP_SECRET"))
		if legacyOpenAppSecret != "" {
			cfg.OpenAppSecret = legacyOpenAppSecret
		}
	}

	legacyMPAppID := ""
	if shouldApplyLegacyWeChatEnv("wechat_connect.mp_app_id", "WECHAT_CONNECT_MP_APP_ID") &&
		shouldApplyLegacyWeChatEnv("wechat_connect.app_id", "WECHAT_CONNECT_APP_ID") {
		legacyMPAppID = strings.TrimSpace(os.Getenv("WECHAT_OAUTH_MP_APP_ID"))
		if legacyMPAppID != "" {
			cfg.MPAppID = legacyMPAppID
		}
	}

	legacyMPAppSecret := ""
	if shouldApplyLegacyWeChatEnv("wechat_connect.mp_app_secret", "WECHAT_CONNECT_MP_APP_SECRET") &&
		shouldApplyLegacyWeChatEnv("wechat_connect.app_secret", "WECHAT_CONNECT_APP_SECRET") {
		legacyMPAppSecret = strings.TrimSpace(os.Getenv("WECHAT_OAUTH_MP_APP_SECRET"))
		if legacyMPAppSecret != "" {
			cfg.MPAppSecret = legacyMPAppSecret
		}
	}

	if shouldApplyLegacyWeChatEnv("wechat_connect.frontend_redirect_url", "WECHAT_CONNECT_FRONTEND_REDIRECT_URL") {
		if legacyFrontend := strings.TrimSpace(os.Getenv("WECHAT_OAUTH_FRONTEND_REDIRECT_URL")); legacyFrontend != "" {
			cfg.FrontendRedirectURL = legacyFrontend
		}
	}

	hasLegacyOpen := legacyOpenAppID != "" && legacyOpenAppSecret != ""
	hasLegacyMP := legacyMPAppID != "" && legacyMPAppSecret != ""

	if shouldApplyLegacyWeChatEnv("wechat_connect.enabled", "WECHAT_CONNECT_ENABLED") && (hasLegacyOpen || hasLegacyMP) {
		cfg.Enabled = true
	}
	if shouldApplyLegacyWeChatEnv("wechat_connect.open_enabled", "WECHAT_CONNECT_OPEN_ENABLED") && hasLegacyOpen {
		cfg.OpenEnabled = true
	}
	if shouldApplyLegacyWeChatEnv("wechat_connect.mp_enabled", "WECHAT_CONNECT_MP_ENABLED") && hasLegacyMP {
		cfg.MPEnabled = true
	}
	if shouldApplyLegacyWeChatEnv("wechat_connect.mode", "WECHAT_CONNECT_MODE") {
		switch {
		case hasLegacyMP && !hasLegacyOpen:
			cfg.Mode = "mp"
		case hasLegacyOpen:
			cfg.Mode = "open"
		}
	}
	if shouldApplyLegacyWeChatEnv("wechat_connect.scopes", "WECHAT_CONNECT_SCOPES") {
		switch {
		case hasLegacyMP && !hasLegacyOpen:
			cfg.Scopes = defaultWeChatConnectScopesForMode("mp")
		case hasLegacyOpen:
			cfg.Scopes = defaultWeChatConnectScopesForMode("open")
		}
	}
}

func normalizeWeChatConnectConfig(cfg *WeChatConnectConfig) {
	if cfg == nil {
		return
	}

	cfg.AppID = strings.TrimSpace(cfg.AppID)
	cfg.AppSecret = strings.TrimSpace(cfg.AppSecret)
	cfg.OpenAppID = strings.TrimSpace(cfg.OpenAppID)
	cfg.OpenAppSecret = strings.TrimSpace(cfg.OpenAppSecret)
	cfg.MPAppID = strings.TrimSpace(cfg.MPAppID)
	cfg.MPAppSecret = strings.TrimSpace(cfg.MPAppSecret)
	cfg.MobileAppID = strings.TrimSpace(cfg.MobileAppID)
	cfg.MobileAppSecret = strings.TrimSpace(cfg.MobileAppSecret)
	cfg.Mode = normalizeWeChatConnectMode(cfg.Mode)
	cfg.RedirectURL = strings.TrimSpace(cfg.RedirectURL)
	cfg.FrontendRedirectURL = strings.TrimSpace(cfg.FrontendRedirectURL)

	cfg.AppID = firstNonEmptyString(cfg.AppID, cfg.OpenAppID, cfg.MPAppID, cfg.MobileAppID)
	cfg.AppSecret = firstNonEmptyString(cfg.AppSecret, cfg.OpenAppSecret, cfg.MPAppSecret, cfg.MobileAppSecret)
	cfg.OpenAppID = firstNonEmptyString(cfg.OpenAppID, cfg.AppID)
	cfg.OpenAppSecret = firstNonEmptyString(cfg.OpenAppSecret, cfg.AppSecret)
	cfg.MPAppID = firstNonEmptyString(cfg.MPAppID, cfg.AppID)
	cfg.MPAppSecret = firstNonEmptyString(cfg.MPAppSecret, cfg.AppSecret)
	cfg.MobileAppID = firstNonEmptyString(cfg.MobileAppID, cfg.AppID)
	cfg.MobileAppSecret = firstNonEmptyString(cfg.MobileAppSecret, cfg.AppSecret)

	if !cfg.OpenEnabled && !cfg.MPEnabled && !cfg.MobileEnabled && cfg.Enabled {
		switch cfg.Mode {
		case "mp":
			cfg.MPEnabled = true
		case "mobile":
			cfg.MobileEnabled = true
		default:
			cfg.OpenEnabled = true
		}
	}
	cfg.Mode = normalizeWeChatConnectStoredMode(cfg.OpenEnabled, cfg.MPEnabled, cfg.MobileEnabled, cfg.Mode)
	cfg.Scopes = normalizeWeChatConnectScopes(cfg.Scopes, cfg.Mode)
	if cfg.FrontendRedirectURL == "" {
		cfg.FrontendRedirectURL = defaultWeChatConnectFrontendRedirect
	}
}

func NormalizeForwardedClientIPHeaders(headers []string) ([]string, error) {
	return ippolicy.NormalizeForwardedClientIPHeaders(headers)
}

func cloneForwardedClientIPHeaders(headers []string) []string {
	if len(headers) == 0 {
		return []string{}
	}
	return append([]string(nil), headers...)
}

func (c *Config) ForwardedClientIPSettings() ForwardedClientIPSettings {
	if c == nil {
		return ForwardedClientIPSettings{Headers: []string{}}
	}
	live := c.Security.forwardedClientIPSettingsLive
	if live != nil {
		if snapshot := live.Load(); snapshot != nil {
			return ForwardedClientIPSettings{
				TrustForwardedIP: snapshot.TrustForwardedIP,
				Headers:          cloneForwardedClientIPHeaders(snapshot.Headers),
			}
		}
	}
	return ForwardedClientIPSettings{
		TrustForwardedIP: c.Security.TrustForwardedIPForAPIKeyACL,
		Headers:          cloneForwardedClientIPHeaders(c.Security.ForwardedClientIPHeaders),
	}
}

func (c *Config) TrustForwardedIPForAPIKeyACL() bool {
	return c.ForwardedClientIPSettings().TrustForwardedIP
}

func (c *Config) SetForwardedClientIPSettings(enabled bool, headers []string) {
	if c == nil {
		return
	}
	headers = cloneForwardedClientIPHeaders(headers)
	if c.Security.forwardedClientIPSettingsLive == nil {
		c.Security.forwardedClientIPSettingsLive = &atomic.Pointer[ForwardedClientIPSettings]{}
	}
	c.Security.forwardedClientIPSettingsLive.Store(&ForwardedClientIPSettings{
		TrustForwardedIP: enabled,
		Headers:          headers,
	})
}

func (c *Config) SetTrustForwardedIPForAPIKeyACL(enabled bool) {
	if c == nil {
		return
	}
	c.SetForwardedClientIPSettings(enabled, c.ForwardedClientIPSettings().Headers)
}

// normalizeProxyProbeURLs validates and normalizes configured probe endpoints.
func normalizeProxyProbeURLs(targets []ProbeURLConfig) ([]ProbeURLConfig, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	normalized := make([]ProbeURLConfig, 0, len(targets))
	for i, target := range targets {
		rawURL := strings.TrimSpace(target.URL)
		parser := strings.ToLower(strings.TrimSpace(target.Parser))
		if rawURL == "" {
			return nil, fmt.Errorf("entry %d: url is required", i)
		}
		if parser == "" {
			return nil, fmt.Errorf("entry %d: parser is required", i)
		}
		switch parser {
		case "ip-api", "ipify", "chatgpt-trace":
		default:
			return nil, fmt.Errorf("entry %d: unsupported parser %q", i, target.Parser)
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" {
			return nil, fmt.Errorf("entry %d: invalid url %q", i, target.URL)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("entry %d: url scheme must be http or https", i)
		}
		normalized = append(normalized, ProbeURLConfig{URL: rawURL, Parser: parser})
	}
	return normalized, nil
}

// WaitTimeout returns the configured wait duration, or 30 seconds for a nonpositive value.
func (c *UserMessageQueueConfig) WaitTimeout() time.Duration {
	if c.WaitTimeoutMs <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.WaitTimeoutMs) * time.Millisecond
}

// GetEffectiveMode returns serialize, throttle, or an empty string for disabled queuing.
// Load clears unsupported modes; the legacy Enabled flag also selects serialization.
func (c *UserMessageQueueConfig) GetEffectiveMode() string {
	if c.Mode == schedulerpolicy.MessageQueueSerialize || c.Mode == schedulerpolicy.MessageQueueThrottle {
		return c.Mode
	}
	if c.Enabled {
		return schedulerpolicy.MessageQueueSerialize // Legacy Enabled flag.
	}
	return ""
}

func (s *ServerConfig) Address() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

func (d *DatabaseConfig) DSN() string {
	// Omit an empty password parameter so libpq can parse the remaining DSN fields.
	if d.Password == "" {
		return fmt.Sprintf(
			"host=%s port=%d user=%s dbname=%s sslmode=%s",
			d.Host, d.Port, d.User, d.DBName, d.SSLMode,
		)
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.DBName, d.SSLMode,
	)
}

// DSNWithTimezone returns a DSN with the supplied time zone; empty uses Asia/Shanghai.
func (d *DatabaseConfig) DSNWithTimezone(tz string) string {
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	// Omit an empty password parameter so libpq can parse the remaining DSN fields.
	if d.Password == "" {
		return fmt.Sprintf(
			"host=%s port=%d user=%s dbname=%s sslmode=%s TimeZone=%s",
			d.Host, d.Port, d.User, d.DBName, d.SSLMode, tz,
		)
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
		d.Host, d.Port, d.User, d.Password, d.DBName, d.SSLMode, tz,
	)
}

func (r *RedisConfig) Address() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

// Load reads and validates the full configuration, requiring a configured jwt.secret.
func Load() (*Config, error) {
	return load(false)
}

// LoadForBootstrap reads configuration for startup.
//
// It permits an empty jwt.secret; database initialization must supply it and validate again.
func LoadForBootstrap() (*Config, error) {
	return load(true)
}

// load reads process configuration using the sources documented below.
// @project-doc docs/interfaces/configuration.md#configuration_sources
func load(allowMissingJWTSecret bool) (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	configureConfigSource(viper.SetConfigFile, viper.AddConfigPath)

	// Map environment variables to dotted configuration keys.
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	if tz, ok := os.LookupEnv("TZ"); ok && strings.TrimSpace(tz) != "" {
		// TZ overrides the legacy TIMEZONE value so container and application statistics use the same zone.
		viper.Set("timezone", strings.TrimSpace(tz))
	}
	if err := viper.BindEnv("server.enable_server_timing", "ENABLE_SERVER_TIMING"); err != nil {
		return nil, fmt.Errorf("bind ENABLE_SERVER_TIMING: %w", err)
	}

	// Register defaults before decoding configuration.
	setDefaults()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config error: %w", err)
		}
		// Continue with defaults and environment variables when no file is found.
	}
	if err := applyLegacyConfigCompatibility(); err != nil {
		return nil, err
	}
	trustedProxiesEnv, trustedProxiesEnvConfigured := os.LookupEnv("SERVER_TRUSTED_PROXIES")
	forwardedClientIPHeadersEnv, forwardedClientIPHeadersEnvConfigured := os.LookupEnv("SECURITY_FORWARDED_CLIENT_IP_HEADERS")
	trustedProxiesConfigured := viper.InConfig("server.trusted_proxies") ||
		viper.IsSet("server.trusted_proxies") || trustedProxiesEnvConfigured

	if strings.TrimSpace(viper.GetString("pricing.override_file")) != "" {
		slog.Warn("pricing.override_file is retired and ignored; configure user prices in Pricing Management before upgrading")
	}
	var cfg Config
	warnLegacyResponsesWSSettings()
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config error: %w", err)
	}
	if trustedProxiesEnvConfigured {
		cfg.Server.TrustedProxies = normalizeStringSlice(strings.Split(trustedProxiesEnv, ","))
	}
	if forwardedClientIPHeadersEnvConfigured {
		cfg.Security.ForwardedClientIPHeaders = normalizeStringSlice(strings.Split(forwardedClientIPHeadersEnv, ","))
	}
	cfg.Server.TrustedProxiesConfigured = trustedProxiesConfigured
	cfg.Gateway.ConnectionPoolIsolation = normalizeLegacyConnectionPoolIsolation(cfg.Gateway.ConnectionPoolIsolation)
	// setEnvReachableDefaults normally registers this key as true, making IsSet true.
	// This guard also supplies the default if that registration is absent.
	if !cfg.Gateway.AdvancedScheduler.StickyEscapeEnabled && !viper.IsSet("gateway.advanced_scheduler.sticky_escape_enabled") {
		cfg.Gateway.AdvancedScheduler.StickyEscapeEnabled = true
	}

	cfg.Server.Mode = strings.ToLower(strings.TrimSpace(cfg.Server.Mode))
	if cfg.Server.Mode == "" {
		cfg.Server.Mode = "debug"
	}
	cfg.Server.FrontendURL = strings.TrimSpace(cfg.Server.FrontendURL)
	cfg.JWT.Secret = strings.TrimSpace(cfg.JWT.Secret)
	cfg.LinuxDo.ClientID = strings.TrimSpace(cfg.LinuxDo.ClientID)
	cfg.LinuxDo.ClientSecret = strings.TrimSpace(cfg.LinuxDo.ClientSecret)
	cfg.LinuxDo.AuthorizeURL = strings.TrimSpace(cfg.LinuxDo.AuthorizeURL)
	cfg.LinuxDo.TokenURL = strings.TrimSpace(cfg.LinuxDo.TokenURL)
	cfg.LinuxDo.UserInfoURL = strings.TrimSpace(cfg.LinuxDo.UserInfoURL)
	cfg.LinuxDo.Scopes = strings.TrimSpace(cfg.LinuxDo.Scopes)
	cfg.LinuxDo.RedirectURL = strings.TrimSpace(cfg.LinuxDo.RedirectURL)
	cfg.LinuxDo.FrontendRedirectURL = strings.TrimSpace(cfg.LinuxDo.FrontendRedirectURL)
	cfg.LinuxDo.TokenAuthMethod = strings.ToLower(strings.TrimSpace(cfg.LinuxDo.TokenAuthMethod))
	cfg.LinuxDo.UserInfoEmailPath = strings.TrimSpace(cfg.LinuxDo.UserInfoEmailPath)
	cfg.LinuxDo.UserInfoIDPath = strings.TrimSpace(cfg.LinuxDo.UserInfoIDPath)
	cfg.LinuxDo.UserInfoUsernamePath = strings.TrimSpace(cfg.LinuxDo.UserInfoUsernamePath)
	applyLegacyWeChatConnectEnvCompatibility(&cfg.WeChat)
	normalizeWeChatConnectConfig(&cfg.WeChat)
	cfg.OIDC.ProviderName = strings.TrimSpace(cfg.OIDC.ProviderName)
	cfg.OIDC.ClientID = strings.TrimSpace(cfg.OIDC.ClientID)
	cfg.OIDC.ClientSecret = strings.TrimSpace(cfg.OIDC.ClientSecret)
	cfg.OIDC.IssuerURL = strings.TrimSpace(cfg.OIDC.IssuerURL)
	cfg.OIDC.DiscoveryURL = strings.TrimSpace(cfg.OIDC.DiscoveryURL)
	cfg.OIDC.AuthorizeURL = strings.TrimSpace(cfg.OIDC.AuthorizeURL)
	cfg.OIDC.TokenURL = strings.TrimSpace(cfg.OIDC.TokenURL)
	cfg.OIDC.UserInfoURL = strings.TrimSpace(cfg.OIDC.UserInfoURL)
	cfg.OIDC.JWKSURL = strings.TrimSpace(cfg.OIDC.JWKSURL)
	cfg.OIDC.Scopes = strings.TrimSpace(cfg.OIDC.Scopes)
	cfg.OIDC.RedirectURL = strings.TrimSpace(cfg.OIDC.RedirectURL)
	cfg.OIDC.FrontendRedirectURL = strings.TrimSpace(cfg.OIDC.FrontendRedirectURL)
	cfg.OIDC.TokenAuthMethod = strings.ToLower(strings.TrimSpace(cfg.OIDC.TokenAuthMethod))
	cfg.OIDC.AllowedSigningAlgs = strings.TrimSpace(cfg.OIDC.AllowedSigningAlgs)
	cfg.OIDC.UserInfoEmailPath = strings.TrimSpace(cfg.OIDC.UserInfoEmailPath)
	cfg.OIDC.UserInfoIDPath = strings.TrimSpace(cfg.OIDC.UserInfoIDPath)
	cfg.OIDC.UserInfoUsernamePath = strings.TrimSpace(cfg.OIDC.UserInfoUsernamePath)
	cfg.OIDC.UsePKCEExplicit = hasExplicitConfigOrEnv("oidc_connect.use_pkce", "OIDC_CONNECT_USE_PKCE")
	cfg.OIDC.ValidateIDTokenExplicit = hasExplicitConfigOrEnv("oidc_connect.validate_id_token", "OIDC_CONNECT_VALIDATE_ID_TOKEN")
	cfg.Dashboard.KeyPrefix = strings.TrimSpace(cfg.Dashboard.KeyPrefix)
	cfg.CORS.AllowedOrigins = normalizeStringSlice(cfg.CORS.AllowedOrigins)
	cfg.Security.ResponseHeaders.AdditionalAllowed = normalizeStringSlice(cfg.Security.ResponseHeaders.AdditionalAllowed)
	cfg.Security.ResponseHeaders.ForceRemove = normalizeStringSlice(cfg.Security.ResponseHeaders.ForceRemove)
	cfg.Security.CSP.Policy = strings.TrimSpace(cfg.Security.CSP.Policy)
	forwardedClientIPHeaders, err := NormalizeForwardedClientIPHeaders(cfg.Security.ForwardedClientIPHeaders)
	if err != nil {
		return nil, fmt.Errorf("security.forwarded_client_ip_headers: %w", err)
	}
	cfg.Security.ForwardedClientIPHeaders = forwardedClientIPHeaders
	cfg.SetForwardedClientIPSettings(cfg.Security.TrustForwardedIPForAPIKeyACL, forwardedClientIPHeaders)
	cfg.Log.Level = strings.ToLower(strings.TrimSpace(cfg.Log.Level))
	cfg.Log.Format = strings.ToLower(strings.TrimSpace(cfg.Log.Format))
	cfg.Log.ServiceName = strings.TrimSpace(cfg.Log.ServiceName)
	cfg.Log.Environment = strings.TrimSpace(cfg.Log.Environment)
	cfg.Log.StacktraceLevel = strings.ToLower(strings.TrimSpace(cfg.Log.StacktraceLevel))
	cfg.Log.Output.FilePath = strings.TrimSpace(cfg.Log.Output.FilePath)
	cfg.Gateway.ForcedCodexInstructionsTemplateFile = strings.TrimSpace(cfg.Gateway.ForcedCodexInstructionsTemplateFile)
	if cfg.Gateway.ForcedCodexInstructionsTemplateFile != "" {
		content, err := os.ReadFile(cfg.Gateway.ForcedCodexInstructionsTemplateFile)
		if err != nil {
			return nil, fmt.Errorf("read forced codex instructions template %q: %w", cfg.Gateway.ForcedCodexInstructionsTemplateFile, err)
		}
		cfg.Gateway.ForcedCodexInstructionsTemplate = string(content)
	}

	// Accept the legacy gateway.openai_ws.sticky_previous_response_ttl_seconds value.
	// A positive new value takes precedence; nonpositive values use the old key.
	if cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds <= 0 && cfg.Gateway.OpenAIWS.StickyPreviousResponseTTLSeconds > 0 {
		cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = cfg.Gateway.OpenAIWS.StickyPreviousResponseTTLSeconds
	}

	// Clear unsupported queue modes and warn once during loading.
	if m := cfg.Gateway.UserMessageQueue.Mode; m != "" && m != schedulerpolicy.MessageQueueSerialize && m != schedulerpolicy.MessageQueueThrottle {
		slog.Warn("invalid user_message_queue mode, disabling",
			"mode", m,
			"valid_modes", []string{schedulerpolicy.MessageQueueSerialize, schedulerpolicy.MessageQueueThrottle})
		cfg.Gateway.UserMessageQueue.Mode = ""
	}

	// Auto-generate TOTP encryption key if not set (32 bytes = 64 hex chars for AES-256)
	cfg.Totp.EncryptionKey = strings.TrimSpace(cfg.Totp.EncryptionKey)
	if cfg.Totp.EncryptionKey == "" {
		key, err := generateJWTSecret(32)
		if err != nil {
			return nil, fmt.Errorf("generate totp encryption key error: %w", err)
		}
		cfg.Totp.EncryptionKey = key
		cfg.Totp.EncryptionKeyConfigured = false
		slog.Warn("TOTP encryption key auto-generated. Consider setting a fixed key for production.")
	} else {
		cfg.Totp.EncryptionKeyConfigured = true
	}

	originalJWTSecret := cfg.JWT.Secret
	if allowMissingJWTSecret && originalJWTSecret == "" {
		// Use a temporary valid secret for validation; bootstrap supplies the persisted secret later.
		cfg.JWT.Secret = strings.Repeat("0", 32)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config error: %w", err)
	}

	if allowMissingJWTSecret && originalJWTSecret == "" {
		cfg.JWT.Secret = ""
	}

	if !cfg.Security.URLAllowlist.Enabled {
		slog.Warn("security.url_allowlist.enabled=false; allowlist/SSRF checks disabled (minimal format validation only).")
	}
	if !cfg.Security.ResponseHeaders.Enabled {
		slog.Warn("security.response_headers.enabled=false; configurable header filtering disabled (default allowlist only).")
	}

	if cfg.JWT.Secret != "" && isWeakJWTSecret(cfg.JWT.Secret) {
		slog.Warn("JWT secret appears weak; use a 32+ character random secret in production.")
	}
	if len(cfg.Security.ResponseHeaders.AdditionalAllowed) > 0 || len(cfg.Security.ResponseHeaders.ForceRemove) > 0 {
		slog.Info("response header policy configured",
			"additional_allowed", cfg.Security.ResponseHeaders.AdditionalAllowed,
			"force_remove", cfg.Security.ResponseHeaders.ForceRemove,
		)
	}

	return &cfg, nil
}

// configureConfigSource uses CONFIG_FILE when set, otherwise searches config.yaml directories in order.
func configureConfigSource(setConfigFile, addConfigPath func(string)) {
	if configFile := strings.TrimSpace(os.Getenv("CONFIG_FILE")); configFile != "" {
		setConfigFile(configFile)
		return
	}

	// DATA_DIR precedes container, working-directory, and system paths.
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		addConfigPath(dataDir)
	}
	addConfigPath("/app/data")
	addConfigPath(".")
	addConfigPath("./config")
	addConfigPath("/etc/tokenrouter")
	// Legacy installations can use the old path; the new system path takes precedence.
	addConfigPath("/etc/sub2api")
}

func setDefaults() {
	// Server
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.mode", "release")
	viper.SetDefault("server.enable_server_timing", false)
	viper.SetDefault("server.frontend_url", "")
	viper.SetDefault("server.read_header_timeout", 10) // 10-second header read timeout.
	viper.SetDefault("server.max_header_bytes", 64*1024)
	viper.SetDefault("server.idle_timeout", 120) // 120-second idle timeout.
	viper.SetDefault("server.max_request_body_size", int64(256*1024*1024))
	// H2C defaults.
	viper.SetDefault("server.h2c.enabled", false)
	viper.SetDefault("server.h2c.max_concurrent_streams", uint32(50))      // 50 concurrent streams.
	viper.SetDefault("server.h2c.idle_timeout", 75)                        // 75 seconds.
	viper.SetDefault("server.h2c.max_read_frame_size", 1<<20)              // 1 MiB.
	viper.SetDefault("server.h2c.max_upload_buffer_per_connection", 2<<20) // 2 MiB
	viper.SetDefault("server.h2c.max_upload_buffer_per_stream", 512<<10)   // 512 KiB
	// The collector starts from the UI; these values supply its listen settings.
	viper.SetDefault("server.tls_fingerprint_collector.host", "0.0.0.0")
	viper.SetDefault("server.tls_fingerprint_collector.port", 8443)
	viper.SetDefault("server.tls_fingerprint_collector.public_base_url", "")
	viper.SetDefault("server.tls_fingerprint_collector.cert_file", "")
	viper.SetDefault("server.tls_fingerprint_collector.key_file", "")
	viper.SetDefault("server.tls_fingerprint_collector.session_ttl_seconds", 1800)
	viper.SetDefault("server.tls_fingerprint_collector.max_records_per_session", 20)

	// Log
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.format", "console")
	viper.SetDefault("log.service_name", "tokenrouter")
	viper.SetDefault("log.env", "production")
	viper.SetDefault("log.caller", true)
	viper.SetDefault("log.stacktrace_level", "error")
	viper.SetDefault("log.output.to_stdout", true)
	viper.SetDefault("log.output.to_file", true)
	viper.SetDefault("log.output.file_path", "")
	viper.SetDefault("log.rotation.max_size_mb", 100)
	viper.SetDefault("log.rotation.max_backups", 10)
	viper.SetDefault("log.rotation.max_age_days", 7)
	viper.SetDefault("log.rotation.compress", true)
	viper.SetDefault("log.rotation.local_time", true)
	viper.SetDefault("log.sampling.enabled", false)
	viper.SetDefault("log.sampling.initial", 100)
	viper.SetDefault("log.sampling.thereafter", 100)

	// CORS
	viper.SetDefault("cors.allowed_origins", []string{})
	viper.SetDefault("cors.allow_credentials", true)

	// Passkeys are disabled by default; enabling requires a relying-party domain and trusted browser origins.
	viper.SetDefault("webauthn.enabled", false)
	viper.SetDefault("webauthn.rp_display_name", "TokenRouter")
	viper.SetDefault("webauthn.rp_id", "")
	viper.SetDefault("webauthn.rp_origins", []string{})

	// Security
	viper.SetDefault("security.url_allowlist.enabled", false)
	viper.SetDefault("security.url_allowlist.upstream_hosts", []string{
		"api.openai.com",
		"api.anthropic.com",
		"api.kimi.com",
		"api.moonshot.ai",
		"api.moonshot.cn",
		"open.bigmodel.cn",
		"api.z.ai",
		"api.deepseek.com",
		"api.typesafe.ai",
		"api.minimaxi.com",
		"generativelanguage.googleapis.com",
		"cloudcode-pa.googleapis.com",
		"*.openai.azure.com",
	})
	viper.SetDefault("security.url_allowlist.pricing_hosts", []string{
		"models.dev",
		"raw.githubusercontent.com",
	})
	viper.SetDefault("security.url_allowlist.crs_hosts", []string{})
	viper.SetDefault("security.url_allowlist.allow_private_hosts", true)
	viper.SetDefault("security.url_allowlist.allow_insecure_http", true)
	viper.SetDefault("security.response_headers.enabled", true)
	viper.SetDefault("security.response_headers.additional_allowed", []string{})
	viper.SetDefault("security.response_headers.force_remove", []string{})
	viper.SetDefault("security.csp.enabled", true)
	viper.SetDefault("security.csp.policy", DefaultCSPPolicy)
	viper.SetDefault("security.proxy_probe.insecure_skip_verify", false)
	viper.SetDefault("security.trust_forwarded_ip_for_api_key_acl", true)

	// Security - disable direct fallback on proxy error
	viper.SetDefault("security.proxy_fallback.allow_direct_on_error", false)

	// Billing
	viper.SetDefault("billing.circuit_breaker.enabled", true)
	viper.SetDefault("billing.circuit_breaker.failure_threshold", 5)
	viper.SetDefault("billing.circuit_breaker.reset_timeout_seconds", 30)
	viper.SetDefault("billing.circuit_breaker.half_open_requests", 3)
	viper.SetDefault("billing.minimum_balance_reserve", 0.000001)

	// Turnstile
	viper.SetDefault("turnstile.required", false)

	// LinuxDo Connect OAuth login.
	viper.SetDefault("linuxdo_connect.enabled", false)
	viper.SetDefault("linuxdo_connect.client_id", "")
	viper.SetDefault("linuxdo_connect.client_secret", "")
	viper.SetDefault("linuxdo_connect.authorize_url", "https://connect.linux.do/oauth2/authorize")
	viper.SetDefault("linuxdo_connect.token_url", "https://connect.linux.do/oauth2/token")
	viper.SetDefault("linuxdo_connect.userinfo_url", "https://connect.linux.do/api/user")
	viper.SetDefault("linuxdo_connect.scopes", "user")
	viper.SetDefault("linuxdo_connect.redirect_url", "")
	viper.SetDefault("linuxdo_connect.frontend_redirect_url", "/auth/linuxdo/callback")
	viper.SetDefault("linuxdo_connect.token_auth_method", "client_secret_post")
	viper.SetDefault("linuxdo_connect.use_pkce", false)
	viper.SetDefault("linuxdo_connect.userinfo_email_path", "")
	viper.SetDefault("linuxdo_connect.userinfo_id_path", "")
	viper.SetDefault("linuxdo_connect.userinfo_username_path", "")

	// WeChat Connect OAuth login.
	viper.SetDefault("wechat_connect.enabled", false)
	viper.SetDefault("wechat_connect.app_id", "")
	viper.SetDefault("wechat_connect.app_secret", "")
	viper.SetDefault("wechat_connect.open_app_id", "")
	viper.SetDefault("wechat_connect.open_app_secret", "")
	viper.SetDefault("wechat_connect.mp_app_id", "")
	viper.SetDefault("wechat_connect.mp_app_secret", "")
	viper.SetDefault("wechat_connect.mobile_app_id", "")
	viper.SetDefault("wechat_connect.mobile_app_secret", "")
	viper.SetDefault("wechat_connect.open_enabled", false)
	viper.SetDefault("wechat_connect.mp_enabled", false)
	viper.SetDefault("wechat_connect.mobile_enabled", false)
	viper.SetDefault("wechat_connect.mode", defaultWeChatConnectMode)
	viper.SetDefault("wechat_connect.scopes", defaultWeChatConnectScopes)
	viper.SetDefault("wechat_connect.redirect_url", "")
	viper.SetDefault("wechat_connect.frontend_redirect_url", defaultWeChatConnectFrontendRedirect)

	// Generic OIDC login.
	viper.SetDefault("oidc_connect.enabled", false)
	viper.SetDefault("oidc_connect.provider_name", "OIDC")
	viper.SetDefault("oidc_connect.client_id", "")
	viper.SetDefault("oidc_connect.client_secret", "")
	viper.SetDefault("oidc_connect.issuer_url", "")
	viper.SetDefault("oidc_connect.discovery_url", "")
	viper.SetDefault("oidc_connect.authorize_url", "")
	viper.SetDefault("oidc_connect.token_url", "")
	viper.SetDefault("oidc_connect.userinfo_url", "")
	viper.SetDefault("oidc_connect.jwks_url", "")
	viper.SetDefault("oidc_connect.scopes", "openid email profile")
	viper.SetDefault("oidc_connect.redirect_url", "")
	viper.SetDefault("oidc_connect.frontend_redirect_url", "/auth/oidc/callback")
	viper.SetDefault("oidc_connect.token_auth_method", "client_secret_post")
	viper.SetDefault("oidc_connect.use_pkce", true)
	viper.SetDefault("oidc_connect.validate_id_token", true)
	viper.SetDefault("oidc_connect.allowed_signing_algs", "RS256,ES256,PS256")
	viper.SetDefault("oidc_connect.clock_skew_seconds", 120)
	viper.SetDefault("oidc_connect.require_email_verified", false)
	viper.SetDefault("oidc_connect.userinfo_email_path", "")
	viper.SetDefault("oidc_connect.userinfo_id_path", "")
	viper.SetDefault("oidc_connect.userinfo_username_path", "")

	// DingTalk Connect OAuth login.
	viper.SetDefault("dingtalk_connect.enabled", false)
	viper.SetDefault("dingtalk_connect.authorize_url", "https://login.dingtalk.com/oauth2/auth")
	viper.SetDefault("dingtalk_connect.token_url", "https://api.dingtalk.com/v1.0/oauth2/userAccessToken")
	viper.SetDefault("dingtalk_connect.userinfo_url", "https://api.dingtalk.com/v1.0/contact/users/me")
	viper.SetDefault("dingtalk_connect.scopes", "openid")
	viper.SetDefault("dingtalk_connect.frontend_redirect_url", "/auth/dingtalk/callback")
	viper.SetDefault("dingtalk_connect.dingtalk_app_kind", "internal_app")
	viper.SetDefault("dingtalk_connect.app_type", "public")
	viper.SetDefault("dingtalk_connect.corp_restriction_policy", "none")
	viper.SetDefault("dingtalk_connect.require_email", true)
	viper.SetDefault("dingtalk_connect.username_overwrite_policy", "if_empty")

	// Database
	viper.SetDefault("database.host", "localhost")
	viper.SetDefault("database.port", 5432)
	viper.SetDefault("database.user", "postgres")
	viper.SetDefault("database.password", "postgres")
	viper.SetDefault("database.dbname", "tokenrouter")
	viper.SetDefault("database.sslmode", "prefer")
	viper.SetDefault("database.max_open_conns", 256)
	viper.SetDefault("database.max_idle_conns", 128)
	viper.SetDefault("database.conn_max_lifetime_minutes", 30)
	viper.SetDefault("database.conn_max_idle_time_minutes", 5)

	// Redis
	viper.SetDefault("redis.host", "localhost")
	viper.SetDefault("redis.port", 6379)
	viper.SetDefault("redis.username", "")
	viper.SetDefault("redis.password", "")
	viper.SetDefault("redis.db", 0)
	viper.SetDefault("redis.dial_timeout_seconds", 5)
	viper.SetDefault("redis.read_timeout_seconds", 3)
	viper.SetDefault("redis.write_timeout_seconds", 3)
	viper.SetDefault("redis.pool_size", 1024)
	viper.SetDefault("redis.min_idle_conns", 128)
	viper.SetDefault("redis.enable_tls", false)

	// Batch Image queue
	viper.SetDefault("batch_image.enabled", false)
	viper.SetDefault("batch_image.max_items_per_job_default", 200)
	viper.SetDefault("batch_image.max_items_per_job_trial", 50)
	viper.SetDefault("batch_image.max_output_images_per_job", 200)
	viper.SetDefault("batch_image.max_output_images_per_item", 4)
	viper.SetDefault("batch_image.max_prompt_chars_per_item", 8000)
	viper.SetDefault("batch_image.max_reference_images_per_job", 1000)
	viper.SetDefault("batch_image.max_reference_inline_bytes_per_job", 134217728)
	viper.SetDefault("batch_image.default_response_mime_type", "image/png")
	viper.SetDefault("batch_image.default_image_size", "1K")
	viper.SetDefault("batch_image.max_download_items_zip", 200)
	viper.SetDefault("batch_image.max_download_bytes_per_request", 536870912)
	viper.SetDefault("batch_image.max_download_duration_seconds", 600)
	viper.SetDefault("batch_image.max_download_concurrency_per_user", 1)
	viper.SetDefault("batch_image.input_retention_after_terminal_hours", 24)
	viper.SetDefault("batch_image.output_retention_after_terminal_hours", 72)
	viper.SetDefault("batch_image.output_retention_max_days", 7)
	viper.SetDefault("batch_image.cleanup_interval_minutes", 30)
	viper.SetDefault("batch_image.cleanup_batch_size", 100)
	viper.SetDefault("batch_image.queue_enabled", false)
	viper.SetDefault("batch_image.queue_ready_key", "batch_image:queue:ready")
	viper.SetDefault("batch_image.queue_delayed_key", "batch_image:queue:delayed")
	viper.SetDefault("batch_image.queue_active_key", "batch_image:queue:active")
	viper.SetDefault("batch_image.inflight_key_prefix", "batch_image:queue:inflight:")
	viper.SetDefault("batch_image.lock_key_prefix", "batch_image:queue:lock:")
	viper.SetDefault("batch_image.idempotency_key_prefix", "batch_image:queue:idem:")
	viper.SetDefault("batch_image.inflight_ttl_seconds", 604800)
	viper.SetDefault("batch_image.job_lock_ttl_seconds", 300)
	viper.SetDefault("batch_image.default_requeue_delay_seconds", 30)
	viper.SetDefault("batch_image.error_retry_delay_seconds", 60)
	viper.SetDefault("batch_image.lock_conflict_delay_seconds", 5)
	viper.SetDefault("batch_image.stale_active_after_seconds", 600)
	viper.SetDefault("batch_image.delayed_mover_interval_seconds", 5)
	viper.SetDefault("batch_image.recovery_interval_seconds", 300)
	viper.SetDefault("batch_image.delayed_move_limit", 100)
	viper.SetDefault("batch_image.recover_limit", 100)
	viper.SetDefault("batch_image.vertex_enabled", false)
	viper.SetDefault("batch_image.vertex_project_id", "")
	viper.SetDefault("batch_image.vertex_location", "global")
	viper.SetDefault("batch_image.vertex_managed_gcs_bucket", "")
	viper.SetDefault("batch_image.vertex_managed_gcs_prefix", "batch-image/{env}/{batch_id}")
	viper.SetDefault("batch_image.vertex_input_retention_hours", 24)
	viper.SetDefault("batch_image.vertex_output_retention_hours", 72)
	viper.SetDefault("batch_image.vertex_batch_prediction_base_url", "")
	viper.SetDefault("batch_image.vertex_gcs_base_url", "")

	// Creative Studio.
	viper.SetDefault("creative.enabled", true)
	viper.SetDefault("creative.queue_enabled", true)
	viper.SetDefault("creative.transient_ttl_seconds", 1800)
	viper.SetDefault("creative.max_asset_bytes", 33554432)
	viper.SetDefault("creative.max_total_input_bytes", 67108864)
	viper.SetDefault("creative.max_prompt_chars", 8000)
	viper.SetDefault("creative.default_response_mime_type", "image/png")
	viper.SetDefault("creative.default_image_size", "1K")
	viper.SetDefault("creative.queue_ready_key", "creative:queue:ready")
	viper.SetDefault("creative.queue_delayed_key", "creative:queue:delayed")
	viper.SetDefault("creative.queue_active_key", "creative:queue:active")
	viper.SetDefault("creative.inflight_key_prefix", "creative:queue:inflight:")
	viper.SetDefault("creative.lock_key_prefix", "creative:queue:lock:")
	viper.SetDefault("creative.inflight_ttl_seconds", 604800)
	viper.SetDefault("creative.job_lock_ttl_seconds", 300)
	viper.SetDefault("creative.default_requeue_delay_seconds", 30)
	viper.SetDefault("creative.error_retry_delay_seconds", 60)
	viper.SetDefault("creative.lock_conflict_delay_seconds", 5)
	viper.SetDefault("creative.stale_active_after_seconds", 600)
	viper.SetDefault("creative.delayed_mover_interval_seconds", 5)
	viper.SetDefault("creative.recovery_interval_seconds", 300)
	viper.SetDefault("creative.delayed_move_limit", 100)
	viper.SetDefault("creative.recover_limit", 100)
	viper.SetDefault("creative.execute_timeout_seconds", 300)
	viper.SetDefault("creative.max_execute_attempts", 3)

	// Ops
	viper.SetDefault("ops.enabled", true)
	viper.SetDefault("ops.cleanup.enabled", true)
	viper.SetDefault("ops.cleanup.schedule", "0 3 * * *")
	viper.SetDefault("ops.cleanup.batch_size", 1000)
	viper.SetDefault("ops.cleanup.batch_pause_ms", 200)
	// Retain 30 days across Ops datasets by default.
	viper.SetDefault("request_log.retention_days", 30)
	viper.SetDefault("ops.cleanup.error_log_retention_days", 30)
	viper.SetDefault("ops.cleanup.system_log_retention_days", 30)
	viper.SetDefault("ops.cleanup.minute_metrics_retention_days", 30)
	viper.SetDefault("ops.cleanup.hourly_metrics_retention_days", 30)
	viper.SetDefault("ops.aggregation.enabled", true)
	viper.SetDefault("ops.metrics_collector_cache.enabled", true)
	// TTL should be slightly larger than collection interval (1m) to maximize cross-replica cache hits.
	viper.SetDefault("ops.metrics_collector_cache.ttl", 65*time.Second)

	// JWT
	viper.SetDefault("jwt.secret", "")
	viper.SetDefault("jwt.expire_hour", 24)
	viper.SetDefault("jwt.access_token_expire_minutes", 0) // Zero uses expire_hour.
	viper.SetDefault("jwt.refresh_token_expire_days", 30)  // 30-day refresh-token lifetime.
	viper.SetDefault("jwt.refresh_window_minutes", 2)      // Permit refresh two minutes before expiry.

	// TOTP
	viper.SetDefault("totp.encryption_key", "")

	// Default
	// Admin credentials are created via the setup flow (web wizard / CLI / AUTO_SETUP).
	// Do not ship fixed defaults here to avoid insecure "known credentials" in production.
	viper.SetDefault("default.admin_email", "")
	viper.SetDefault("default.admin_password", "")
	viper.SetDefault("default.user_concurrency", 5)
	viper.SetDefault("default.user_balance", 0)
	viper.SetDefault("default.api_key_prefix", "sk-")
	viper.SetDefault("default.rate_multiplier", 1.0)

	// RateLimit
	viper.SetDefault("rate_limit.overload_cooldown_minutes", 10)
	viper.SetDefault("rate_limit.oauth_401_cooldown_minutes", 10)

	// Pricing uses the models.dev catalog and local supplements for additional billing dimensions.
	viper.SetDefault("pricing.remote_url", "https://models.dev/catalog.json?type=all")
	viper.SetDefault("pricing.data_dir", "./data")
	viper.SetDefault("pricing.fallback_file", "")
	viper.SetDefault("pricing.check_interval_minutes", 10)

	// Default time zone for local-day statistics.
	viper.SetDefault("timezone", "Asia/Shanghai")
	viper.SetDefault("team.enabled", true)
	viper.SetDefault("team.self_service_enabled", true)
	viper.SetDefault("team.default_member_limit", 10)

	// API Key auth cache
	viper.SetDefault("api_key_auth_cache.l1_size", 65535)
	viper.SetDefault("api_key_auth_cache.l1_ttl_seconds", 15)
	viper.SetDefault("api_key_auth_cache.l2_ttl_seconds", 300)
	viper.SetDefault("api_key_auth_cache.negative_ttl_seconds", 30)
	viper.SetDefault("api_key_auth_cache.jitter_percent", 10)
	viper.SetDefault("api_key_auth_cache.singleflight", true)
	viper.SetDefault("api_key_auth_cache.lookup_concurrency", 64)
	viper.SetDefault("api_key_auth_cache.invalid_abuse.enabled", true)
	viper.SetDefault("api_key_auth_cache.invalid_abuse.threshold", 120)
	viper.SetDefault("api_key_auth_cache.invalid_abuse.window_seconds", 60)
	viper.SetDefault("api_key_auth_cache.invalid_abuse.block_seconds", 60)
	viper.SetDefault("api_key_auth_cache.invalid_abuse.capacity", 16384)

	// Subscription auth L1 cache
	viper.SetDefault("subscription_cache.l1_size", 16384)
	viper.SetDefault("subscription_cache.l1_ttl_seconds", 10)
	viper.SetDefault("subscription_cache.jitter_percent", 10)

	// Dashboard cache
	viper.SetDefault("dashboard_cache.enabled", true)
	viper.SetDefault("dashboard_cache.key_prefix", "tokenrouter:")
	viper.SetDefault("dashboard_cache.stats_fresh_ttl_seconds", 15)
	viper.SetDefault("dashboard_cache.stats_ttl_seconds", 30)
	viper.SetDefault("dashboard_cache.stats_refresh_timeout_seconds", 30)

	// Dashboard aggregation
	viper.SetDefault("dashboard_aggregation.enabled", true)
	viper.SetDefault("dashboard_aggregation.interval_seconds", 60)
	viper.SetDefault("dashboard_aggregation.lookback_seconds", 120)
	viper.SetDefault("dashboard_aggregation.backfill_enabled", false)
	viper.SetDefault("dashboard_aggregation.backfill_max_days", 31)
	viper.SetDefault("dashboard_aggregation.retention.usage_logs_days", 90)
	viper.SetDefault("dashboard_aggregation.retention.usage_billing_dedup_days", 365)
	viper.SetDefault("dashboard_aggregation.retention.hourly_days", 180)
	viper.SetDefault("dashboard_aggregation.retention.daily_days", 730)
	viper.SetDefault("dashboard_aggregation.recompute_days", 2)

	// Usage cleanup task
	viper.SetDefault("usage_cleanup.enabled", true)
	viper.SetDefault("usage_cleanup.max_range_days", 31)
	viper.SetDefault("usage_cleanup.batch_size", 5000)
	viper.SetDefault("usage_cleanup.worker_interval_seconds", 10)
	viper.SetDefault("usage_cleanup.task_timeout_seconds", 1800)

	// Idempotency
	viper.SetDefault("idempotency.observe_only", true)
	viper.SetDefault("idempotency.default_ttl_seconds", 86400)
	viper.SetDefault("idempotency.system_operation_ttl_seconds", 3600)
	viper.SetDefault("idempotency.processing_timeout_seconds", 30)
	viper.SetDefault("idempotency.failed_retry_backoff_seconds", 5)
	viper.SetDefault("idempotency.max_stored_response_len", 64*1024)
	viper.SetDefault("idempotency.cleanup_interval_seconds", 60)
	viper.SetDefault("idempotency.cleanup_batch_size", 500)

	// Gateway
	viper.SetDefault("gateway.response_header_timeout", 600) // Allow ten minutes for upstream LLM queueing before response headers.
	viper.SetDefault("gateway.openai_response_header_timeout", 0)
	viper.SetDefault("gateway.openai_compact_model", "")
	viper.SetDefault("gateway.grok_response_header_timeout", 120)
	viper.SetDefault("gateway.openai_first_output_timeout_seconds", 0)
	viper.SetDefault("gateway.openai_high_effort_first_output_timeout_seconds", 0)
	viper.SetDefault("gateway.log_upstream_error_body", true)
	viper.SetDefault("gateway.log_upstream_error_body_max_bytes", 2048)
	viper.SetDefault("gateway.inject_beta_for_apikey", false)
	viper.SetDefault("gateway.failover_on_400", false)
	viper.SetDefault("gateway.max_provider_switches", 10)
	viper.SetDefault("gateway.max_provider_switches_gemini", 3)
	viper.SetDefault("gateway.force_codex_cli", false)
	viper.SetDefault("gateway.codex_image_generation_bridge_enabled", false)
	viper.SetDefault("gateway.openai_passthrough_allow_timeout_headers", false)
	viper.SetDefault("gateway.live.max_session_duration_seconds", 3600)
	// OpenAI Responses WebSocket defaults.
	viper.SetDefault("gateway.openai_ws.client_first_message_timeout_seconds", DefaultOpenAIWSClientFirstMessageTimeoutSeconds)
	viper.SetDefault("gateway.openai_ws.ingress_inter_turn_idle_timeout_seconds", 300)
	viper.SetDefault("gateway.openai_ws.max_ingress_connections_per_api_key", 64)
	viper.SetDefault("gateway.openai_ws.ingress_previous_response_recovery_enabled", true)
	viper.SetDefault("gateway.openai_ws.client_read_limit_bytes", 64*1024*1024)
	viper.SetDefault("gateway.openai_ws.http_bridge_threshold_bytes", 15*1024*1024)
	viper.SetDefault("gateway.openai_ws.max_conns_per_provider", 128)
	viper.SetDefault("gateway.openai_ws.min_idle_per_provider", 4)
	viper.SetDefault("gateway.openai_ws.max_idle_per_provider", 12)
	viper.SetDefault("gateway.openai_ws.dynamic_max_conns_by_provider_concurrency_enabled", true)
	viper.SetDefault("gateway.openai_ws.oauth_max_conns_factor", 1.0)
	viper.SetDefault("gateway.openai_ws.apikey_max_conns_factor", 1.0)
	viper.SetDefault("gateway.openai_ws.dial_timeout_seconds", 10)
	viper.SetDefault("gateway.openai_ws.read_timeout_seconds", 900)
	viper.SetDefault("gateway.openai_ws.write_timeout_seconds", 120)
	viper.SetDefault("gateway.openai_ws.pool_target_utilization", 0.7)
	viper.SetDefault("gateway.openai_ws.queue_limit_per_conn", 64)
	viper.SetDefault("gateway.openai_ws.prewarm_cooldown_ms", 300)
	viper.SetDefault("gateway.advanced_scheduler.lb_top_k", 7)
	viper.SetDefault("gateway.advanced_scheduler.ewma_error_rate_alpha", 0.2)
	viper.SetDefault("gateway.advanced_scheduler.ewma_ttft_alpha", 0.2)
	viper.SetDefault("gateway.openai_ws.sticky_session_ttl_seconds", 3600)
	viper.SetDefault("gateway.openai_ws.session_hash_read_old_fallback", true)
	viper.SetDefault("gateway.openai_ws.session_hash_dual_write_old", true)
	viper.SetDefault("gateway.openai_ws.metadata_bridge_enabled", true)
	viper.SetDefault("gateway.openai_ws.sticky_response_id_ttl_seconds", 3600)
	viper.SetDefault("gateway.openai_ws.sticky_previous_response_ttl_seconds", 3600)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.priority", 1.0)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.load", 1.0)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.queue", 0.7)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.error_rate", 0.8)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.ttft", 0.5)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.reset", 0.0)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.quota_headroom", 0.0)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.previous_response", 5.0)
	viper.SetDefault("gateway.advanced_scheduler.score_weights.session_sticky", 3.0)
	// OpenAI upstream HTTP version policy.
	viper.SetDefault("gateway.openai_http2.enabled", true)
	viper.SetDefault("gateway.openai_http2.allow_proxy_fallback_to_http1", true)
	viper.SetDefault("gateway.openai_http2.fallback_error_threshold", 2)
	viper.SetDefault("gateway.openai_http2.fallback_window_seconds", 60)
	viper.SetDefault("gateway.openai_http2.fallback_ttl_seconds", 600)
	viper.SetDefault("gateway.openai_proxy_stream_circuit.disabled", false)
	viper.SetDefault("gateway.openai_proxy_stream_circuit.failure_threshold", 2)
	viper.SetDefault("gateway.openai_proxy_stream_circuit.window_seconds", 60)
	viper.SetDefault("gateway.openai_proxy_stream_circuit.ttl_seconds", 600)
	// The Grok quota gate applies to scheduling; admin QueryQuota bypasses it.
	// Only providers marked free are gated, so unknown and paid tiers are unaffected.
	viper.SetDefault("gateway.grok.free_quota_soft_gate_enabled", true)
	viper.SetDefault("gateway.grok.password_auth_enabled", false)
	// Default nominal allowance: 500,000 tokens in a rolling 24-hour window.
	viper.SetDefault("gateway.grok.free_quota_token_limit", int64(500_000))
	viper.SetDefault("gateway.grok.free_quota_soft_gate_percent", 95)
	viper.SetDefault("gateway.grok.free_quota_window_hours", 24)
	viper.SetDefault("gateway.grok.free_quota_stats_cache_seconds", 60)
	// Supplier monitoring is disabled by default; manual admin queries remain available.
	viper.SetDefault("gateway.cn_providers.monitor_enabled", false)
	viper.SetDefault("gateway.cn_providers.balance_threshold", 0.5)
	viper.SetDefault("gateway.cn_providers.interval_minutes", 10)
	viper.SetDefault("gateway.cn_providers.concurrency", 4)
	viper.SetDefault("gateway.cn_providers.probe_timeout_seconds", 20)
	viper.SetDefault("gateway.cn_providers.round_timeout_seconds", 300)
	viper.SetDefault("gateway.image_concurrency.enabled", false)
	viper.SetDefault("gateway.image_concurrency.max_concurrent_requests", 0)
	viper.SetDefault("gateway.image_concurrency.overflow_mode", ImageConcurrencyOverflowModeReject)
	viper.SetDefault("gateway.image_concurrency.wait_timeout_seconds", 30)
	viper.SetDefault("gateway.image_concurrency.max_waiting_requests", 100)
	viper.SetDefault("gateway.antigravity_fallback_cooldown_minutes", 1)
	viper.SetDefault("gateway.antigravity_extra_retries", 10)
	viper.SetDefault("gateway.max_body_size", int64(256*1024*1024))
	viper.SetDefault("gateway.text_max_body_size", int64(32*1024*1024))
	viper.SetDefault("gateway.upstream_response_read_max_bytes", DefaultUpstreamResponseReadMaxBytes)
	viper.SetDefault("gateway.models_list_read_max_bytes", DefaultModelsListReadMaxBytes)
	viper.SetDefault("gateway.proxy_probe_response_read_max_bytes", int64(1024*1024))
	viper.SetDefault("gateway.gemini_debug_response_headers", false)
	viper.SetDefault("gateway.connection_pool_isolation", ConnectionPoolIsolationProviderProxy)
	// Upstream HTTP connection-pool defaults.
	viper.SetDefault("gateway.max_idle_conns", 2560)          // Total idle connection limit.
	viper.SetDefault("gateway.max_idle_conns_per_host", 120)  // Idle connection limit per host.
	viper.SetDefault("gateway.max_conns_per_host", 1024)      // Active and idle connection limit per host.
	viper.SetDefault("gateway.idle_conn_timeout_seconds", 90) // Idle timeout, in seconds.
	viper.SetDefault("gateway.max_upstream_clients", 5000)
	viper.SetDefault("gateway.client_idle_ttl_seconds", 900)
	viper.SetDefault("gateway.concurrency_slot_ttl_minutes", 30) // Slot lifetime accommodates long requests.
	viper.SetDefault("gateway.stream_data_interval_timeout", 180)
	viper.SetDefault("gateway.stream_keepalive_interval", 10)
	viper.SetDefault("gateway.image_stream_data_interval_timeout", 900)
	viper.SetDefault("gateway.image_stream_keepalive_interval", 10)
	viper.SetDefault("gateway.image_nonstream_keepalive_interval", 0)
	viper.SetDefault("gateway.max_line_size", 500*1024*1024)
	viper.SetDefault("gateway.scheduling.sticky_session_max_waiting", 3)
	viper.SetDefault("gateway.scheduling.sticky_session_wait_timeout", 120*time.Second)
	viper.SetDefault("gateway.scheduling.fallback_wait_timeout", 30*time.Second)
	viper.SetDefault("gateway.scheduling.fallback_max_waiting", 100)
	viper.SetDefault("gateway.scheduling.fallback_selection_mode", "last_used")
	viper.SetDefault("gateway.scheduling.prefer_soonest_reset", false)
	viper.SetDefault("gateway.scheduling.load_batch_enabled", true)
	viper.SetDefault("gateway.scheduling.load_batch_cache_ttl_ms", 200)
	viper.SetDefault("gateway.scheduling.snapshot_mget_chunk_size", 128)
	viper.SetDefault("gateway.scheduling.snapshot_write_chunk_size", 256)
	viper.SetDefault("gateway.scheduling.slot_cleanup_interval", 30*time.Second)
	viper.SetDefault("gateway.scheduling.db_fallback_enabled", true)
	viper.SetDefault("gateway.scheduling.db_fallback_timeout_seconds", 0)
	viper.SetDefault("gateway.scheduling.db_fallback_max_qps", 0)
	viper.SetDefault("gateway.scheduling.outbox_poll_interval_seconds", 1)
	viper.SetDefault("gateway.scheduling.outbox_lag_warn_seconds", 5)
	viper.SetDefault("gateway.scheduling.outbox_lag_rebuild_seconds", 10)
	viper.SetDefault("gateway.scheduling.outbox_lag_rebuild_failures", 3)
	viper.SetDefault("gateway.scheduling.outbox_backlog_rebuild_rows", 10000)
	viper.SetDefault("gateway.scheduling.full_rebuild_interval_seconds", 300)
	viper.SetDefault("gateway.usage_record.worker_count", 128)
	viper.SetDefault("gateway.usage_record.queue_size", 16384)
	viper.SetDefault("gateway.usage_record.task_timeout_seconds", 5)
	// The default sync policy runs overflow tasks inline after the response is written.
	// sample and drop can discard billing tasks, creating charge and usage_logs reconciliation gaps (issue #3656).
	// Use those policies only when the deployment accepts that loss.
	viper.SetDefault("gateway.usage_record.overflow_policy", UsageRecordOverflowPolicySync)
	viper.SetDefault("gateway.usage_record.overflow_sample_percent", 10)
	viper.SetDefault("gateway.usage_record.auto_scale_enabled", true)
	viper.SetDefault("gateway.usage_record.auto_scale_min_workers", 128)
	viper.SetDefault("gateway.usage_record.auto_scale_max_workers", 512)
	viper.SetDefault("gateway.usage_record.auto_scale_up_queue_percent", 70)
	viper.SetDefault("gateway.usage_record.auto_scale_down_queue_percent", 15)
	viper.SetDefault("gateway.usage_record.auto_scale_up_step", 32)
	viper.SetDefault("gateway.usage_record.auto_scale_down_step", 16)
	viper.SetDefault("gateway.usage_record.auto_scale_check_interval_seconds", 3)
	viper.SetDefault("gateway.usage_record.auto_scale_cooldown_seconds", 10)
	viper.SetDefault("gateway.user_group_rate_cache_ttl_seconds", 30)
	viper.SetDefault("gateway.models_list_cache_ttl_seconds", 15)
	// User-message queue defaults.
	viper.SetDefault("gateway.user_message_queue.enabled", false)
	viper.SetDefault("gateway.user_message_queue.lock_ttl_ms", 120000)
	viper.SetDefault("gateway.user_message_queue.wait_timeout_ms", 30000)
	viper.SetDefault("gateway.user_message_queue.min_delay_ms", 200)
	viper.SetDefault("gateway.user_message_queue.max_delay_ms", 2000)
	viper.SetDefault("gateway.user_message_queue.cleanup_interval_seconds", 60)

	// Enable TLS fingerprint support by default.
	viper.SetDefault("gateway.tls_fingerprint.enabled", true)
	viper.SetDefault("concurrency.ping_interval", 10)

	// TokenRefresh
	viper.SetDefault("token_refresh.enabled", true)
	viper.SetDefault("token_refresh.check_interval_minutes", 5)        // Check every five minutes.
	viper.SetDefault("token_refresh.refresh_before_expiry_hours", 0.5) // Refresh 30 minutes early for one-hour Google tokens.
	viper.SetDefault("token_refresh.max_retries", 3)                   // At most three retries.
	viper.SetDefault("token_refresh.retry_backoff_seconds", 2)         // Two-second base backoff.
	viper.SetDefault("token_refresh.candidate_page_size", 200)
	viper.SetDefault("token_refresh.provider_concurrency", 4)
	viper.SetDefault("token_refresh.provider_qps", 2)
	viper.SetDefault("token_refresh.provider_failure_threshold", 3)
	viper.SetDefault("token_refresh.attempt_timeout_seconds", 15)
	viper.SetDefault("token_refresh.cycle_timeout_seconds", 240)

	// Gemini OAuth - configure via environment variables or config file
	// GEMINI_OAUTH_CLIENT_ID and GEMINI_OAUTH_CLIENT_SECRET
	// Default: uses Gemini CLI public credentials (set via environment)
	viper.SetDefault("gemini.oauth.client_id", "")
	viper.SetDefault("gemini.oauth.client_secret", "")
	viper.SetDefault("gemini.oauth.scopes", "")
	viper.SetDefault("gemini.quota.policy", "")

	setEnvReachableDefaults()
}

// setEnvReachableDefaults registers configuration keys so environment values reach Unmarshal.
//
// viper.Unmarshal decodes keys from AllKeys: defaults, file keys, and BindEnv keys.
// AutomaticEnv overrides values for those registered keys. Builds with
// -tags embed do not enable viper_bind_struct, so keys present only in the example file
// would otherwise decode to zero values in environment-only deployments.
//
// Missing keys decode to zero values; subsystems then apply their own defaults.
func setEnvReachableDefaults() {
	viper.SetDefault("gateway.forced_codex_instructions_template_file", "")
	viper.SetDefault("gateway.session_idle_timeout_minutes", 0)
	viper.SetDefault("gateway.user_message_queue.mode", "")
	viper.SetDefault("update.proxy_url", "")

	// Sticky escape registers effective defaults so an explicit zero error rate is retained
	// and an explicit zero TTFT reaches validation. Files and environment variables can override these values.
	viper.SetDefault("gateway.advanced_scheduler.sticky_escape_enabled", true)
	viper.SetDefault("gateway.advanced_scheduler.sticky_escape_error_rate", 0.5)
	viper.SetDefault("gateway.advanced_scheduler.sticky_escape_ttft_ms", 15000)

	// server.trusted_proxies and security.forwarded_client_ip_headers use BindEnv:
	// load must distinguish configured values from absent values (#4600), while IsSet
	// treats SetDefault entries as configured even when the operator supplies nothing.
	// load parses these two environment variables through os.LookupEnv.
	// BindEnv adds the keys to AllKeys for the environment-reachability check,
	// while leaving IsSet false for absent variables. These complete BindEnv arguments cannot fail.
	_ = viper.BindEnv("server.trusted_proxies", "SERVER_TRUSTED_PROXIES")
	_ = viper.BindEnv("security.forwarded_client_ip_headers", "SECURITY_FORWARDED_CLIENT_IP_HEADERS")

	// Register OAuth client secrets and other login settings so deployments
	// can supply them through environment variables.
	for _, provider := range []string{"github_oauth", "google_oauth"} {
		viper.SetDefault(provider+".enabled", false)
		viper.SetDefault(provider+".client_id", "")
		viper.SetDefault(provider+".client_secret", "")
		viper.SetDefault(provider+".authorize_url", "")
		viper.SetDefault(provider+".token_url", "")
		viper.SetDefault(provider+".userinfo_url", "")
		viper.SetDefault(provider+".emails_url", "")
		viper.SetDefault(provider+".scopes", "")
		viper.SetDefault(provider+".redirect_url", "")
		viper.SetDefault(provider+".frontend_redirect_url", "")
	}

	viper.SetDefault("dingtalk_connect.client_id", "")
	viper.SetDefault("dingtalk_connect.client_secret", "")
	viper.SetDefault("dingtalk_connect.internal_corp_id", "")
	viper.SetDefault("dingtalk_connect.redirect_url", "")
	viper.SetDefault("dingtalk_connect.bypass_registration", false)
	viper.SetDefault("dingtalk_connect.username_attribute_key", "")
	viper.SetDefault("dingtalk_connect.enable_attribute_matching", false)
	viper.SetDefault("dingtalk_connect.enable_attribute_sync", false)
	viper.SetDefault("dingtalk_connect.attribute_sync_fields", []string{})
	viper.SetDefault("dingtalk_connect.attribute_sync_overwrite_policy", "")
	viper.SetDefault("dingtalk_connect.sync_display_name", false)
	viper.SetDefault("dingtalk_connect.sync_display_name_attr_key", "")
	viper.SetDefault("dingtalk_connect.sync_display_name_attr_name", "")
	viper.SetDefault("dingtalk_connect.sync_dept", false)
	viper.SetDefault("dingtalk_connect.sync_dept_attr_key", "")
	viper.SetDefault("dingtalk_connect.sync_dept_attr_name", "")
	viper.SetDefault("dingtalk_connect.sync_corp_email", false)
	viper.SetDefault("dingtalk_connect.sync_corp_email_attr_key", "")
	viper.SetDefault("dingtalk_connect.sync_corp_email_attr_name", "")
}

func (c *Config) Validate() error {
	if c.RequestLog.RetentionDays < 0 {
		return fmt.Errorf("request_log.retention_days must be non-negative")
	}
	c.normalizePricingCatalogSource()

	forwardedClientIPHeaders, err := NormalizeForwardedClientIPHeaders(c.Security.ForwardedClientIPHeaders)
	if err != nil {
		return fmt.Errorf("security.forwarded_client_ip_headers: %w", err)
	}
	c.Security.ForwardedClientIPHeaders = forwardedClientIPHeaders
	c.SetForwardedClientIPSettings(c.Security.TrustForwardedIPForAPIKeyACL, forwardedClientIPHeaders)
	proxyProbeURLs, err := normalizeProxyProbeURLs(c.Security.ProxyProbe.URLs)
	if err != nil {
		return fmt.Errorf("security.proxy_probe.urls: %w", err)
	}
	c.Security.ProxyProbe.URLs = proxyProbeURLs
	if c.Server.ReadHeaderTimeout < 1 || c.Server.ReadHeaderTimeout > 60 {
		return fmt.Errorf("server.read_header_timeout must be between 1 and 60 seconds")
	}
	if c.Server.MaxHeaderBytes < 8*1024 || c.Server.MaxHeaderBytes > 1024*1024 {
		return fmt.Errorf("server.max_header_bytes must be between 8192 and 1048576 bytes")
	}
	if c.Server.IdleTimeout <= 0 {
		return fmt.Errorf("server.idle_timeout must be positive")
	}
	if c.Server.MaxRequestBodySize < 0 {
		return fmt.Errorf("server.max_request_body_size must be non-negative")
	}
	if c.Server.H2C.Enabled {
		if c.Server.H2C.MaxConcurrentStreams == 0 {
			return fmt.Errorf("server.h2c.max_concurrent_streams must be positive")
		}
		if c.Server.H2C.IdleTimeout <= 0 {
			return fmt.Errorf("server.h2c.idle_timeout must be positive")
		}
		if c.Server.H2C.MaxReadFrameSize < 16*1024 || c.Server.H2C.MaxReadFrameSize > 16*1024*1024-1 {
			return fmt.Errorf("server.h2c.max_read_frame_size must be between 16384 and 16777215 bytes")
		}
		if c.Server.H2C.MaxUploadBufferPerConnection < 65535 {
			return fmt.Errorf("server.h2c.max_upload_buffer_per_connection must be at least 65535 bytes")
		}
		if c.Server.H2C.MaxUploadBufferPerStream <= 0 {
			return fmt.Errorf("server.h2c.max_upload_buffer_per_stream must be positive")
		}
	}
	if c.APIKeyAuth.InvalidAbuse.Enabled {
		if c.APIKeyAuth.InvalidAbuse.Threshold < 10 {
			return fmt.Errorf("api_key_auth_cache.invalid_abuse.threshold must be at least 10")
		}
		if c.APIKeyAuth.InvalidAbuse.WindowSeconds < 1 || c.APIKeyAuth.InvalidAbuse.WindowSeconds > 3600 {
			return fmt.Errorf("api_key_auth_cache.invalid_abuse.window_seconds must be between 1 and 3600")
		}
		if c.APIKeyAuth.InvalidAbuse.BlockSeconds < 1 || c.APIKeyAuth.InvalidAbuse.BlockSeconds > 3600 {
			return fmt.Errorf("api_key_auth_cache.invalid_abuse.block_seconds must be between 1 and 3600")
		}
		if c.APIKeyAuth.InvalidAbuse.Capacity < 256 || c.APIKeyAuth.InvalidAbuse.Capacity > 1_000_000 {
			return fmt.Errorf("api_key_auth_cache.invalid_abuse.capacity must be between 256 and 1000000")
		}
	}
	jwtSecret := strings.TrimSpace(c.JWT.Secret)
	if jwtSecret == "" {
		return fmt.Errorf("jwt.secret is required")
	}
	// Measure the JWT secret in UTF-8 bytes.
	// The minimum is 32 bytes.
	if len([]byte(jwtSecret)) < 32 {
		return fmt.Errorf("jwt.secret must be at least 32 bytes")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	case "":
		return fmt.Errorf("log.level is required")
	default:
		return fmt.Errorf("log.level must be one of: debug/info/warn/error")
	}
	switch c.Log.Format {
	case "json", "console":
	case "":
		return fmt.Errorf("log.format is required")
	default:
		return fmt.Errorf("log.format must be one of: json/console")
	}
	switch c.Log.StacktraceLevel {
	case "none", "error", "fatal":
	case "":
		return fmt.Errorf("log.stacktrace_level is required")
	default:
		return fmt.Errorf("log.stacktrace_level must be one of: none/error/fatal")
	}
	if !c.Log.Output.ToStdout && !c.Log.Output.ToFile {
		return fmt.Errorf("log.output.to_stdout and log.output.to_file cannot both be false")
	}
	if c.Log.Rotation.MaxSizeMB <= 0 {
		return fmt.Errorf("log.rotation.max_size_mb must be positive")
	}
	if c.Log.Rotation.MaxBackups < 0 {
		return fmt.Errorf("log.rotation.max_backups must be non-negative")
	}
	if c.Log.Rotation.MaxAgeDays < 0 {
		return fmt.Errorf("log.rotation.max_age_days must be non-negative")
	}
	if c.Log.Sampling.Enabled {
		if c.Log.Sampling.Initial <= 0 {
			return fmt.Errorf("log.sampling.initial must be positive when sampling is enabled")
		}
		if c.Log.Sampling.Thereafter <= 0 {
			return fmt.Errorf("log.sampling.thereafter must be positive when sampling is enabled")
		}
	} else {
		if c.Log.Sampling.Initial < 0 {
			return fmt.Errorf("log.sampling.initial must be non-negative")
		}
		if c.Log.Sampling.Thereafter < 0 {
			return fmt.Errorf("log.sampling.thereafter must be non-negative")
		}
	}

	// Gemini client_id and client_secret must both be set or both be empty.
	// Empty selects the built-in Gemini CLI OAuth client, whose secret comes from the environment.
	geminiClientID := strings.TrimSpace(c.Gemini.OAuth.ClientID)
	geminiClientSecret := strings.TrimSpace(c.Gemini.OAuth.ClientSecret)
	if (geminiClientID == "") != (geminiClientSecret == "") {
		return fmt.Errorf("gemini.oauth.client_id and gemini.oauth.client_secret must be both set or both empty")
	}

	if strings.TrimSpace(c.Server.FrontendURL) != "" {
		if err := ValidateAbsoluteHTTPURL(c.Server.FrontendURL); err != nil {
			return fmt.Errorf("server.frontend_url invalid: %w", err)
		}
		u, err := url.Parse(strings.TrimSpace(c.Server.FrontendURL))
		if err != nil {
			return fmt.Errorf("server.frontend_url invalid: %w", err)
		}
		if u.RawQuery != "" || u.ForceQuery {
			return fmt.Errorf("server.frontend_url invalid: must not include query")
		}
		if u.User != nil {
			return fmt.Errorf("server.frontend_url invalid: must not include userinfo")
		}
		warnIfInsecureURL("server.frontend_url", c.Server.FrontendURL)
	}
	if c.WebAuthn.Enabled {
		c.WebAuthn.RPDisplayName = strings.TrimSpace(c.WebAuthn.RPDisplayName)
		c.WebAuthn.RPID = strings.ToLower(strings.TrimSpace(c.WebAuthn.RPID))
		c.WebAuthn.RPOrigins = normalizeStringSlice(c.WebAuthn.RPOrigins)
		if c.WebAuthn.RPDisplayName == "" {
			return fmt.Errorf("webauthn.rp_display_name is required when passkeys are enabled")
		}
		if c.WebAuthn.RPID == "" {
			return fmt.Errorf("webauthn.rp_id is required when passkeys are enabled")
		}
		if strings.Contains(c.WebAuthn.RPID, "://") || strings.ContainsAny(c.WebAuthn.RPID, "/:") {
			return fmt.Errorf("webauthn.rp_id must be a domain without scheme, port, or path")
		}
		if len(c.WebAuthn.RPOrigins) == 0 {
			return fmt.Errorf("webauthn.rp_origins must contain at least one origin when passkeys are enabled")
		}
		for i, origin := range c.WebAuthn.RPOrigins {
			u, err := url.Parse(origin)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return fmt.Errorf("webauthn.rp_origins contains invalid origin %q", origin)
			}
			if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
				return fmt.Errorf("webauthn.rp_origins entry %q must not include userinfo, path, query, or fragment", origin)
			}
			u.Scheme = strings.ToLower(u.Scheme)
			u.Host = strings.ToLower(u.Host)
			host := strings.ToLower(u.Hostname())
			localDevelopment := host == "localhost" || host == "127.0.0.1" || host == "::1"
			// WebAuthn requires HTTPS, except for local loopback origins used in development.
			if u.Scheme != "https" && (u.Scheme != "http" || !localDevelopment) {
				return fmt.Errorf("webauthn.rp_origins entry %q must use HTTPS (HTTP is allowed only for localhost)", origin)
			}
			if host != c.WebAuthn.RPID && !strings.HasSuffix(host, "."+c.WebAuthn.RPID) {
				return fmt.Errorf("webauthn.rp_origins entry %q is not within relying party ID %q", origin, c.WebAuthn.RPID)
			}
			c.WebAuthn.RPOrigins[i] = u.Scheme + "://" + u.Host
		}
	}
	if c.JWT.ExpireHour <= 0 {
		return fmt.Errorf("jwt.expire_hour must be positive")
	}
	if c.JWT.ExpireHour > 168 {
		return fmt.Errorf("jwt.expire_hour must be <= 168 (7 days)")
	}
	if c.JWT.ExpireHour > 24 {
		slog.Warn("jwt.expire_hour is high; consider shorter expiration for security", "expire_hour", c.JWT.ExpireHour)
	}
	// Validate token lifetimes and the refresh window.
	if c.JWT.AccessTokenExpireMinutes < 0 {
		return fmt.Errorf("jwt.access_token_expire_minutes must be non-negative")
	}
	if c.JWT.AccessTokenExpireMinutes > 720 {
		slog.Warn("jwt.access_token_expire_minutes is high; consider shorter expiration for security", "access_token_expire_minutes", c.JWT.AccessTokenExpireMinutes)
	}
	if c.JWT.RefreshTokenExpireDays <= 0 {
		return fmt.Errorf("jwt.refresh_token_expire_days must be positive")
	}
	if c.JWT.RefreshTokenExpireDays > 90 {
		slog.Warn("jwt.refresh_token_expire_days is high; consider shorter expiration for security", "refresh_token_expire_days", c.JWT.RefreshTokenExpireDays)
	}
	if c.JWT.RefreshWindowMinutes < 0 {
		return fmt.Errorf("jwt.refresh_window_minutes must be non-negative")
	}
	if c.Security.CSP.Enabled && strings.TrimSpace(c.Security.CSP.Policy) == "" {
		return fmt.Errorf("security.csp.policy is required when CSP is enabled")
	}
	if c.LinuxDo.Enabled {
		if strings.TrimSpace(c.LinuxDo.ClientID) == "" {
			return fmt.Errorf("linuxdo_connect.client_id is required when linuxdo_connect.enabled=true")
		}
		if strings.TrimSpace(c.LinuxDo.AuthorizeURL) == "" {
			return fmt.Errorf("linuxdo_connect.authorize_url is required when linuxdo_connect.enabled=true")
		}
		if strings.TrimSpace(c.LinuxDo.TokenURL) == "" {
			return fmt.Errorf("linuxdo_connect.token_url is required when linuxdo_connect.enabled=true")
		}
		if strings.TrimSpace(c.LinuxDo.UserInfoURL) == "" {
			return fmt.Errorf("linuxdo_connect.userinfo_url is required when linuxdo_connect.enabled=true")
		}
		if strings.TrimSpace(c.LinuxDo.RedirectURL) == "" {
			return fmt.Errorf("linuxdo_connect.redirect_url is required when linuxdo_connect.enabled=true")
		}
		method := strings.ToLower(strings.TrimSpace(c.LinuxDo.TokenAuthMethod))
		switch method {
		case "", "client_secret_post", "client_secret_basic", "none":
		default:
			return fmt.Errorf("linuxdo_connect.token_auth_method must be one of: client_secret_post/client_secret_basic/none")
		}
		if (method == "" || method == "client_secret_post" || method == "client_secret_basic") &&
			strings.TrimSpace(c.LinuxDo.ClientSecret) == "" {
			return fmt.Errorf("linuxdo_connect.client_secret is required when linuxdo_connect.enabled=true and token_auth_method is client_secret_post/client_secret_basic")
		}
		if strings.TrimSpace(c.LinuxDo.FrontendRedirectURL) == "" {
			return fmt.Errorf("linuxdo_connect.frontend_redirect_url is required when linuxdo_connect.enabled=true")
		}

		if err := ValidateAbsoluteHTTPURL(c.LinuxDo.AuthorizeURL); err != nil {
			return fmt.Errorf("linuxdo_connect.authorize_url invalid: %w", err)
		}
		if err := ValidateAbsoluteHTTPURL(c.LinuxDo.TokenURL); err != nil {
			return fmt.Errorf("linuxdo_connect.token_url invalid: %w", err)
		}
		if err := ValidateAbsoluteHTTPURL(c.LinuxDo.UserInfoURL); err != nil {
			return fmt.Errorf("linuxdo_connect.userinfo_url invalid: %w", err)
		}
		if err := ValidateAbsoluteHTTPURL(c.LinuxDo.RedirectURL); err != nil {
			return fmt.Errorf("linuxdo_connect.redirect_url invalid: %w", err)
		}
		if err := ValidateFrontendRedirectURL(c.LinuxDo.FrontendRedirectURL); err != nil {
			return fmt.Errorf("linuxdo_connect.frontend_redirect_url invalid: %w", err)
		}

		warnIfInsecureURL("linuxdo_connect.authorize_url", c.LinuxDo.AuthorizeURL)
		warnIfInsecureURL("linuxdo_connect.token_url", c.LinuxDo.TokenURL)
		warnIfInsecureURL("linuxdo_connect.userinfo_url", c.LinuxDo.UserInfoURL)
		warnIfInsecureURL("linuxdo_connect.redirect_url", c.LinuxDo.RedirectURL)
		warnIfInsecureURL("linuxdo_connect.frontend_redirect_url", c.LinuxDo.FrontendRedirectURL)
	}
	if c.WeChat.Enabled {
		weChat := c.WeChat
		normalizeWeChatConnectConfig(&weChat)

		if weChat.OpenEnabled {
			if strings.TrimSpace(weChat.OpenAppID) == "" {
				return fmt.Errorf("wechat_connect.open_app_id is required when wechat_connect.open_enabled=true")
			}
			if strings.TrimSpace(weChat.OpenAppSecret) == "" {
				return fmt.Errorf("wechat_connect.open_app_secret is required when wechat_connect.open_enabled=true")
			}
		}
		if weChat.MPEnabled {
			if strings.TrimSpace(weChat.MPAppID) == "" {
				return fmt.Errorf("wechat_connect.mp_app_id is required when wechat_connect.mp_enabled=true")
			}
			if strings.TrimSpace(weChat.MPAppSecret) == "" {
				return fmt.Errorf("wechat_connect.mp_app_secret is required when wechat_connect.mp_enabled=true")
			}
		}
		if weChat.MobileEnabled {
			if strings.TrimSpace(weChat.MobileAppID) == "" {
				return fmt.Errorf("wechat_connect.mobile_app_id is required when wechat_connect.mobile_enabled=true")
			}
			if strings.TrimSpace(weChat.MobileAppSecret) == "" {
				return fmt.Errorf("wechat_connect.mobile_app_secret is required when wechat_connect.mobile_enabled=true")
			}
		}
		if v := strings.TrimSpace(weChat.RedirectURL); v != "" {
			if err := ValidateAbsoluteHTTPURL(v); err != nil {
				return fmt.Errorf("wechat_connect.redirect_url invalid: %w", err)
			}
			warnIfInsecureURL("wechat_connect.redirect_url", v)
		}
		if err := ValidateFrontendRedirectURL(weChat.FrontendRedirectURL); err != nil {
			return fmt.Errorf("wechat_connect.frontend_redirect_url invalid: %w", err)
		}
		warnIfInsecureURL("wechat_connect.frontend_redirect_url", weChat.FrontendRedirectURL)
	}
	if c.OIDC.Enabled {
		if strings.TrimSpace(c.OIDC.ClientID) == "" {
			return fmt.Errorf("oidc_connect.client_id is required when oidc_connect.enabled=true")
		}
		if strings.TrimSpace(c.OIDC.IssuerURL) == "" {
			return fmt.Errorf("oidc_connect.issuer_url is required when oidc_connect.enabled=true")
		}
		if strings.TrimSpace(c.OIDC.RedirectURL) == "" {
			return fmt.Errorf("oidc_connect.redirect_url is required when oidc_connect.enabled=true")
		}
		if strings.TrimSpace(c.OIDC.FrontendRedirectURL) == "" {
			return fmt.Errorf("oidc_connect.frontend_redirect_url is required when oidc_connect.enabled=true")
		}
		if !scopeContainsOpenID(c.OIDC.Scopes) {
			return fmt.Errorf("oidc_connect.scopes must contain openid")
		}

		method := strings.ToLower(strings.TrimSpace(c.OIDC.TokenAuthMethod))
		switch method {
		case "", "client_secret_post", "client_secret_basic", "none":
		default:
			return fmt.Errorf("oidc_connect.token_auth_method must be one of: client_secret_post/client_secret_basic/none")
		}
		if (method == "" || method == "client_secret_post" || method == "client_secret_basic") &&
			strings.TrimSpace(c.OIDC.ClientSecret) == "" {
			return fmt.Errorf("oidc_connect.client_secret is required when oidc_connect.enabled=true and token_auth_method is client_secret_post/client_secret_basic")
		}
		if c.OIDC.ClockSkewSeconds < 0 || c.OIDC.ClockSkewSeconds > 600 {
			return fmt.Errorf("oidc_connect.clock_skew_seconds must be between 0 and 600")
		}
		if c.OIDC.ValidateIDToken && strings.TrimSpace(c.OIDC.AllowedSigningAlgs) == "" {
			return fmt.Errorf("oidc_connect.allowed_signing_algs is required when oidc_connect.validate_id_token=true")
		}

		if err := ValidateAbsoluteHTTPURL(c.OIDC.IssuerURL); err != nil {
			return fmt.Errorf("oidc_connect.issuer_url invalid: %w", err)
		}
		if v := strings.TrimSpace(c.OIDC.DiscoveryURL); v != "" {
			if err := ValidateAbsoluteHTTPURL(v); err != nil {
				return fmt.Errorf("oidc_connect.discovery_url invalid: %w", err)
			}
		}
		if v := strings.TrimSpace(c.OIDC.AuthorizeURL); v != "" {
			if err := ValidateAbsoluteHTTPURL(v); err != nil {
				return fmt.Errorf("oidc_connect.authorize_url invalid: %w", err)
			}
		}
		if v := strings.TrimSpace(c.OIDC.TokenURL); v != "" {
			if err := ValidateAbsoluteHTTPURL(v); err != nil {
				return fmt.Errorf("oidc_connect.token_url invalid: %w", err)
			}
		}
		if v := strings.TrimSpace(c.OIDC.UserInfoURL); v != "" {
			if err := ValidateAbsoluteHTTPURL(v); err != nil {
				return fmt.Errorf("oidc_connect.userinfo_url invalid: %w", err)
			}
		}
		if v := strings.TrimSpace(c.OIDC.JWKSURL); v != "" {
			if err := ValidateAbsoluteHTTPURL(v); err != nil {
				return fmt.Errorf("oidc_connect.jwks_url invalid: %w", err)
			}
		}
		if err := ValidateAbsoluteHTTPURL(c.OIDC.RedirectURL); err != nil {
			return fmt.Errorf("oidc_connect.redirect_url invalid: %w", err)
		}
		if err := ValidateFrontendRedirectURL(c.OIDC.FrontendRedirectURL); err != nil {
			return fmt.Errorf("oidc_connect.frontend_redirect_url invalid: %w", err)
		}

		warnIfInsecureURL("oidc_connect.issuer_url", c.OIDC.IssuerURL)
		warnIfInsecureURL("oidc_connect.discovery_url", c.OIDC.DiscoveryURL)
		warnIfInsecureURL("oidc_connect.authorize_url", c.OIDC.AuthorizeURL)
		warnIfInsecureURL("oidc_connect.token_url", c.OIDC.TokenURL)
		warnIfInsecureURL("oidc_connect.userinfo_url", c.OIDC.UserInfoURL)
		warnIfInsecureURL("oidc_connect.jwks_url", c.OIDC.JWKSURL)
		warnIfInsecureURL("oidc_connect.redirect_url", c.OIDC.RedirectURL)
		warnIfInsecureURL("oidc_connect.frontend_redirect_url", c.OIDC.FrontendRedirectURL)
	}
	if c.Billing.CircuitBreaker.Enabled {
		if c.Billing.CircuitBreaker.FailureThreshold <= 0 {
			return fmt.Errorf("billing.circuit_breaker.failure_threshold must be positive")
		}
		if c.Billing.CircuitBreaker.ResetTimeoutSeconds <= 0 {
			return fmt.Errorf("billing.circuit_breaker.reset_timeout_seconds must be positive")
		}
		if c.Billing.CircuitBreaker.HalfOpenRequests <= 0 {
			return fmt.Errorf("billing.circuit_breaker.half_open_requests must be positive")
		}
	}
	if c.Billing.MinimumBalanceReserve < 0 {
		return fmt.Errorf("billing.minimum_balance_reserve must be non-negative")
	}
	if c.Database.MaxOpenConns <= 0 {
		return fmt.Errorf("database.max_open_conns must be positive")
	}
	if c.Database.MaxIdleConns < 0 {
		return fmt.Errorf("database.max_idle_conns must be non-negative")
	}
	if c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("database.max_idle_conns cannot exceed database.max_open_conns")
	}
	if c.Database.ConnMaxLifetimeMinutes < 0 {
		return fmt.Errorf("database.conn_max_lifetime_minutes must be non-negative")
	}
	if c.Database.ConnMaxIdleTimeMinutes < 0 {
		return fmt.Errorf("database.conn_max_idle_time_minutes must be non-negative")
	}
	if c.Redis.DialTimeoutSeconds <= 0 {
		return fmt.Errorf("redis.dial_timeout_seconds must be positive")
	}
	if c.Redis.ReadTimeoutSeconds <= 0 {
		return fmt.Errorf("redis.read_timeout_seconds must be positive")
	}
	if c.Redis.WriteTimeoutSeconds <= 0 {
		return fmt.Errorf("redis.write_timeout_seconds must be positive")
	}
	if c.Redis.PoolSize <= 0 {
		return fmt.Errorf("redis.pool_size must be positive")
	}
	if c.Redis.MinIdleConns < 0 {
		return fmt.Errorf("redis.min_idle_conns must be non-negative")
	}
	if c.Redis.MinIdleConns > c.Redis.PoolSize {
		return fmt.Errorf("redis.min_idle_conns cannot exceed redis.pool_size")
	}
	if c.BatchImage.QueueEnabled {
		if strings.TrimSpace(c.BatchImage.QueueReadyKey) == "" {
			return fmt.Errorf("batch_image.queue_ready_key must not be empty")
		}
		if strings.TrimSpace(c.BatchImage.QueueDelayedKey) == "" {
			return fmt.Errorf("batch_image.queue_delayed_key must not be empty")
		}
		if strings.TrimSpace(c.BatchImage.QueueActiveKey) == "" {
			return fmt.Errorf("batch_image.queue_active_key must not be empty")
		}
		if strings.TrimSpace(c.BatchImage.InflightKeyPrefix) == "" {
			return fmt.Errorf("batch_image.inflight_key_prefix must not be empty")
		}
		if strings.TrimSpace(c.BatchImage.LockKeyPrefix) == "" {
			return fmt.Errorf("batch_image.lock_key_prefix must not be empty")
		}
		if c.BatchImage.InflightTTLSeconds <= 0 {
			return fmt.Errorf("batch_image.inflight_ttl_seconds must be positive")
		}
		if c.BatchImage.JobLockTTLSeconds <= 0 {
			return fmt.Errorf("batch_image.job_lock_ttl_seconds must be positive")
		}
		if c.BatchImage.StaleActiveAfterSeconds <= 0 {
			return fmt.Errorf("batch_image.stale_active_after_seconds must be positive")
		}
		if c.BatchImage.DelayedMoveLimit <= 0 {
			return fmt.Errorf("batch_image.delayed_move_limit must be positive")
		}
		if c.BatchImage.RecoverLimit <= 0 {
			return fmt.Errorf("batch_image.recover_limit must be positive")
		}
	}
	if c.BatchImage.VertexEnabled {
		if strings.TrimSpace(c.BatchImage.VertexManagedGCSBucket) == "" {
			return fmt.Errorf("batch_image.vertex_managed_gcs_bucket must not be empty when vertex is enabled")
		}
		if strings.Contains(c.BatchImage.VertexManagedGCSBucket, "://") {
			return fmt.Errorf("batch_image.vertex_managed_gcs_bucket must be a bucket name, not a URI")
		}
		if strings.TrimSpace(c.BatchImage.VertexLocation) == "" {
			return fmt.Errorf("batch_image.vertex_location must not be empty when vertex is enabled")
		}
		if strings.TrimSpace(c.BatchImage.VertexManagedGCSPrefix) == "" {
			return fmt.Errorf("batch_image.vertex_managed_gcs_prefix must not be empty when vertex is enabled")
		}
		if !strings.Contains(c.BatchImage.VertexManagedGCSPrefix, "{batch_id}") {
			return fmt.Errorf("batch_image.vertex_managed_gcs_prefix must contain {batch_id}")
		}
		if c.BatchImage.VertexInputRetentionHours <= 0 {
			return fmt.Errorf("batch_image.vertex_input_retention_hours must be positive")
		}
		if c.BatchImage.VertexOutputRetentionHours <= 0 {
			return fmt.Errorf("batch_image.vertex_output_retention_hours must be positive")
		}
	}
	// Validate Creative Studio limits and queue settings.
	if c.Creative.Enabled {
		if c.Creative.TransientTTLSeconds <= 0 {
			return fmt.Errorf("creative.transient_ttl_seconds must be positive")
		}
		if c.Creative.MaxAssetBytes <= 0 {
			return fmt.Errorf("creative.max_asset_bytes must be positive")
		}
		if c.Creative.MaxTotalInputBytes <= 0 {
			return fmt.Errorf("creative.max_total_input_bytes must be positive")
		}
		if c.Creative.MaxTotalInputBytes < c.Creative.MaxAssetBytes {
			return fmt.Errorf("creative.max_total_input_bytes cannot be smaller than creative.max_asset_bytes")
		}
		if c.Creative.MaxPromptChars <= 0 {
			return fmt.Errorf("creative.max_prompt_chars must be positive")
		}
	}
	if c.Creative.QueueEnabled {
		if strings.TrimSpace(c.Creative.QueueReadyKey) == "" {
			return fmt.Errorf("creative.queue_ready_key must not be empty")
		}
		if strings.TrimSpace(c.Creative.QueueDelayedKey) == "" {
			return fmt.Errorf("creative.queue_delayed_key must not be empty")
		}
		if strings.TrimSpace(c.Creative.QueueActiveKey) == "" {
			return fmt.Errorf("creative.queue_active_key must not be empty")
		}
		if strings.TrimSpace(c.Creative.InflightKeyPrefix) == "" {
			return fmt.Errorf("creative.inflight_key_prefix must not be empty")
		}
		if strings.TrimSpace(c.Creative.LockKeyPrefix) == "" {
			return fmt.Errorf("creative.lock_key_prefix must not be empty")
		}
		if c.Creative.InflightTTLSeconds <= 0 {
			return fmt.Errorf("creative.inflight_ttl_seconds must be positive")
		}
		if c.Creative.JobLockTTLSeconds <= 0 {
			return fmt.Errorf("creative.job_lock_ttl_seconds must be positive")
		}
		if c.Creative.StaleActiveAfterSeconds <= 0 {
			return fmt.Errorf("creative.stale_active_after_seconds must be positive")
		}
		if c.Creative.DelayedMoveLimit <= 0 {
			return fmt.Errorf("creative.delayed_move_limit must be positive")
		}
		if c.Creative.RecoverLimit <= 0 {
			return fmt.Errorf("creative.recover_limit must be positive")
		}
	}
	if c.Creative.Enabled {
		if c.Creative.ExecuteTimeoutSeconds <= 0 {
			return fmt.Errorf("creative.execute_timeout_seconds must be positive")
		}
		if c.Creative.MaxExecuteAttempts < 1 {
			return fmt.Errorf("creative.max_execute_attempts must be at least 1")
		}
	}
	if c.Dashboard.Enabled {
		if c.Dashboard.StatsFreshTTLSeconds <= 0 {
			return fmt.Errorf("dashboard_cache.stats_fresh_ttl_seconds must be positive")
		}
		if c.Dashboard.StatsTTLSeconds <= 0 {
			return fmt.Errorf("dashboard_cache.stats_ttl_seconds must be positive")
		}
		if c.Dashboard.StatsRefreshTimeoutSeconds <= 0 {
			return fmt.Errorf("dashboard_cache.stats_refresh_timeout_seconds must be positive")
		}
		if c.Dashboard.StatsFreshTTLSeconds > c.Dashboard.StatsTTLSeconds {
			return fmt.Errorf("dashboard_cache.stats_fresh_ttl_seconds must be <= dashboard_cache.stats_ttl_seconds")
		}
	} else {
		if c.Dashboard.StatsFreshTTLSeconds < 0 {
			return fmt.Errorf("dashboard_cache.stats_fresh_ttl_seconds must be non-negative")
		}
		if c.Dashboard.StatsTTLSeconds < 0 {
			return fmt.Errorf("dashboard_cache.stats_ttl_seconds must be non-negative")
		}
		if c.Dashboard.StatsRefreshTimeoutSeconds < 0 {
			return fmt.Errorf("dashboard_cache.stats_refresh_timeout_seconds must be non-negative")
		}
	}
	if c.DashboardAgg.Enabled {
		if c.DashboardAgg.IntervalSeconds <= 0 {
			return fmt.Errorf("dashboard_aggregation.interval_seconds must be positive")
		}
		if c.DashboardAgg.LookbackSeconds < 0 {
			return fmt.Errorf("dashboard_aggregation.lookback_seconds must be non-negative")
		}
		if c.DashboardAgg.BackfillMaxDays < 0 {
			return fmt.Errorf("dashboard_aggregation.backfill_max_days must be non-negative")
		}
		if c.DashboardAgg.BackfillEnabled && c.DashboardAgg.BackfillMaxDays == 0 {
			return fmt.Errorf("dashboard_aggregation.backfill_max_days must be positive")
		}
		if c.DashboardAgg.Retention.UsageLogsDays <= 0 {
			return fmt.Errorf("dashboard_aggregation.retention.usage_logs_days must be positive")
		}
		if c.DashboardAgg.Retention.UsageBillingDedupDays <= 0 {
			return fmt.Errorf("dashboard_aggregation.retention.usage_billing_dedup_days must be positive")
		}
		if c.DashboardAgg.Retention.UsageBillingDedupDays < c.DashboardAgg.Retention.UsageLogsDays {
			return fmt.Errorf("dashboard_aggregation.retention.usage_billing_dedup_days must be greater than or equal to usage_logs_days")
		}
		if c.DashboardAgg.Retention.HourlyDays <= 0 {
			return fmt.Errorf("dashboard_aggregation.retention.hourly_days must be positive")
		}
		if c.DashboardAgg.Retention.DailyDays <= 0 {
			return fmt.Errorf("dashboard_aggregation.retention.daily_days must be positive")
		}
		if c.DashboardAgg.RecomputeDays < 0 {
			return fmt.Errorf("dashboard_aggregation.recompute_days must be non-negative")
		}
	} else {
		if c.DashboardAgg.IntervalSeconds < 0 {
			return fmt.Errorf("dashboard_aggregation.interval_seconds must be non-negative")
		}
		if c.DashboardAgg.LookbackSeconds < 0 {
			return fmt.Errorf("dashboard_aggregation.lookback_seconds must be non-negative")
		}
		if c.DashboardAgg.BackfillMaxDays < 0 {
			return fmt.Errorf("dashboard_aggregation.backfill_max_days must be non-negative")
		}
		if c.DashboardAgg.Retention.UsageLogsDays < 0 {
			return fmt.Errorf("dashboard_aggregation.retention.usage_logs_days must be non-negative")
		}
		if c.DashboardAgg.Retention.UsageBillingDedupDays < 0 {
			return fmt.Errorf("dashboard_aggregation.retention.usage_billing_dedup_days must be non-negative")
		}
		if c.DashboardAgg.Retention.UsageBillingDedupDays > 0 &&
			c.DashboardAgg.Retention.UsageLogsDays > 0 &&
			c.DashboardAgg.Retention.UsageBillingDedupDays < c.DashboardAgg.Retention.UsageLogsDays {
			return fmt.Errorf("dashboard_aggregation.retention.usage_billing_dedup_days must be greater than or equal to usage_logs_days")
		}
		if c.DashboardAgg.Retention.HourlyDays < 0 {
			return fmt.Errorf("dashboard_aggregation.retention.hourly_days must be non-negative")
		}
		if c.DashboardAgg.Retention.DailyDays < 0 {
			return fmt.Errorf("dashboard_aggregation.retention.daily_days must be non-negative")
		}
		if c.DashboardAgg.RecomputeDays < 0 {
			return fmt.Errorf("dashboard_aggregation.recompute_days must be non-negative")
		}
	}
	if c.UsageCleanup.Enabled {
		if c.UsageCleanup.MaxRangeDays <= 0 {
			return fmt.Errorf("usage_cleanup.max_range_days must be positive")
		}
		if c.UsageCleanup.BatchSize <= 0 {
			return fmt.Errorf("usage_cleanup.batch_size must be positive")
		}
		if c.UsageCleanup.WorkerIntervalSeconds <= 0 {
			return fmt.Errorf("usage_cleanup.worker_interval_seconds must be positive")
		}
		if c.UsageCleanup.TaskTimeoutSeconds <= 0 {
			return fmt.Errorf("usage_cleanup.task_timeout_seconds must be positive")
		}
	} else {
		if c.UsageCleanup.MaxRangeDays < 0 {
			return fmt.Errorf("usage_cleanup.max_range_days must be non-negative")
		}
		if c.UsageCleanup.BatchSize < 0 {
			return fmt.Errorf("usage_cleanup.batch_size must be non-negative")
		}
		if c.UsageCleanup.WorkerIntervalSeconds < 0 {
			return fmt.Errorf("usage_cleanup.worker_interval_seconds must be non-negative")
		}
		if c.UsageCleanup.TaskTimeoutSeconds < 0 {
			return fmt.Errorf("usage_cleanup.task_timeout_seconds must be non-negative")
		}
	}
	if c.Idempotency.DefaultTTLSeconds <= 0 {
		return fmt.Errorf("idempotency.default_ttl_seconds must be positive")
	}
	if c.Idempotency.SystemOperationTTLSeconds <= 0 {
		return fmt.Errorf("idempotency.system_operation_ttl_seconds must be positive")
	}
	if c.Idempotency.ProcessingTimeoutSeconds <= 0 {
		return fmt.Errorf("idempotency.processing_timeout_seconds must be positive")
	}
	if c.Idempotency.FailedRetryBackoffSeconds <= 0 {
		return fmt.Errorf("idempotency.failed_retry_backoff_seconds must be positive")
	}
	if c.Idempotency.MaxStoredResponseLen <= 0 {
		return fmt.Errorf("idempotency.max_stored_response_len must be positive")
	}
	if c.Idempotency.CleanupIntervalSeconds <= 0 {
		return fmt.Errorf("idempotency.cleanup_interval_seconds must be positive")
	}
	if c.Idempotency.CleanupBatchSize <= 0 {
		return fmt.Errorf("idempotency.cleanup_batch_size must be positive")
	}
	if c.Gateway.MaxBodySize <= 0 {
		return fmt.Errorf("gateway.max_body_size must be positive")
	}
	if c.Gateway.TextMaxBodySize <= 0 || c.Gateway.TextMaxBodySize > c.Gateway.MaxBodySize {
		return fmt.Errorf("gateway.text_max_body_size must be positive and no greater than gateway.max_body_size")
	}
	if c.Gateway.UpstreamResponseReadMaxBytes <= 0 {
		return fmt.Errorf("gateway.upstream_response_read_max_bytes must be positive")
	}
	if c.Gateway.ModelsListReadMaxBytes <= 0 {
		return fmt.Errorf("gateway.models_list_read_max_bytes must be positive")
	}
	if c.Gateway.ProxyProbeResponseReadMaxBytes <= 0 {
		return fmt.Errorf("gateway.proxy_probe_response_read_max_bytes must be positive")
	}
	if c.Gateway.ResponseHeaderTimeout < 0 {
		return fmt.Errorf("gateway.response_header_timeout must be non-negative")
	}
	if c.Gateway.OpenAIResponseHeaderTimeout < 0 {
		return fmt.Errorf("gateway.openai_response_header_timeout must be non-negative")
	}
	if c.Gateway.GrokResponseHeaderTimeout < 0 || c.Gateway.GrokResponseHeaderTimeout > 1800 {
		return fmt.Errorf("gateway.grok_response_header_timeout must be between 0-1800 seconds")
	}
	if c.Gateway.OpenAIFirstOutputTimeoutSeconds < 0 || c.Gateway.OpenAIFirstOutputTimeoutSeconds > 600 ||
		(c.Gateway.OpenAIFirstOutputTimeoutSeconds > 0 && c.Gateway.OpenAIFirstOutputTimeoutSeconds < 30) {
		return fmt.Errorf("gateway.openai_first_output_timeout_seconds must be 0 or between 30-600 seconds")
	}
	if c.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds < 0 || c.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds > 1800 ||
		(c.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds > 0 && c.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds < 30) {
		return fmt.Errorf("gateway.openai_high_effort_first_output_timeout_seconds must be 0 or between 30-1800 seconds")
	}
	if c.Gateway.Live.MaxSessionDurationSeconds <= 0 {
		c.Gateway.Live.MaxSessionDurationSeconds = 3600
	}
	if strings.TrimSpace(c.Gateway.ConnectionPoolIsolation) != "" {
		switch c.Gateway.ConnectionPoolIsolation {
		case ConnectionPoolIsolationProxy, ConnectionPoolIsolationProvider, ConnectionPoolIsolationProviderProxy:
		default:
			return fmt.Errorf("gateway.connection_pool_isolation must be one of: %s/%s/%s",
				ConnectionPoolIsolationProxy, ConnectionPoolIsolationProvider, ConnectionPoolIsolationProviderProxy)
		}
	}
	if c.Gateway.ImageConcurrency.MaxConcurrentRequests < 0 {
		return fmt.Errorf("gateway.image_concurrency.max_concurrent_requests must be non-negative")
	}
	switch strings.TrimSpace(c.Gateway.ImageConcurrency.OverflowMode) {
	case "", ImageConcurrencyOverflowModeReject, ImageConcurrencyOverflowModeWait:
	default:
		return fmt.Errorf("gateway.image_concurrency.overflow_mode must be one of: %s/%s",
			ImageConcurrencyOverflowModeReject, ImageConcurrencyOverflowModeWait)
	}
	if c.Gateway.ImageConcurrency.WaitTimeoutSeconds < 0 {
		return fmt.Errorf("gateway.image_concurrency.wait_timeout_seconds must be non-negative")
	}
	if c.Gateway.ImageConcurrency.MaxWaitingRequests < 0 {
		return fmt.Errorf("gateway.image_concurrency.max_waiting_requests must be non-negative")
	}
	if c.Gateway.MaxIdleConns <= 0 {
		return fmt.Errorf("gateway.max_idle_conns must be positive")
	}
	if c.Gateway.MaxIdleConnsPerHost <= 0 {
		return fmt.Errorf("gateway.max_idle_conns_per_host must be positive")
	}
	if c.Gateway.MaxConnsPerHost < 0 {
		return fmt.Errorf("gateway.max_conns_per_host must be non-negative")
	}
	if c.Gateway.IdleConnTimeoutSeconds <= 0 {
		return fmt.Errorf("gateway.idle_conn_timeout_seconds must be positive")
	}
	if c.Gateway.IdleConnTimeoutSeconds > 180 {
		slog.Warn("gateway.idle_conn_timeout_seconds is high; consider 60-120 seconds for better connection reuse", "idle_conn_timeout_seconds", c.Gateway.IdleConnTimeoutSeconds)
	}
	if c.Gateway.MaxUpstreamClients <= 0 {
		return fmt.Errorf("gateway.max_upstream_clients must be positive")
	}
	if c.Gateway.ClientIdleTTLSeconds <= 0 {
		return fmt.Errorf("gateway.client_idle_ttl_seconds must be positive")
	}
	if c.Gateway.ConcurrencySlotTTLMinutes <= 0 {
		return fmt.Errorf("gateway.concurrency_slot_ttl_minutes must be positive")
	}
	if c.Gateway.StreamDataIntervalTimeout < 0 {
		return fmt.Errorf("gateway.stream_data_interval_timeout must be non-negative")
	}
	if c.Gateway.StreamDataIntervalTimeout != 0 &&
		(c.Gateway.StreamDataIntervalTimeout < 30 || c.Gateway.StreamDataIntervalTimeout > 300) {
		return fmt.Errorf("gateway.stream_data_interval_timeout must be 0 or between 30-300 seconds")
	}
	if c.Gateway.StreamKeepaliveInterval < 0 {
		return fmt.Errorf("gateway.stream_keepalive_interval must be non-negative")
	}
	if c.Gateway.StreamKeepaliveInterval != 0 &&
		(c.Gateway.StreamKeepaliveInterval < 5 || c.Gateway.StreamKeepaliveInterval > 30) {
		return fmt.Errorf("gateway.stream_keepalive_interval must be 0 or between 5-30 seconds")
	}
	if c.Gateway.ImageStreamDataIntervalTimeout < 0 {
		return fmt.Errorf("gateway.image_stream_data_interval_timeout must be non-negative")
	}
	if c.Gateway.ImageStreamDataIntervalTimeout != 0 &&
		(c.Gateway.ImageStreamDataIntervalTimeout < 60 || c.Gateway.ImageStreamDataIntervalTimeout > 1800) {
		return fmt.Errorf("gateway.image_stream_data_interval_timeout must be 0 or between 60-1800 seconds")
	}
	if c.Gateway.ImageStreamKeepaliveInterval < 0 {
		return fmt.Errorf("gateway.image_stream_keepalive_interval must be non-negative")
	}
	if c.Gateway.ImageStreamKeepaliveInterval != 0 &&
		(c.Gateway.ImageStreamKeepaliveInterval < 5 || c.Gateway.ImageStreamKeepaliveInterval > 60) {
		return fmt.Errorf("gateway.image_stream_keepalive_interval must be 0 or between 5-60 seconds")
	}
	if c.Gateway.ImageNonstreamKeepaliveInterval < 0 {
		return fmt.Errorf("gateway.image_nonstream_keepalive_interval must be non-negative")
	}
	if c.Gateway.ImageNonstreamKeepaliveInterval != 0 &&
		(c.Gateway.ImageNonstreamKeepaliveInterval < 5 || c.Gateway.ImageNonstreamKeepaliveInterval > 60) {
		return fmt.Errorf("gateway.image_nonstream_keepalive_interval must be 0 or between 5-60 seconds")
	}
	// Accept the legacy sticky_previous_response_ttl_seconds key.
	if c.Gateway.OpenAIWS.StickyResponseIDTTLSeconds <= 0 && c.Gateway.OpenAIWS.StickyPreviousResponseTTLSeconds > 0 {
		c.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = c.Gateway.OpenAIWS.StickyPreviousResponseTTLSeconds
	}
	if c.Gateway.OpenAIWS.MaxConnsPerProvider <= 0 {
		return fmt.Errorf("gateway.openai_ws.max_conns_per_provider must be positive")
	}
	if c.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds <= 0 {
		return fmt.Errorf("gateway.openai_ws.client_first_message_timeout_seconds must be positive")
	}
	if c.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds < 0 {
		return fmt.Errorf("gateway.openai_ws.ingress_inter_turn_idle_timeout_seconds must be non-negative")
	}
	if c.Gateway.OpenAIWS.MaxIngressConnectionsPerAPIKey < 0 {
		return fmt.Errorf("gateway.openai_ws.max_ingress_connections_per_api_key must be non-negative")
	}
	if c.Gateway.OpenAIWS.MinIdlePerProvider < 0 {
		return fmt.Errorf("gateway.openai_ws.min_idle_per_provider must be non-negative")
	}
	if c.Gateway.OpenAIWS.MaxIdlePerProvider < 0 {
		return fmt.Errorf("gateway.openai_ws.max_idle_per_provider must be non-negative")
	}
	if c.Gateway.OpenAIWS.MinIdlePerProvider > c.Gateway.OpenAIWS.MaxIdlePerProvider {
		return fmt.Errorf("gateway.openai_ws.min_idle_per_provider must be <= max_idle_per_provider")
	}
	if c.Gateway.OpenAIWS.MaxIdlePerProvider > c.Gateway.OpenAIWS.MaxConnsPerProvider {
		return fmt.Errorf("gateway.openai_ws.max_idle_per_provider must be <= max_conns_per_provider")
	}
	if c.Gateway.OpenAIWS.OAuthMaxConnsFactor <= 0 {
		return fmt.Errorf("gateway.openai_ws.oauth_max_conns_factor must be positive")
	}
	if c.Gateway.OpenAIWS.APIKeyMaxConnsFactor <= 0 {
		return fmt.Errorf("gateway.openai_ws.apikey_max_conns_factor must be positive")
	}
	if c.Gateway.OpenAIWS.DialTimeoutSeconds <= 0 {
		return fmt.Errorf("gateway.openai_ws.dial_timeout_seconds must be positive")
	}
	if c.Gateway.OpenAIWS.ReadTimeoutSeconds <= 0 {
		return fmt.Errorf("gateway.openai_ws.read_timeout_seconds must be positive")
	}
	if c.Gateway.OpenAIWS.WriteTimeoutSeconds <= 0 {
		return fmt.Errorf("gateway.openai_ws.write_timeout_seconds must be positive")
	}
	if c.Gateway.OpenAIWS.PoolTargetUtilization <= 0 || c.Gateway.OpenAIWS.PoolTargetUtilization > 1 {
		return fmt.Errorf("gateway.openai_ws.pool_target_utilization must be within (0,1]")
	}
	if c.Gateway.OpenAIWS.QueueLimitPerConn <= 0 {
		return fmt.Errorf("gateway.openai_ws.queue_limit_per_conn must be positive")
	}
	if c.Gateway.OpenAIWS.PrewarmCooldownMS < 0 {
		return fmt.Errorf("gateway.openai_ws.prewarm_cooldown_ms must be non-negative")
	}
	if c.Gateway.OpenAIWS.ClientReadLimitBytes <= 0 {
		return fmt.Errorf("gateway.openai_ws.client_read_limit_bytes must be positive")
	}
	if c.Gateway.OpenAIWS.HTTPBridgeThresholdBytes <= 0 {
		return fmt.Errorf("gateway.openai_ws.http_bridge_threshold_bytes must be positive")
	}
	if c.Gateway.AdvancedScheduler.LBTopK <= 0 {
		return fmt.Errorf("gateway.advanced_scheduler.lb_top_k must be positive")
	}
	if c.Gateway.AdvancedScheduler.EWMAErrorRateAlpha <= 0 || c.Gateway.AdvancedScheduler.EWMAErrorRateAlpha > 1 ||
		math.IsNaN(c.Gateway.AdvancedScheduler.EWMAErrorRateAlpha) || math.IsInf(c.Gateway.AdvancedScheduler.EWMAErrorRateAlpha, 0) {
		return fmt.Errorf("gateway.advanced_scheduler.ewma_error_rate_alpha must be between 0 and 1")
	}
	if c.Gateway.AdvancedScheduler.EWMATTFTAlpha <= 0 || c.Gateway.AdvancedScheduler.EWMATTFTAlpha > 1 ||
		math.IsNaN(c.Gateway.AdvancedScheduler.EWMATTFTAlpha) || math.IsInf(c.Gateway.AdvancedScheduler.EWMATTFTAlpha, 0) {
		return fmt.Errorf("gateway.advanced_scheduler.ewma_ttft_alpha must be between 0 and 1")
	}
	if c.Gateway.OpenAIWS.StickySessionTTLSeconds <= 0 {
		return fmt.Errorf("gateway.openai_ws.sticky_session_ttl_seconds must be positive")
	}
	if c.Gateway.OpenAIWS.StickyResponseIDTTLSeconds <= 0 {
		return fmt.Errorf("gateway.openai_ws.sticky_response_id_ttl_seconds must be positive")
	}
	if c.Gateway.OpenAIWS.StickyPreviousResponseTTLSeconds < 0 {
		return fmt.Errorf("gateway.openai_ws.sticky_previous_response_ttl_seconds must be non-negative")
	}
	if c.Gateway.OpenAIHTTP2.FallbackErrorThreshold < 0 {
		return fmt.Errorf("gateway.openai_http2.fallback_error_threshold must be non-negative")
	}
	if c.Gateway.OpenAIHTTP2.FallbackWindowSeconds < 0 {
		return fmt.Errorf("gateway.openai_http2.fallback_window_seconds must be non-negative")
	}
	if c.Gateway.OpenAIHTTP2.FallbackTTLSeconds < 0 {
		return fmt.Errorf("gateway.openai_http2.fallback_ttl_seconds must be non-negative")
	}
	if c.Gateway.OpenAIProxyStreamCircuit.FailureThreshold < 0 {
		return fmt.Errorf("gateway.openai_proxy_stream_circuit.failure_threshold must be non-negative")
	}
	if c.Gateway.OpenAIProxyStreamCircuit.WindowSeconds < 0 {
		return fmt.Errorf("gateway.openai_proxy_stream_circuit.window_seconds must be non-negative")
	}
	if c.Gateway.OpenAIProxyStreamCircuit.TTLSeconds < 0 {
		return fmt.Errorf("gateway.openai_proxy_stream_circuit.ttl_seconds must be non-negative")
	}
	weights := c.Gateway.AdvancedScheduler.ScoreWeights
	for _, weight := range []float64{
		weights.Priority, weights.Load, weights.Queue, weights.ErrorRate, weights.TTFT,
		weights.Reset, weights.QuotaHeadroom,
		weights.PreviousResponse, weights.SessionSticky,
	} {
		if weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
			return fmt.Errorf("gateway.advanced_scheduler.score_weights.* must be non-negative and finite")
		}
	}
	weightSum := weights.BaseWeightSum()
	if weightSum <= 0 {
		return fmt.Errorf("gateway.advanced_scheduler.score_weights must not all be zero")
	}
	if math.IsNaN(weightSum) || math.IsInf(weightSum, 0) {
		return fmt.Errorf("gateway.advanced_scheduler.score_weights base-weight sum must be finite")
	}
	if totalWeightSum := weights.TotalWeightSum(); math.IsNaN(totalWeightSum) || math.IsInf(totalWeightSum, 0) {
		return fmt.Errorf("gateway.advanced_scheduler.score_weights total-weight sum must be finite")
	}
	if c.Gateway.AdvancedScheduler.StickyEscapeTTFTMs <= 0 {
		return fmt.Errorf("gateway.advanced_scheduler.sticky_escape_ttft_ms must be positive")
	}
	if c.Gateway.AdvancedScheduler.StickyEscapeErrorRate < 0 || c.Gateway.AdvancedScheduler.StickyEscapeErrorRate > 1 {
		return fmt.Errorf("gateway.advanced_scheduler.sticky_escape_error_rate must be between 0 and 1")
	}
	if c.Gateway.MaxLineSize < 0 {
		return fmt.Errorf("gateway.max_line_size must be non-negative")
	}
	if c.Gateway.MaxLineSize != 0 && c.Gateway.MaxLineSize < 1024*1024 {
		return fmt.Errorf("gateway.max_line_size must be at least 1MB")
	}
	if c.Gateway.UsageRecord.WorkerCount <= 0 {
		return fmt.Errorf("gateway.usage_record.worker_count must be positive")
	}
	if c.Gateway.UsageRecord.QueueSize <= 0 {
		return fmt.Errorf("gateway.usage_record.queue_size must be positive")
	}
	if c.Gateway.UsageRecord.TaskTimeoutSeconds <= 0 {
		return fmt.Errorf("gateway.usage_record.task_timeout_seconds must be positive")
	}
	switch strings.ToLower(strings.TrimSpace(c.Gateway.UsageRecord.OverflowPolicy)) {
	case UsageRecordOverflowPolicyDrop, UsageRecordOverflowPolicySample, UsageRecordOverflowPolicySync:
	default:
		return fmt.Errorf("gateway.usage_record.overflow_policy must be one of: %s/%s/%s",
			UsageRecordOverflowPolicyDrop, UsageRecordOverflowPolicySample, UsageRecordOverflowPolicySync)
	}
	if c.Gateway.UsageRecord.OverflowSamplePercent < 0 || c.Gateway.UsageRecord.OverflowSamplePercent > 100 {
		return fmt.Errorf("gateway.usage_record.overflow_sample_percent must be between 0-100")
	}
	if strings.EqualFold(strings.TrimSpace(c.Gateway.UsageRecord.OverflowPolicy), UsageRecordOverflowPolicySample) &&
		c.Gateway.UsageRecord.OverflowSamplePercent <= 0 {
		return fmt.Errorf("gateway.usage_record.overflow_sample_percent must be positive when overflow_policy=sample")
	}
	if c.Gateway.UsageRecord.AutoScaleEnabled {
		if c.Gateway.UsageRecord.AutoScaleMinWorkers <= 0 {
			return fmt.Errorf("gateway.usage_record.auto_scale_min_workers must be positive")
		}
		if c.Gateway.UsageRecord.AutoScaleMaxWorkers <= 0 {
			return fmt.Errorf("gateway.usage_record.auto_scale_max_workers must be positive")
		}
		if c.Gateway.UsageRecord.AutoScaleMaxWorkers < c.Gateway.UsageRecord.AutoScaleMinWorkers {
			return fmt.Errorf("gateway.usage_record.auto_scale_max_workers must be >= auto_scale_min_workers")
		}
		if c.Gateway.UsageRecord.WorkerCount < c.Gateway.UsageRecord.AutoScaleMinWorkers ||
			c.Gateway.UsageRecord.WorkerCount > c.Gateway.UsageRecord.AutoScaleMaxWorkers {
			return fmt.Errorf("gateway.usage_record.worker_count must be between auto_scale_min_workers and auto_scale_max_workers")
		}
		if c.Gateway.UsageRecord.AutoScaleUpQueuePercent <= 0 || c.Gateway.UsageRecord.AutoScaleUpQueuePercent > 100 {
			return fmt.Errorf("gateway.usage_record.auto_scale_up_queue_percent must be between 1-100")
		}
		if c.Gateway.UsageRecord.AutoScaleDownQueuePercent < 0 || c.Gateway.UsageRecord.AutoScaleDownQueuePercent >= 100 {
			return fmt.Errorf("gateway.usage_record.auto_scale_down_queue_percent must be between 0-99")
		}
		if c.Gateway.UsageRecord.AutoScaleDownQueuePercent >= c.Gateway.UsageRecord.AutoScaleUpQueuePercent {
			return fmt.Errorf("gateway.usage_record.auto_scale_down_queue_percent must be less than auto_scale_up_queue_percent")
		}
		if c.Gateway.UsageRecord.AutoScaleUpStep <= 0 {
			return fmt.Errorf("gateway.usage_record.auto_scale_up_step must be positive")
		}
		if c.Gateway.UsageRecord.AutoScaleDownStep <= 0 {
			return fmt.Errorf("gateway.usage_record.auto_scale_down_step must be positive")
		}
		if c.Gateway.UsageRecord.AutoScaleCheckIntervalSeconds <= 0 {
			return fmt.Errorf("gateway.usage_record.auto_scale_check_interval_seconds must be positive")
		}
		if c.Gateway.UsageRecord.AutoScaleCooldownSeconds < 0 {
			return fmt.Errorf("gateway.usage_record.auto_scale_cooldown_seconds must be non-negative")
		}
	}
	if c.Gateway.UserGroupRateCacheTTLSeconds <= 0 {
		return fmt.Errorf("gateway.user_group_rate_cache_ttl_seconds must be positive")
	}
	if c.Gateway.ModelsListCacheTTLSeconds < 10 || c.Gateway.ModelsListCacheTTLSeconds > 30 {
		return fmt.Errorf("gateway.models_list_cache_ttl_seconds must be between 10-30")
	}
	if c.Gateway.Scheduling.StickySessionMaxWaiting <= 0 {
		return fmt.Errorf("gateway.scheduling.sticky_session_max_waiting must be positive")
	}
	if c.Gateway.Scheduling.StickySessionWaitTimeout <= 0 {
		return fmt.Errorf("gateway.scheduling.sticky_session_wait_timeout must be positive")
	}
	if c.Gateway.Scheduling.FallbackWaitTimeout <= 0 {
		return fmt.Errorf("gateway.scheduling.fallback_wait_timeout must be positive")
	}
	if c.Gateway.Scheduling.FallbackMaxWaiting <= 0 {
		return fmt.Errorf("gateway.scheduling.fallback_max_waiting must be positive")
	}
	if c.Gateway.Scheduling.LoadBatchCacheTTLMS < 0 {
		return fmt.Errorf("gateway.scheduling.load_batch_cache_ttl_ms must be non-negative")
	}
	if c.Gateway.Scheduling.SnapshotMGetChunkSize <= 0 {
		return fmt.Errorf("gateway.scheduling.snapshot_mget_chunk_size must be positive")
	}
	if c.Gateway.Scheduling.SnapshotWriteChunkSize <= 0 {
		return fmt.Errorf("gateway.scheduling.snapshot_write_chunk_size must be positive")
	}
	if c.Gateway.Scheduling.SlotCleanupInterval < 0 {
		return fmt.Errorf("gateway.scheduling.slot_cleanup_interval must be non-negative")
	}
	if c.Gateway.Scheduling.DbFallbackTimeoutSeconds < 0 {
		return fmt.Errorf("gateway.scheduling.db_fallback_timeout_seconds must be non-negative")
	}
	if c.Gateway.Scheduling.DbFallbackMaxQPS < 0 {
		return fmt.Errorf("gateway.scheduling.db_fallback_max_qps must be non-negative")
	}
	if c.Gateway.Scheduling.OutboxPollIntervalSeconds <= 0 {
		return fmt.Errorf("gateway.scheduling.outbox_poll_interval_seconds must be positive")
	}
	if c.Gateway.Scheduling.OutboxLagWarnSeconds < 0 {
		return fmt.Errorf("gateway.scheduling.outbox_lag_warn_seconds must be non-negative")
	}
	if c.Gateway.Scheduling.OutboxLagRebuildSeconds < 0 {
		return fmt.Errorf("gateway.scheduling.outbox_lag_rebuild_seconds must be non-negative")
	}
	if c.Gateway.Scheduling.OutboxLagRebuildFailures <= 0 {
		return fmt.Errorf("gateway.scheduling.outbox_lag_rebuild_failures must be positive")
	}
	if c.Gateway.Scheduling.OutboxBacklogRebuildRows < 0 {
		return fmt.Errorf("gateway.scheduling.outbox_backlog_rebuild_rows must be non-negative")
	}
	if c.Gateway.Scheduling.FullRebuildIntervalSeconds < 0 {
		return fmt.Errorf("gateway.scheduling.full_rebuild_interval_seconds must be non-negative")
	}
	if c.Gateway.Scheduling.OutboxLagWarnSeconds > 0 &&
		c.Gateway.Scheduling.OutboxLagRebuildSeconds > 0 &&
		c.Gateway.Scheduling.OutboxLagRebuildSeconds < c.Gateway.Scheduling.OutboxLagWarnSeconds {
		return fmt.Errorf("gateway.scheduling.outbox_lag_rebuild_seconds must be >= outbox_lag_warn_seconds")
	}
	if c.Ops.MetricsCollectorCache.TTL < 0 {
		return fmt.Errorf("ops.metrics_collector_cache.ttl must be non-negative")
	}
	if c.Ops.Cleanup.ErrorLogRetentionDays < 0 {
		return fmt.Errorf("ops.cleanup.error_log_retention_days must be non-negative")
	}
	if c.Ops.Cleanup.SystemLogRetentionDays < 0 {
		return fmt.Errorf("ops.cleanup.system_log_retention_days must be non-negative")
	}
	if c.Ops.Cleanup.MinuteMetricsRetentionDays < 0 {
		return fmt.Errorf("ops.cleanup.minute_metrics_retention_days must be non-negative")
	}
	if c.Ops.Cleanup.HourlyMetricsRetentionDays < 0 {
		return fmt.Errorf("ops.cleanup.hourly_metrics_retention_days must be non-negative")
	}
	if c.Ops.Cleanup.Enabled && strings.TrimSpace(c.Ops.Cleanup.Schedule) == "" {
		return fmt.Errorf("ops.cleanup.schedule is required when ops.cleanup.enabled=true")
	}
	if c.Ops.Cleanup.BatchSize < 100 || c.Ops.Cleanup.BatchSize > 5000 {
		return fmt.Errorf("ops.cleanup.batch_size must be between 100 and 5000")
	}
	if c.Ops.Cleanup.BatchPauseMS < 1 || c.Ops.Cleanup.BatchPauseMS > 2000 {
		return fmt.Errorf("ops.cleanup.batch_pause_ms must be between 1 and 2000")
	}
	if c.Concurrency.PingInterval < 5 || c.Concurrency.PingInterval > 30 {
		return fmt.Errorf("concurrency.ping_interval must be between 5-30 seconds")
	}
	if c.Gateway.Grok.FreeQuotaSoftGateEnabled {
		if c.Gateway.Grok.FreeQuotaTokenLimit <= 0 {
			return fmt.Errorf("gateway.grok.free_quota_token_limit must be positive")
		}
		if c.Gateway.Grok.FreeQuotaSoftGatePercent < 1 || c.Gateway.Grok.FreeQuotaSoftGatePercent > 100 {
			return fmt.Errorf("gateway.grok.free_quota_soft_gate_percent must be between 1 and 100")
		}
		if c.Gateway.Grok.FreeQuotaWindowHours <= 0 {
			return fmt.Errorf("gateway.grok.free_quota_window_hours must be positive")
		}
	}
	if c.Gateway.Grok.FreeQuotaStatsCacheSeconds < 0 {
		return fmt.Errorf("gateway.grok.free_quota_stats_cache_seconds must be non-negative")
	}
	if err := ValidateDingTalkConfig(c.DingTalk); err != nil {
		return fmt.Errorf("dingtalk_connect: %w", err)
	}
	return nil
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return values
	}
	normalized := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func isWeakJWTSecret(secret string) bool {
	lower := strings.ToLower(strings.TrimSpace(secret))
	if lower == "" {
		return true
	}
	weak := map[string]struct{}{
		"change-me-in-production": {},
		"changeme":                {},
		"secret":                  {},
		"password":                {},
		"123456":                  {},
		"12345678":                {},
		"admin":                   {},
		"jwt-secret":              {},
	}
	_, exists := weak[lower]
	return exists
}

func generateJWTSecret(byteLength int) (string, error) {
	if byteLength <= 0 {
		byteLength = 32
	}
	buf := make([]byte, byteLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// GetServerAddress reads the listen address before full validation, for startup and setup.
// Environment variables take precedence over the file, then defaults.
func GetServerAddress() string {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	configureConfigSource(v.SetConfigFile, v.AddConfigPath)

	// Environment variables can override the file's listen address.
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)

	// This lightweight read ignores file errors; Load reports missing or unreadable explicit files.
	_ = v.ReadInConfig()

	host := v.GetString("server.host")
	port := v.GetInt("server.port")
	return fmt.Sprintf("%s:%d", host, port)
}

// ValidateAbsoluteHTTPURL checks an absolute HTTP(S) URL.
func ValidateAbsoluteHTTPURL(raw string) error { return authconfig.ValidateAbsoluteHTTPURL(raw) }

// ValidateFrontendRedirectURL checks an absolute URL or relative frontend redirect path.
func ValidateFrontendRedirectURL(raw string) error {
	return authconfig.ValidateFrontendRedirectURL(raw)
}

func scopeContainsOpenID(scopes string) bool {
	return slices.Contains(strings.Fields(strings.ToLower(strings.TrimSpace(scopes))), "openid")
}

func warnIfInsecureURL(field, raw string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return
	}
	if strings.EqualFold(u.Scheme, "http") {
		slog.Warn("url uses http scheme; use https in production to avoid token leakage", "field", field)
	}
}

// normalizePricingCatalogSource updates known legacy sources and missing packaged paths in memory.
func (c *Config) normalizePricingCatalogSource() {
	if strings.TrimSpace(c.Pricing.RemoteURL) == "https://models.dev/catalog.json" {
		c.Pricing.RemoteURL = "https://models.dev/catalog.json?type=all"
	}

	source, err := url.Parse(c.Pricing.RemoteURL)
	if err == nil && strings.EqualFold(source.Host, "raw.githubusercontent.com") && source.User == nil && (source.Scheme == "https" || source.Scheme == "http") {
		sourcePath := strings.ToLower(path.Clean(source.Path))
		knownRepository := strings.HasPrefix(sourcePath, "/wei-shaw/model-price-repo/") || strings.HasPrefix(sourcePath, "/berriai/litellm/")
		if knownRepository && strings.HasSuffix(sourcePath, "/model_prices_and_context_window.json") {
			c.Pricing.RemoteURL = "https://models.dev/catalog.json?type=all"
			if !slices.Contains(c.Security.URLAllowlist.PricingHosts, "models.dev") {
				c.Security.URLAllowlist.PricingHosts = append(c.Security.URLAllowlist.PricingHosts, "models.dev")
			}
		}
	}
	fallback := filepath.Clean(c.Pricing.FallbackFile)
	if fallback != "resources/model-pricing/model_prices_and_context_window.json" && fallback != "/app/resources/model-pricing/model_prices_and_context_window.json" {
		return
	}
	if _, err := os.Stat(fallback); os.IsNotExist(err) {
		c.Pricing.FallbackFile = filepath.Join(filepath.Dir(fallback), "model_pricing_supplements.json")
	}
}

// ValidateDingTalkConfig validates DingTalk login settings using identity configuration rules.
func ValidateDingTalkConfig(value DingTalkConnectConfig) error {
	return authconfig.ValidateDingTalkConfig(value)
}
