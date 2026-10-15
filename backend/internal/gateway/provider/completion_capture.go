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

// completion_capture.go captures completion inputs and associates billing
// identities with diagnostic request records.
// Completion can run after the HTTP request or WS turn has ended, so capture
// resolves usage, payer, provider, and pricing evidence synchronously before a
// worker receives it. Dedicated snapshot helpers copy the mutable fields that
// worker normalization uses. Callers serialize source writers with this
// capture phase.
//
// Financial deduplication and diagnostic correlation can select different IDs.
// Ordinary telemetry seeds feed the shared billing-key resolver, while
// SettlementKey applies WS or video operation overrides. Related video
// completions derive stable diagnostic records from Key and financial
// identity; ordinary completions update the active request and its aliases.
// Requested reasoning is extracted from original bytes before capture returns,
// and a WS turn can retain its start time for price selection.

// MessagesCapture supplies request-owned inputs for synchronous Messages
// completion snapshot capture.
type MessagesCapture struct {
	// Result, APIKey, User, and Provider are read during synchronous capture.
	// Callers serialize source mutation until capture has copied worker-owned data;
	// returning from capture permits submitting that snapshot to asynchronous completion.
	//
	// forwarding result usage evidence
	Result *forwardcore.MessagesResult
	// admitted request API Key
	APIKey *apikey.APIKey
	// completion payer user input
	User *identity.User
	// executed provider configuration input
	Provider *provider.Record
	// optional subscription pricing evidence
	Subscription *billing.UserSubscription
	// client request endpoint path
	InboundEndpoint string
	// normalized upstream endpoint path
	UpstreamEndpoint string
	// client request User-Agent text
	UserAgent string
	// observed client IP address
	IPAddress string
	// explicit client session correlation ID
	ClientSessionID string
	// semantic request payload digest
	RequestPayloadHash string
	// RequestBody is read synchronously for requested reasoning effort. Keep these
	// bytes stable during capture; they are not retained by the completion snapshot.
	RequestBody       []byte // original requested reasoning body
	ForceCacheBilling bool   // requested cache-read billing override
	// APIKeyService is checked for interface presence and is never invoked during
	// capture; an interface containing a typed nil still records QuotaUpdates=true.
	APIKeyService QuotaUpdater // supplied Key quota-update capability

	// Group mappings resolved by the handler before forwarding.
	routing.PricingUsageFields
}

// QuotaUpdater identifies a supplied API Key quota-update capability during
// completion capture.
type QuotaUpdater interface {
	// UpdateQuotaUsed increments the selected API Key quota usage by cost.
	UpdateQuotaUsed(ctx context.Context, apiKeyID int64, cost float64) error
	// UpdateRateLimitUsage increments the selected API Key window usage by cost.
	UpdateRateLimitUsage(ctx context.Context, apiKeyID int64, cost float64) error
}

// OpenAICapture supplies request or WS-turn observations for synchronous
// completion capture.
type OpenAICapture struct {
	// Result, APIKey, User, and Provider are read during synchronous capture.
	// Callers serialize source mutation until capture has copied worker-owned data;
	// returning from capture permits submitting that snapshot to asynchronous completion.
	//
	// forwarding result usage evidence
	Result *forwardcore.OpenAIResult
	APIKey *apikey.APIKey // admitted request API Key
	// completion payer user input
	User *identity.User
	// executed provider configuration input
	Provider *provider.Record
	// optional subscription pricing evidence
	Subscription *billing.UserSubscription
	// client request endpoint path
	InboundEndpoint string
	// normalized upstream endpoint path
	UpstreamEndpoint string
	// client request User-Agent text
	UserAgent string
	// observed client IP address
	IPAddress string
	// explicit client session correlation ID
	ClientSessionID string
	// semantic request payload digest
	RequestPayloadHash string
	// RequestBody is read synchronously for requested reasoning effort. Keep these
	// bytes stable during capture; they are not retained by the completion snapshot.
	RequestBody []byte // original requested reasoning body
	// PricingAt supplies the WS turn start for time-sensitive prices; zero makes
	// completion use its recording time, as ordinary HTTP capture callers intend.
	PricingAt time.Time
	// APIKeyService is checked for interface presence and is never invoked during
	// capture; an interface containing a typed nil still records QuotaUpdates=true.
	APIKeyService QuotaUpdater // supplied Key quota-update capability
	CyberBlocked  bool         // request cyber failure marker
	// NativeCompactionV2 reports whether runtime inspection identified native remote compaction v2.
	NativeCompactionV2 bool // native compaction v2 observation
	routing.PricingUsageFields
}

// CyberCapture supplies cyber-failure usage and request identity for
// completion recording.
type CyberCapture struct {
	APIKey *apikey.APIKey // admitted request API Key
	// executed provider configuration input
	Provider *provider.Record
	// optional subscription pricing evidence
	Subscription *billing.UserSubscription
	// observed upstream operation identity
	RequestID string
	// trimmed cyber billing model
	Model string
	// request streaming mode flag
	Stream bool
	// observed aggregate input tokens
	InputTokens int
	// observed aggregate output tokens
	OutputTokens int
	// client request endpoint path
	InboundEndpoint string
	// normalized upstream endpoint path
	UpstreamEndpoint string
	// client request User-Agent text
	UserAgent string
	// observed client IP address
	IPAddress string
	// explicit client session correlation ID
	ClientSessionID string
	// semantic request payload digest
	RequestPayloadHash string
	// APIKeyService is checked for interface presence and is never invoked during
	// capture; an interface containing a typed nil still records QuotaUpdates=true.
	APIKeyService QuotaUpdater // supplied Key quota-update capability

	// NativeCompactionV2 carries the native compaction flag through failure handling.
	NativeCompactionV2 bool // native compaction v2 observation
	routing.PricingUsageFields
}

