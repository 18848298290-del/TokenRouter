package httpapi

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const (
	// OpenAIInvalidEncryptedContentReason identifies rejected encrypted reasoning or compaction items.
	// Clients may resend these items in later turns, repeating the rejection and retry.
	// The shared session store keeps their digests with TTL and capacity limits.
	// Later requests strip matching digests; newly encrypted content has a different digest.

	OpenAIInvalidEncryptedContentReason = "invalid_encrypted_content"

	// OpenAIWSIngressLineageContextKey carries the ingress session hash in Gin context
	// so HTTP bridge turns use the same key for invalid-content tracking.
	OpenAIWSIngressLineageContextKey = "openai_ws_ingress_session_hash"
)

// OpenAIEncryptedLineage tracks rejected ciphertext in the session store shared by HTTP and WS.
type OpenAIEncryptedLineage struct {
	Store session.OpenAIWSStateStore
	TTL   func() time.Duration
}

// Mark stores rejected ciphertext digests for the session.
// Callers collect digests before stripping the items.
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

// Digests returns known invalid ciphertext digests for the session.
// It skips the session lookup when the store has no invalid-content records.
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

// SessionHash prefers the ingress session hash used by the reader.
// Otherwise it derives a hash from the request body.
func (s *OpenAIEncryptedLineage) SessionHash(c *gin.Context, body []byte) string {
	if c != nil {
		if fromCtx := strings.TrimSpace(c.GetString(OpenAIWSIngressLineageContextKey)); fromCtx != "" {
			return fromCtx
		}
	}
	return GenerateOpenAISessionHash(c, body)
}

// MarkPayload records and logs ciphertext digests from a payload rejected with
// invalid_encrypted_content. The caller determines whether the payload was rejected.
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

// Strip removes items whose ciphertext digests are in invalid and returns the payload and removal count.
// It logs removals under logKey and errors under logKey+"_skip".
// No match or a stripping error leaves the payload unchanged.
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
