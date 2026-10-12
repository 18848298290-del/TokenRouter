package moderationflow

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// An upstream cyber warning can produce usage recording, session blocking
// and an Ops entry after the request has ended. Dispatch copies completion
// and metadata evidence before task submission, then supplies a detached
// thirty-second context to recording and blocking while Ops enqueue
// receives its own entry.
//
// Those effects run in order and have independent failure handling;
// task-admission results are ignored and no local retry is scheduled.
// Before-scope blocking instead uses a detached short context and copied
// keys. Block plans retain explicit-key priority and suppress repeats of
// the first key, while other transcript duplicates remain. Upstream warning
// and local session rejection produce different Ops source/owner fields,
// preserving their distinct origin for later inspection.

// SessionBlockedClientMessage gives client instructions when local session
// security policy rejects a request before upstream execution can proceed.
const SessionBlockedClientMessage = "This session is blocked by the security policy. Start a new session."

// Mark carries the upstream cyber-warning classification, response text and
// usage evidence.
type Mark struct {
	// Code, Message and Body describe one warning. BuildPolicyOpsEntry uses
	// trimmed Message and retained Body, while Code remains classification
	// evidence for the caller's other handling.
	Code, Message, Body string // warning classification code, selected warning message and retained response text
	// UpstreamStatus supplies the Ops response status. UpstreamInTok and
	// UpstreamOutTok retain warning token evidence; Dispatch obtains recording
	// input from PolicyCompletion.Usage rather than these fields.
	UpstreamStatus, UpstreamInTok, UpstreamOutTok int // upstream HTTP status, observed input tokens and observed output tokens
}

// Provider identifies the selected upstream record for moderation adapters
// and observations.
type Provider struct {
	ID int64 // selected upstream provider ID
	// Name and Platform describe the same selected upstream record used by
	// moderation adapters. ID keeps its persistent identity separate from the
	// display name and platform spelling.
	Name, Platform string // selected provider display name and selected upstream platform label
}

// BlockWriter supplies session blocking used by detached moderation
// side-effect work.
type BlockWriter interface {
	// MarkCyberSessionBlocked submits keys and optional scope under the
	// supplied context. Dispatch uses an empty scope and one warning key;
	// MarkBeforeScope copies its plan keys, leaving actual storage/error
	// handling with the implementation.
	MarkCyberSessionBlocked(context.Context, string, []string)
}
type (
	// Tasks admits named background work for request-detached moderation
	// effects.
	Tasks interface {
		// Go submits named work and reports admission through its boolean result.
		// Runtime.Dispatch ignores that result, so the implementation owns
		// rejection behavior and Dispatch supplies no local retry or acceptance
		// feedback.
		Go(string, func()) bool
	}
	// OpsWriter receives prepared moderation error entries for its own enqueue
	// lifecycle.
	OpsWriter interface {
		// Enqueue submits the supplied error entry and returns no admission or
		// persistence result. Callers keep retained entry fields stable while the
		// sink uses them; Runtime.Dispatch receives no completion confirmation.
		Enqueue(*ops.OpsInsertErrorLogInput)
	}
)

// Runtime binds moderation recording, session blocking, task admission and
// Ops side effects.
type Runtime struct {
	// Recorder is used only when ForwardErrored is true and the pointer is
	// nonnil. RecordCyber validates its captured input and handles its own
	// recording errors.
	Recorder *completion.Recorder // optional cyber usage recorder
	// Blocks receives detached-context blocking when a warning key or
	// before-scope plan is present. Nil leaves those block effects absent
	// while other configured effects can proceed.
	Blocks BlockWriter // optional session block writer
	// Tasks is required by Dispatch, which invokes Go after capturing input.
	// Its returned admission boolean is ignored, leaving rejected work without
	// a local retry.
	Tasks Tasks // required background task executor
	// Ops receives the prepared upstream-warning entry after recording and
	// blocking calls. Enqueue has no context or returned persistence result,
	// so the worker timeout does not itself bound that call.
	Ops OpsWriter // optional moderation Ops sink
}