// CompletionBillingKey delegates billing deduplication selection using context
// request seeds and the upstream operation ID. Recognized operation prefixes
// win; the shared resolver supplies compatibility/local namespaces or a
// generated key when evidence is absent.
func CompletionBillingKey(ctx context.Context, upstreamRequestID string) string {
	return completion.ResolveBillingKey(RequestIdentity(ctx, upstreamRequestID, ""), scheduler.GenerateRequestID)
}

// StableAudioBillingRequestID creates the stable Grok audio operation key from
// a trimmed upstream ID. Existing audio prefixes survive, and blank input
// generates a new ID that callers retain for subsequent completion.
func StableAudioBillingRequestID(upstreamRequestID string) string {
	return completion.StableAudioRequestID(upstreamRequestID, scheduler.GenerateRequestID)
}

// StableRealtimeBillingRequestID creates a stable Grok Realtime operation key
// from a trimmed session ID. Existing prefixes survive, while blank input
// generates an ID that callers retain across that session's completion work.
func StableRealtimeBillingRequestID(sessionID string) string {
	return completion.StableRealtimeRequestID(sessionID, scheduler.GenerateRequestID)
}

// CompletionPayloadFingerprint selects supplied semantic payload evidence or a
// prefixed context request seed. The shared resolver can return empty when
// only an upstream operation ID is available, keeping identity distinct from
// payload evidence.
func CompletionPayloadFingerprint(ctx context.Context, requestPayloadHash string) string {
	return completion.PayloadFingerprint(RequestIdentity(ctx, "", requestPayloadHash))
}

// CaptureMessages extracts Messages result, payer, Key, provider, and pricing
// fields before returning a worker snapshot. Nil input returns nil; callers
// serialize capture with source mutations, and supplied quota capability is
// recorded by interface presence without invocation.
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

// CaptureOpenAI extracts result, payer, pricing time, request reasoning, and
// provider fields before returning a completion snapshot. Nil input returns
// nil; native usage can enable context force-cache billing, and WS PricingAt
// remains the supplied turn time.
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

// RequestIdentity captures context request seeds plus the supplied upstream ID
// and payload digest. Nil context leaves context seeds empty;
// telemetry.ClientRequestID and RequestID currently use the same compatibility
// context key, while upstream/payload stay separate.
func RequestIdentity(ctx context.Context, upstream, payload string) completion.RequestIdentity {
	out := completion.RequestIdentity{Upstream: upstream, PayloadHash: payload}
	if ctx != nil {
		out.Client, _ = ctx.Value(telemetry.ClientRequestID).(string)
		out.Local, _ = ctx.Value(telemetry.RequestID).(string)
	}
	return out
}

// CaptureCyber builds a cyber-blocked completion input when Key, Key.User,
// provider, and trimmed model are present. Missing requirements return nil;
// the generated forwarding result carries observed tokens and uses the Key
// user as the payer input.
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
		// APIKeyService is checked for interface presence and is never invoked during
		// capture; an interface containing a typed nil still records QuotaUpdates=true.
		APIKeyService: in.APIKeyService,

		PricingUsageFields: in.PricingUsageFields,
		CyberBlocked:       true,
		NativeCompactionV2: in.NativeCompactionV2,
	})
}

// captureRequestIdentity assigns financial and diagnostic identities before
// completion outlives the request. It first resolves BillingKey and applies
// SettlementKey overrides, then retains the telemetry request ID unless a
// missing ID or video completion needs a stable related record.
//
// Main-request capture updates model/provider observations and aliases,
// limiting appended attempts to nonchild requests below the cap. A distinct
// derived ID receives a completed related record with actor, Key, team,
// provider, and timing evidence. out.Result must be present. Existing
// client/local context seeds can coincide, while task or WS settlement
// identity remains separate from the diagnostic row.
func captureRequestIdentity(ctx context.Context, out *completion.Input, upstreamID string) {
	out.BillingKey = CompletionBillingKey(ctx, upstreamID)
	out.BillingKey = out.SettlementKey()
	out.RequestID = telemetry.RequestIDValue(ctx)
	// Video polling and missing diagnostic IDs derive a stable related ID from
	// Key identity and settlement key, so repeated financial operations correlate to one record.
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
				// Child completion already belongs to its own request relationship. Append
				// main-request attempts only below the cap, retaining aliases even when history is full.
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
	// A derived diagnostic ID needs its own completed record rather than rewriting
	// the active request record; carry settlement aliases and captured actor/provider identity.
	if out.RequestID != telemetry.RequestIDValue(ctx) {
		record := telemetry.RequestRecord{RequestID: out.RequestID, State: "completed", Model: out.Result.Model, StartedAt: out.PricingAt}
		// Without a turn pricing timestamp, infer start from the observed duration
		// at capture time so the related completion record still has a useful request window.
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
