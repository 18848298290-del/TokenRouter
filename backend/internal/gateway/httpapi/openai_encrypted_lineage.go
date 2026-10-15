package httpapi

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_encrypted_lineage.go records rejected encrypted-content digests and
// removes known rejected items from subsequent request payloads.
// Rejected encrypted reasoning can be replayed by later HTTP or WebSocket
// turns. Tracking stores ciphertext digests under the group's session key, so
// subsequent requests can remove known rejected items while accepting different
// ciphertext. The HTTP bridge reuses the ingress session hash when one was
// bound upstream.
//
// Collection precedes removal because stripped input no longer contains the
// bytes needed to recognize the rejected items. The shared session store owns
// digest expiry and capacity; this adapter delegates storage and raw payload
// editing. Stripping errors return the supplied payload and zero removals,
// leaving error recovery to the request execution that requested the strip.

const (
	// OpenAIInvalidEncryptedContentReason identifies rejected encrypted
	// reasoning or compaction items. Clients may resend these items in
	// later turns, repeating the rejection and retry. The shared session
	// store keeps their digests with TTL and capacity limits. Later
	// requests strip matching digests; newly encrypted content has a
	// different digest.

	OpenAIInvalidEncryptedContentReason = "invalid_encrypted_content"

	// OpenAIWSIngressLineageContextKey carries the ingress session hash in
	// Gin context so HTTP bridge turns use the same key for invalid-content
	// tracking.
	OpenAIWSIngressLineageContextKey = "openai_ws_ingress_session_hash"
)

// OpenAIEncryptedLineage tracks rejected ciphertext in the session store shared
// by HTTP and WS.
type OpenAIEncryptedLineage struct {
	Store session.OpenAIWSStateStore // shared HTTP/WS lineage state
	// TTL supplies the expiry used for newly recorded rejection digests.
	// Callers configure it whenever Store is present and Mark can record
	// input.
	TTL func() time.Duration
}

// Mark records rejected ciphertext digests under the group's session key.
// Callers collect digests before removing their items and supply TTL when Store
// is configured. Missing store, blank session hash, or empty digests skips
// recording.
func (s *OpenAIEncryptedLineage) Mark(groupID int64, sessionHash string, digests []string) {
	if s == nil || len(digests) == 0 || strings.TrimSpace(sessionHash) == "" {
		return
	}
	stateStore := s.Store
	if stateStore == nil {
		return
	}
	stateStore.MarkSessionInvalidEncryptedContent(groupID, sessionHash, digests, s.TTL())
}

// Digests returns the session store's live set of rejected ciphertext digests.
// A missing adapter, store, blank session hash, or globally empty rejection
// tracker returns nil and avoids a per-session lookup.
func (s *OpenAIEncryptedLineage) Digests(groupID int64, sessionHash string) map[string]struct{} {
	if s == nil || strings.TrimSpace(sessionHash) == "" {
		return nil
	}
	stateStore := s.Store
	if stateStore == nil || !stateStore.HasAnySessionInvalidEncryptedContent() {
		return nil
	}
	return stateStore.GetSessionInvalidEncryptedContentDigests(groupID, sessionHash)
}

// SessionHash chooses the lineage key shared with an ingress WebSocket turn. It
// trims and prefers the bound ingress hash; otherwise GenerateOpenAISessionHash
// uses the HTTP session signals and the supplied request body.
func (s *OpenAIEncryptedLineage) SessionHash(c *gin.Context, body []byte) string {
	if c != nil {
		if fromCtx := strings.TrimSpace(c.GetString(OpenAIWSIngressLineageContextKey)); fromCtx != "" {
			return fromCtx
		}
	}
	return GenerateOpenAISessionHash(c, body)
}

// MarkPayload collects rejected ciphertext digests, records them, and logs
// their count. The caller confirms the upstream rejection before invoking this
// helper. Payloads with no encrypted-content digests produce neither a stored
// entry nor this log.
func (s *OpenAIEncryptedLineage) MarkPayload(
	c *gin.Context,
	payload []byte,
	logKey string,
	providerID int64,
	turn int,
) {
	digests := openai.CollectOpenAIEncryptedContentDigestsRaw(payload)
	if len(digests) == 0 {
		return
	}
	s.Mark(
		OpenAIResponseGroupID(c),
		s.SessionHash(c, payload),
		digests,
	)
	gatewayprovider.LogOpenAIWSModeInfo("%s provider_id=%d turn=%d digests=%d", logKey, providerID, turn, len(digests))
}

// Strip removes payload items whose ciphertext digests occur in invalid. It
// returns the resulting bytes and removal count, logging removals under logKey.
// A stripping error is logged under logKey plus _skip and returns the original
// payload with zero removals for the execution caller's recovery handling.
func (s *OpenAIEncryptedLineage) Strip(
	payload []byte,
	invalid map[string]struct{},
	logKey string,
	providerID int64,
	turn int,
) ([]byte, int) {
	strippedPayload, strippedCount, stripErr := openai.StripOpenAIInvalidEncryptedContentRaw(payload, invalid)
	if stripErr != nil {
		gatewayprovider.LogOpenAIWSModeInfo(
			"%s_skip provider_id=%d turn=%d reason=strip_error cause=%s",
			logKey,
			providerID,
			turn, gatewayprovider.TruncateOpenAIWSLogValue(stripErr.Error(), gatewayprovider.OpenAIWSLogValueMaxLen),
		)
		// A failed raw edit leaves the supplied replay intact;
		// returning zero removals keeps callers from treating an
		// unmodified payload as repaired input.
		return payload, 0
	}
	if strippedCount > 0 {
		gatewayprovider.LogOpenAIWSModeInfo(
			"%s provider_id=%d turn=%d stripped_items=%d",
			logKey,
			providerID,
			turn,
			strippedCount,
		)
	}
	return strippedPayload, strippedCount
}
