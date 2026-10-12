package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// MessagesCapture supplies synchronous capture inputs. Submit the CaptureMessages snapshot to asynchronous workers.
type MessagesCapture struct {
	Result             *forwardcore.MessagesResult
	APIKey             *apikey.APIKey
	User               *identity.User
	Provider           *provider.Record
	Subscription       *billing.UserSubscription // Optional subscription.
	InboundEndpoint    string                    // Client request path.
	UpstreamEndpoint   string                    // Normalized upstream path.
	UserAgent          string                    // Request User-Agent.
	IPAddress          string                    // Client IP address.
	ClientSessionID    string                    // Explicit client session ID used to correlate usage rows.
	RequestPayloadHash string                    // Semantic payload hash that detects conflicting reuse of a request ID.
	RequestBody        []byte                    // Original body used to resolve the requested billing reasoning tier.
	ForceCacheBilling  bool                      // Bill input tokens as cache reads after a sticky-provider switch.
	APIKeyService      QuotaUpdater              // Optional API Key quota updater.

	routing.PricingUsageFields // Group mappings resolved by the handler before forwarding.
}

// QuotaUpdater indicates whether the caller supplied quota updates; capture does not invoke it.
type QuotaUpdater interface {
	UpdateQuotaUsed(ctx context.Context, apiKeyID int64, cost float64) error
	UpdateRateLimitUsage(ctx context.Context, apiKeyID int64, cost float64) error
}

// OpenAICapture supplies synchronous inputs and the pricing time for an HTTP request or WS turn.
type OpenAICapture struct {
	Result             *forwardcore.OpenAIResult
	APIKey             *apikey.APIKey
	User               *identity.User
	Provider           *provider.Record
	Subscription       *billing.UserSubscription
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string // Request User-Agent.
	IPAddress          string // Client IP address.
	ClientSessionID    string // Explicit client session ID used to correlate usage rows.
	RequestPayloadHash string
	RequestBody        []byte // Original body used to resolve the requested billing reasoning tier.
	// PricingAt is the WS turn start; HTTP callers leave it zero to use recording time.
	PricingAt     time.Time
	APIKeyService QuotaUpdater
	CyberBlocked  bool
	// NativeCompactionV2 reports whether runtime inspection identified native remote compaction v2.
	NativeCompactionV2 bool
	routing.PricingUsageFields
}

// CyberCapture supplies usage inputs for cyber_policy failures during forwarding.
type CyberCapture struct {
	APIKey             *apikey.APIKey
	Provider           *provider.Record
	Subscription       *billing.UserSubscription
	RequestID          string
	Model              string
	Stream             bool
	InputTokens        int
	OutputTokens       int
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string
	IPAddress          string
	ClientSessionID    string
	RequestPayloadHash string
	APIKeyService      QuotaUpdater

	// NativeCompactionV2 carries the native compaction flag through failure handling.
	NativeCompactionV2 bool
	routing.PricingUsageFields
}

func CompletionBillingKey(ctx context.Context, upstreamRequestID string) string {
	return completion.ResolveBillingKey(RequestIdentity(ctx, upstreamRequestID, ""), scheduler.GenerateRequestID)
}

// StableAudioBillingRequestID builds a durable TTS/STT usage key, preferring the upstream request ID.
func StableAudioBillingRequestID(upstreamRequestID string) string {
	return completion.StableAudioRequestID(upstreamRequestID, scheduler.GenerateRequestID)
}

// StableRealtimeBillingRequestID builds a durable usage key for one Realtime WebSocket session.
func StableRealtimeBillingRequestID(sessionID string) string {
	return completion.StableRealtimeRequestID(sessionID, scheduler.GenerateRequestID)
}

func CompletionPayloadFingerprint(ctx context.Context, requestPayloadHash string) string {
	return completion.PayloadFingerprint(RequestIdentity(ctx, "", requestPayloadHash))
}

