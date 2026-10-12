package completion

import (
	"context"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

const (
	BillingModeToken              = pricing.BillingModeToken
	BillingModeImage              = pricing.BillingModeImage
	BillingModeVideo              = pricing.BillingModeVideo
	BillingModePerRequest         = pricing.BillingModePerRequest
	BillingTypeBalance            = usage.BillingTypeBalance
	BillingTypeSubscription       = usage.BillingTypeSubscription
	RequestTypeCyberBlocked       = usage.RequestTypeCyberBlocked
	BillingModelSourceRequested   = routing.BillingModelSourceRequested
	BillingModelSourceUpstream    = routing.BillingModelSourceUpstream
	BillingModelSourceGroupMapped = routing.BillingModelSourceGroupMapped
)

var ErrModelPricingUnavailable = pricing.ErrModelPricingUnavailable

// Result contains observed usage and metadata needed for completion processing.
type Result struct {
	// UpstreamResponseModel is the model declared before protocol conversion; empty means absent.
	UpstreamResponseModel string
	// NativeUsage indicates that input and cache counts are already separate billing buckets.
	NativeUsage                                                                  bool
	RequestID, ResponseID, Model, BillingModel, UpstreamModel                    string
	UpstreamRequestID                                                            *string
	Usage                                                                        TokenUsage
	ServiceTier, ReasoningEffort, RequestedReasoningEffort                       *string
	UpstreamResponseServiceTier                                                  string
	Stream, OpenAIWSMode                                                         bool
	Duration                                                                     time.Duration
	FirstTokenMs                                                                 *int
	ImageCount, VideoCount, VideoDurationSeconds, WebSearchCalls, SearchCount    int
	ImageSize, ImageInputSize, ImageOutputSize, ImageSizeSource, VideoResolution string
	ImageOutputSizes                                                             []string
	ImageSizeBreakdown                                                           map[string]int
	AudioUsage                                                                   *AudioUsage
}

// TokenUsage carries protocol usage buckets. Entrypoints determine whether input is total or uncached.
type TokenUsage struct {
	InputTokens, OutputTokens, CacheCreationInputTokens, CacheReadInputTokens         int
	CacheCreation5mTokens, CacheCreation1hTokens, ImageInputTokens, ImageOutputTokens int
	Speed                                                                             string
}
type AudioUsage struct {
	Mode            string
	DurationOrUnits float64
}

// PayerSnapshot captures the funding user and balance-notification data.
type PayerSnapshot struct {
	ID           int64
	Balance      float64
	Notification *billing.UserSummary
}

// ProviderSnapshot captures provider billing and notification data.
type ProviderSnapshot struct {
	CacheTTLOverrideEnabled                                     bool
	CacheTTLOverrideTarget                                      string
	AnthropicOAuthOrSetupToken                                  bool
	Platform                                                    string
	ID                                                          int64
	Type                                                        string
	RateMultiplier                                              float64
	OpenAI, CNProvider, OAuthLike, QuotaEligible, HasQuotaLimit bool
	// CredentialProviderID identifies the parent of a shadow provider.
	CredentialProviderID *int64
	Notification         *billing.QuotaNotifyProvider
}

// KeySnapshot captures funding bindings and actor identity separately from the payer.
type KeySnapshot struct {
	ActorUserPresent                         bool
	ID                                       int64
	Key                                      string
	ActorUserID                              int64
	GroupID, TeamID, PreferredSubscriptionID *int64
	BillingMode                              string
	Quota                                    float64
	HasRateLimits                            bool
	Group                                    *GroupSnapshot
}

// GroupSnapshot contains group fields used by completion billing.
type GroupSnapshot struct {
	ID                                      int64
	RateMultiplier                          float64
	PeakRateEnabled                         bool
	PeakStart, PeakEnd                      string
	PeakRateMultiplier                      float64
	Location                                *time.Location
	FreeOpenAIFast, SupportsOpenAIFast      bool
	WebSearchPricePerCall, SearchPricePer1k *float64
	AudioPrice                              *pricing.AudioPriceConfig
}

// Input contains asynchronous completion data. Call Snapshot before enqueueing.
type Input struct {
	// BillingKey deduplicates financial operations; RequestID correlates usage with the request.
	BillingKey                                                               string
	Result                                                                   *Result
	APIKey                                                                   *KeySnapshot
	User                                                                     *PayerSnapshot
	Provider                                                                 *ProviderSnapshot
	Subscription                                                             *billing.UserSubscription
	InboundEndpoint, UpstreamEndpoint, UserAgent, IPAddress, ClientSessionID string
	RequestID, RequestPayloadHash, CacheOverrideTarget                       string
	RequestedReasoningEffort                                                 *string
	ForceCacheBilling, QuotaUpdates, CyberBlocked, NativeCompactionV2        bool
	PricingAt                                                                time.Time
	PricingUsageFields
}

type (
	PricingUsageFields = routing.PricingUsageFields
	UsageLog           = usage.UsageLog
	UsageTokens        = pricing.UsageTokens
	CostBreakdown      = pricing.CostBreakdown
	ResolvedPricing    = pricing.ResolvedPricing
	CostInput          = billing.CostInput
	PricingInput       = billing.PricingInput
)

type PricingOptions struct{ PricingAt time.Time }

// Store applies each settlement in its own transaction; implementations must not inherit an outer transaction implicitly.
type Store interface {
	Apply(context.Context, *billing.UsageBillingCommand) (*billing.UsageBillingApplyResult, error)
}
type SubscriptionReader interface {
	ResolveUsableSubscriptionForGroup(context.Context, int64, int64) (*billing.UserSubscription, error)
}
type RateReader interface {
	Resolve(context.Context, int64, int64, float64) float64
}
type ProviderReader interface {
	CredentialProvider(context.Context, ProviderSnapshot) (*ProviderSnapshot, error)
}
type (
	HealthObserver  interface{ ResetOpenAI403Counter(context.Context, int64) }
	ModelCandidates interface {
		Candidates(string, ...string) []string
	}
)

type LogWriter interface {
	Create(context.Context, *usage.UsageLog) (bool, error)
}
type BestEffortLogWriter interface {
	CreateBestEffort(context.Context, *usage.UsageLog) error
}
type ProviderStats interface {
	ResolveProviderStats(context.Context, billing.ProviderStatsCostInput) *float64
}

// Effects updates caches and notifications after committed settlement.
type Effects interface {
	ProviderUsed(int64)
	InvalidateAuth(context.Context, string)
	Settled(SettlementInput, *billing.UsageBillingApplyResult)
}

// CacheInjectionPolicy reads the Anthropic one-hour cache TTL injection setting.
type CacheInjectionPolicy interface{ IsAnthropicCacheTTL1hInjectionEnabled(context.Context) bool }

// BillingEvent contains fields for the billing log adapter.
type BillingEvent struct {
	Kind, Component, RequestID, RequestedModel, MappedModel, UpstreamModel, Model, Platform string
	RequestedTier, ObservedTier, BilledTier                                                 string
	Models                                                                                  []string
	KeyID, ProviderID                                                                       int64
	GroupID                                                                                 *int64
	SearchCount                                                                             int
	Err                                                                                     error
}

// Dependencies supplies storage, policy readers, and the billing calculator.
type Dependencies struct {
	RequestRecords func(telemetry.RequestRecord)
	Emit           func(BillingEvent)
	CacheInjection CacheInjectionPolicy
	Calculator     *billing.Calculator
	Prices         *billing.PriceResolver
	ProviderStats  ProviderStats
	Funds          Store
	Subscriptions  SubscriptionReader
	Rates          RateReader
	Providers      ProviderReader
	Health         HealthObserver
	Models         ModelCandidates
	Logs           LogWriter
	Effects        Effects
	Observe        func(string, string)
}
type RecorderOptions struct {
	DefaultMultiplier float64
	Now               func() time.Time
}
type Recorder struct {
	requestRecords    func(telemetry.RequestRecord)
	emit              func(BillingEvent)
	cacheInjection    CacheInjectionPolicy
	billingService    *billing.Calculator
	resolver          *billing.PriceResolver
	stats             ProviderStats
	store             Store
	subscriptions     SubscriptionReader
	rates             RateReader
	providers         ProviderReader
	health            HealthObserver
	models            ModelCandidates
	logs              LogWriter
	effects           Effects
	observe           func(string, string)
	defaultMultiplier float64
	now               func() time.Time
}

func (g *GroupSnapshot) PeakMultiplierAt(at time.Time) float64 {
	if g.Location != nil {
		at = at.In(g.Location)
	}
	return (&pricing.BillingSettings{PeakRateEnabled: g.PeakRateEnabled, PeakStart: g.PeakStart, PeakEnd: g.PeakEnd, PeakRateMultiplier: g.PeakRateMultiplier}).PeakMultiplierAt(at)
}

// NewRecorder binds the dependencies supplied by app.
func NewRecorder(d Dependencies, o RecorderOptions) *Recorder {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Recorder{
		requestRecords:    d.RequestRecords,
		emit:              d.Emit,
		cacheInjection:    d.CacheInjection,
		billingService:    d.Calculator,
		resolver:          d.Prices,
		stats:             d.ProviderStats,
		store:             d.Funds,
		subscriptions:     d.Subscriptions,
		rates:             d.Rates,
		providers:         d.Providers,
		health:            d.Health,
		models:            d.Models,
		logs:              d.Logs,
		effects:           d.Effects,
		observe:           d.Observe,
		defaultMultiplier: o.DefaultMultiplier,
		now:               o.Now,
	}
}

// Record snapshots the input and selects OpenAI total-input billing unless usage is already in native buckets.
func (s *Recorder) Record(ctx context.Context, input *Input, openAI bool) error {
	input = Snapshot(input)
	if openAI && (input == nil || input.Result == nil || !input.Result.NativeUsage) {
		return s.RecordOpenAI(ctx, input)
	}
	return s.RecordAnthropic(ctx, input, &PricingOptions{})
}

func (s *Recorder) printf(component, format string, args ...any) {
	if s.observe != nil {
		s.observe(component, fmt.Sprintf(format, args...))
	}
}