// PolicyCompletion carries one warning's captured usage, metadata and
// requested side effects.
type PolicyCompletion struct {
	// Usage is snapshotted before task submission and passed to RecordCyber on
	// eligible failures. Nil makes RecordCyber omit recording, while
	// configured block and Ops effects can still run.
	Usage *completion.Input // optional completion recording input
	Meta  OpsMeta           // captured moderation Ops metadata
	Mark  Mark              // captured upstream cyber warning
	// ForwardErrored selects recording when Recorder is configured. Session
	// blocking and Ops enqueue remain separate decisions and can run even when
	// this flag is false.
	ForwardErrored bool // forward attempt returned an error
	// BlockKey is used when nonempty and Blocks is configured, under an empty
	// scope string. Dispatch keeps its supplied spelling and performs no local
	// trimming or normalization.
	BlockKey string // optional warning session block key
}

// BlockPlan holds the ordered session keys and optional scope for blocking.
type BlockPlan struct {
	// ScopeKey is assigned by BuildBlockPlan when transcript length is
	// positive, even if its entries were suppressed as repeats of the first
	// key. Before-scope blocking passes it through.
	ScopeKey string // selected transcript-associated block scope
	// Keys is newly built by BuildBlockPlan, retaining allowed duplicates and
	// empty transcript strings. MarkBeforeScope clones its slice before
	// handing the plan to Blocks.
	Keys []string // owned ordered session key list
}

// OpsMeta carries request and identity evidence shared by moderation Ops entry
// builders.
type OpsMeta struct {
	// RequestID and ClientRequestID are separate fields copied into Ops rows.
	// The current HTTP CyberHandler.Meta supplies both from
	// telemetry.RequestIDValue, so these builders preserve the values they
	// receive.
	RequestID       string // captured platform request identifier
	ClientRequestID string // captured client-request correlation value
	Platform        string // captured upstream platform label
	Model           string // captured request model label
	RequestPath     string // captured HTTP request path
	Stream          bool   // captured request streaming indicator
	InboundEndpoint string // captured client protocol endpoint
	// UpstreamEndpoint is copied into upstream policy entries, while local
	// session-block entries omit it because upstream execution has not begun
	// on that path.
	UpstreamEndpoint string // captured selected upstream endpoint
	UserAgent        string // captured client User-Agent text
	APIKeyPrefix     string // caller-supplied API key identification prefix
	// UserID and APIKeyID become Ops pointers only when positive. ProviderID
	// follows that rule for policy entries, while the local session-block
	// builder omits provider ownership.
	UserID     int64 // captured Ops user identifier
	APIKeyID   int64 // captured API key identifier
	ProviderID int64 // captured upstream provider identifier
	// GroupID retains a supplied pointer until Dispatch or an entry builder
	// copies it. Callers keep it stable during synchronous capture; resulting
	// copies isolate later pointed-value changes.
	GroupID   *int64    // optional shared group identifier
	ClientIP  string    // optional captured client IP
	CreatedAt time.Time // caller-supplied event creation timestamp
	// SessionBlockKey supplies ErrorBody text for local session-block entries.
	// BuildPolicyOpsEntry instead uses Mark.Body, keeping local
	// blocked-session evidence separate from an upstream warning response.
	SessionBlockKey string // captured local session block key
}