// CaptureMessages snapshots usage and payer data before completion submission.
func CaptureMessages(ctx context.Context, in *MessagesCapture) *completion.Input {
	if in == nil {
		return nil
	}
	out := &completion.Input{
		Result:             ProjectMessagesCompletionResult(in.Result, in.Provider),
		APIKey:             ProjectCompletionKey(in.APIKey),
		User:               ProjectCompletionPayer(in.User),
		Provider:           ProjectCompletionProvider(in.Provider),
		Subscription:       in.Subscription,
		InboundEndpoint:    in.InboundEndpoint,
		UpstreamEndpoint:   in.UpstreamEndpoint,
		UserAgent:          in.UserAgent,
		IPAddress:          in.IPAddress,
		ClientSessionID:    in.ClientSessionID,
		RequestPayloadHash: CompletionPayloadFingerprint(ctx, in.RequestPayloadHash),

		ForceCacheBilling:  in.ForceCacheBilling,
		QuotaUpdates:       in.APIKeyService != nil,
		PricingUsageFields: in.PricingUsageFields,
	}
	if in.Result != nil {
		captureRequestIdentity(ctx, out, in.Result.RequestID)
		out.RequestedReasoningEffort = requeststate.CanonicalRequestedReasoningEffort(in.RequestBody)
	}
	return completion.Snapshot(out)
}

// CaptureOpenAI captures the WS turn time and independent billing-model chain.
func CaptureOpenAI(ctx context.Context, in *OpenAICapture) *completion.Input {
	if in == nil {
		return nil
	}
	out := &completion.Input{
		ForceCacheBilling:  in.Result != nil && in.Result.NativeUsage != nil && requeststate.IsForceCacheBilling(ctx),
		Result:             ProjectOpenAICompletionResult(in.Result, in.Provider),
		APIKey:             ProjectCompletionKey(in.APIKey),
		User:               ProjectCompletionPayer(in.User),
		Provider:           ProjectCompletionProvider(in.Provider),
		Subscription:       in.Subscription,
		InboundEndpoint:    in.InboundEndpoint,
		UpstreamEndpoint:   in.UpstreamEndpoint,
		UserAgent:          in.UserAgent,
		IPAddress:          in.IPAddress,
		ClientSessionID:    in.ClientSessionID,
		RequestPayloadHash: CompletionPayloadFingerprint(ctx, in.RequestPayloadHash),

		QuotaUpdates:             in.APIKeyService != nil,
		CyberBlocked:             in.CyberBlocked,
		NativeCompactionV2:       in.NativeCompactionV2,
		PricingAt:                in.PricingAt,
		PricingUsageFields:       in.PricingUsageFields,
		RequestedReasoningEffort: requeststate.CanonicalRequestedReasoningEffort(in.RequestBody),
	}
	if in.Result != nil {
		captureRequestIdentity(ctx, out, in.Result.RequestID)
	}
	return completion.Snapshot(out)
}

func RequestIdentity(ctx context.Context, upstream, payload string) completion.RequestIdentity {
	out := completion.RequestIdentity{Upstream: upstream, PayloadHash: payload}
	if ctx != nil {
		out.Client, _ = ctx.Value(telemetry.ClientRequestID).(string)
		out.Local, _ = ctx.Value(telemetry.RequestID).(string)
	}
	return out
}

// CaptureCyber copies policy-event and identity data for asynchronous recording.
func CaptureCyber(ctx context.Context, in CyberCapture) *completion.Input {
	if in.APIKey == nil || in.APIKey.User == nil || in.Provider == nil || strings.TrimSpace(in.Model) == "" {
		return nil
	}
	result := &forwardcore.OpenAIResult{
		RequestID: in.RequestID,
		Model:     strings.TrimSpace(in.Model),
		Usage: openai.ForwardUsage{
			InputTokens:  in.InputTokens,
			OutputTokens: in.OutputTokens,
		},
		Stream:   in.Stream,
		Duration: 0,
	}
	return CaptureOpenAI(ctx, &OpenAICapture{
		Result:             result,
		APIKey:             in.APIKey,
		User:               in.APIKey.User,
		Provider:           in.Provider,
		Subscription:       in.Subscription,
		InboundEndpoint:    in.InboundEndpoint,
		UpstreamEndpoint:   in.UpstreamEndpoint,
		UserAgent:          in.UserAgent,
		IPAddress:          in.IPAddress,
		ClientSessionID:    in.ClientSessionID,
		RequestPayloadHash: in.RequestPayloadHash,
		APIKeyService:      in.APIKeyService,

		PricingUsageFields: in.PricingUsageFields,
		CyberBlocked:       true,
		NativeCompactionV2: in.NativeCompactionV2,
	})
}

// captureRequestIdentity separates diagnostic identifiers from billing deduplication keys before the request ends.
func captureRequestIdentity(ctx context.Context, out *completion.Input, upstreamID string) {
	out.BillingKey = CompletionBillingKey(ctx, upstreamID)
	out.BillingKey = out.SettlementKey()
	out.RequestID = telemetry.RequestIDValue(ctx)
	if out.RequestID == "" || out.Result.VideoCount > 0 {
		var keyID int64
		if out.APIKey != nil {
			keyID = out.APIKey.ID
		}
		out.RequestID = telemetry.StableRequestID(fmt.Sprintf("%d/%s", keyID, out.BillingKey))
	}
	childRequest := telemetry.IsChildRequest(ctx)
	if out.RequestID == telemetry.RequestIDValue(ctx) {
		telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
			record.Model = out.Result.Model
			if out.Provider != nil {
				record.ProviderID = out.Provider.ID
				record.Platform = out.Provider.Platform
				attempt := telemetry.RequestAttempt{Number: len(record.Attempts) + 1, ProviderID: out.Provider.ID, Outcome: "completed", DurationMs: out.Result.Duration.Milliseconds()}
				if out.Result.UpstreamRequestID != nil {
					attempt.RequestID = *out.Result.UpstreamRequestID
				}
				if !childRequest && len(record.Attempts) < 256 {
					record.Attempts = append(record.Attempts, attempt)
				}
			}
		})
		telemetry.AddRequestAlias(ctx, "billing", out.BillingKey)
		if !completion.ForcedRequestID(upstreamID) && !strings.HasPrefix(upstreamID, "x_search:") {
			telemetry.AddRequestAlias(ctx, "upstream", upstreamID)
		}
		telemetry.AddRequestAlias(ctx, "upstream", out.Result.ResponseID)
		if out.Result.UpstreamRequestID != nil {
			telemetry.AddRequestAlias(ctx, "upstream", *out.Result.UpstreamRequestID)
		}
	}
	if out.RequestID != telemetry.RequestIDValue(ctx) {
		record := telemetry.RequestRecord{RequestID: out.RequestID, State: "completed", Model: out.Result.Model, StartedAt: out.PricingAt}
		if record.StartedAt.IsZero() {
			record.StartedAt = time.Now().Add(-out.Result.Duration)
		}
		record.DurationMs = out.Result.Duration.Milliseconds()
		record.Aliases = []telemetry.RequestAlias{{Kind: "billing", Value: out.BillingKey}}
		if upstreamID != "" {
			record.Aliases = append(record.Aliases, telemetry.RequestAlias{Kind: "upstream", Value: upstreamID})
		}
		if out.APIKey != nil {
			record.UserID = out.APIKey.ActorUserID
			record.APIKeyID = out.APIKey.ID
			if out.APIKey.TeamID != nil {
				record.TeamID = *out.APIKey.TeamID
			}
		}
		if out.Provider != nil {
			record.ProviderID = out.Provider.ID
			record.Platform = out.Provider.Platform
		}
		telemetry.RecordRelatedRequest(ctx, record)
	}
}