// cloneID returns a separately allocated copy of a pointed identifier, or
// nil for nil input. The pointed value is kept exactly, including zero or
// negative values; positivity filtering belongs to each consumer.
func cloneID(in *int64) *int64 {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

// Dispatch captures warning input before submitting one request-detached
// task. Callers keep mutable input stable during capture;
// completion.Snapshot and cloneID copy selected evidence, while
// SnapshotContext copies model trace and retains the synchronized request
// recorder. Nil ctx yields background capture.
//
// The task creates a thirty-second context for eligible RecordCyber and
// optional session blocking, then enqueues Ops separately. These calls have
// no shared transaction or local retry, and Ops receives no timeout
// context. Tasks is required and its admission boolean is ignored, so
// returning from Dispatch establishes submission invocation rather than
// completed side effects.
func (r Runtime) Dispatch(ctx context.Context, in PolicyCompletion) {
	// Capture completion, pointed group and selected context evidence before
	// the task can run or the request can mutate, isolating later-turn
	// metadata changes from this warning.
	in.Usage = completion.Snapshot(in.Usage)
	in.Meta.GroupID = cloneID(in.Meta.GroupID)
	source := completion.SnapshotContext(ctx)
	r.Tasks.Go("handler/openai_gateway_handler.go:recordCyberPolicyIfMarked", func() {
		// Use a detached thirty-second context for recorder and block calls, then
		// cancel it when this task returns; enqueue receives no context through
		// OpsWriter.
		work, cancel := context.WithTimeout(source, 30*time.Second)
		defer cancel()
		if in.ForwardErrored && r.Recorder != nil {
			r.Recorder.RecordCyber(work, in.Usage)
		}
		if r.Blocks != nil && in.BlockKey != "" {
			r.Blocks.MarkCyberSessionBlocked(work, "", []string{in.BlockKey})
		}
		if r.Ops != nil {
			r.Ops.Enqueue(BuildPolicyOpsEntry(in.Meta, &in.Mark))
		}
	})
}

// SnapshotContent copies Images, Items and ImageItems slice storage for an
// independent moderation snapshot. Current item/image element fields
// contain scalar values and strings, so copied collections isolate element
// mutation; nil slices remain nil.
func SnapshotContent(in moderation.ContentModerationInput) moderation.ContentModerationInput {
	in.Images = slices.Clone(in.Images)
	in.Items = slices.Clone(in.Items)
	in.ImageItems = slices.Clone(in.ImageItems)
	return in
}

// BuildBlockPlan builds ordered blocking keys with a nonempty explicit key
// first. Transcript entries equal to the current first key are suppressed,
// while other repeated or empty strings are retained in their supplied
// order.
//
// With no explicit key, the first transcript entry becomes that comparison
// key; for example a, b, b remains a, b, b. Scope is assigned whenever
// transcript length is positive, even when every transcript key repeats the
// explicit key and is skipped. Empty transcript leaves scope empty
// regardless of the supplied scope string.
func BuildBlockPlan(explicit string, transcript []string, scope string) BlockPlan {
	p := BlockPlan{}
	if explicit != "" {
		p.Keys = append(p.Keys, explicit)
	}
	// Compare transcript entries with the retained first key to preserve
	// explicit-key priority while leaving other duplicate entries in their
	// supplied order.
	for _, key := range transcript {
		if len(p.Keys) == 0 || key != p.Keys[0] {
			p.Keys = append(p.Keys, key)
		}
	}
	if len(transcript) > 0 {
		p.ScopeKey = scope
	}
	return p
}

// MarkBeforeScope passes nonempty plan keys to configured blocking under a
// detached five-hundred-millisecond context. It clones the key slice before
// the callback, then cancels the context on return; storage errors and
// deadline enforcement belong to Blocks.
func (r Runtime) MarkBeforeScope(plan BlockPlan) {
	if len(plan.Keys) == 0 || r.Blocks == nil {
		return
	}
	// Use an independent short context before request-scope rejection, copying
	// keys before the block callback and canceling this registration after it
	// returns.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	r.Blocks.MarkCyberSessionBlocked(ctx, plan.ScopeKey, slices.Clone(plan.Keys))
}

// BuildPolicyOpsEntry returns an upstream-warning Ops row or nil for a nil
// mark. It uses upstream_http/provider origin, request-phase cyber
// classification, P3 severity and the mark's upstream status, with business
// limitation set.
//
// Positive user, key and provider IDs become optional pointers, GroupID is
// copied and nonempty ClientIP is retained. Request/model/endpoint metadata
// is copied as supplied; Message is trimmed and prefixed while Body stays
// supplied. The returned row is newly allocated, and enqueueing it later
// supplies no persistence result here.
func BuildPolicyOpsEntry(meta OpsMeta, mark *Mark) *ops.OpsInsertErrorLogInput {
	if mark == nil {
		return nil
	}
	// Encode cyber-blocked request type in the Ops int16 representation.
	rt := int16(usage.RequestTypeCyberBlocked)
	entry := &ops.OpsInsertErrorLogInput{
		RequestID:         meta.RequestID,
		ClientRequestID:   meta.ClientRequestID,
		Platform:          meta.Platform,
		Model:             meta.Model,
		RequestPath:       meta.RequestPath,
		Stream:            meta.Stream,
		InboundEndpoint:   meta.InboundEndpoint,
		UpstreamEndpoint:  meta.UpstreamEndpoint,
		RequestedModel:    meta.Model,
		RequestType:       &rt,
		UserAgent:         meta.UserAgent,
		APIKeyPrefix:      meta.APIKeyPrefix,
		ErrorPhase:        "request",
		ErrorType:         "cyber_policy",
		Severity:          "P3",
		StatusCode:        mark.UpstreamStatus,
		IsBusinessLimited: true,
		ErrorMessage:      "cyber_policy: " + strings.TrimSpace(mark.Message),
		ErrorBody:         mark.Body,
		ErrorSource:       "upstream_http",
		ErrorOwner:        "provider",
		CreatedAt:         meta.CreatedAt,
	}
	if meta.UserID > 0 {
		entry.UserID = &meta.UserID
	}
	if meta.APIKeyID > 0 {
		entry.APIKeyID = &meta.APIKeyID
	}
	if meta.ProviderID > 0 {
		entry.ProviderID = &meta.ProviderID
	}
	if meta.GroupID != nil {
		entry.GroupID = cloneID(meta.GroupID)
	}
	if meta.ClientIP != "" {
		entry.ClientIP = &meta.ClientIP
	}
	return entry
}

// BuildSessionBlockedOpsEntry returns a locally rejected session Ops row
// with status 403. It uses gateway_local/platform origin, P3 severity and
// cyber-blocked request classification, with business limitation set and
// the session key in ErrorBody.
//
// Positive user and key IDs become optional pointers, GroupID is copied and
// nonempty ClientIP is retained. ProviderID and UpstreamEndpoint are
// omitted on this local path even when present in metadata. The returned
// row is independently allocated, with enqueue and persistence owned by its
// caller's Ops sink.
func BuildSessionBlockedOpsEntry(meta OpsMeta) *ops.OpsInsertErrorLogInput {
	// Encode cyber-blocked request type in the Ops int16 representation.
	rt := int16(usage.RequestTypeCyberBlocked)
	entry := &ops.OpsInsertErrorLogInput{
		RequestID:         meta.RequestID,
		ClientRequestID:   meta.ClientRequestID,
		Platform:          meta.Platform,
		Model:             meta.Model,
		RequestPath:       meta.RequestPath,
		Stream:            meta.Stream,
		InboundEndpoint:   meta.InboundEndpoint,
		RequestedModel:    meta.Model,
		RequestType:       &rt,
		UserAgent:         meta.UserAgent,
		APIKeyPrefix:      meta.APIKeyPrefix,
		ErrorPhase:        "request",
		ErrorType:         "cyber_policy_session_blocked",
		Severity:          "P3",
		StatusCode:        403,
		IsBusinessLimited: true,
		ErrorMessage:      "cyber_policy_session_blocked: request rejected locally by session block",
		ErrorBody:         "session_block_key=" + meta.SessionBlockKey,
		ErrorSource:       "gateway_local",
		ErrorOwner:        "platform",
		CreatedAt:         meta.CreatedAt,
	}
	if meta.UserID > 0 {
		entry.UserID = &meta.UserID
	}
	if meta.APIKeyID > 0 {
		entry.APIKeyID = &meta.APIKeyID
	}
	if meta.GroupID != nil {
		entry.GroupID = cloneID(meta.GroupID)
	}
	if meta.ClientIP != "" {
		entry.ClientIP = &meta.ClientIP
	}
	return entry
}
