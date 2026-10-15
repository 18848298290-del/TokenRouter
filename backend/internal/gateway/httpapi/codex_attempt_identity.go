package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// codex_attempt_identity.go stages credential-source identity and
// provider-matched fingerprints for Codex attempts. Codex request identity
// follows the provider whose credentials an attempt uses, including the parent
// of a shadow provider. Successful later preparation replaces staged identity
// so session isolation and body metadata are derived from its credential
// source. Resolution errors return before publishing new identity.
//
// Fingerprint IDs are a separate staged object used by both headers and body
// rewriting. Nil staging clears earlier IDs, and reads accept an object only
// when its provider matches the current record or that record's credential parent.
// This check prevents one attempt's IDs from being applied to a different
// provider after failover, while shadow requests can share their parent's identity.

// Codex context keys keep credential-source records and fingerprint IDs separate
// so each attempt can replace one staging value through its own preparation step.
const (
	codexIdentityStateKey    = "openai_codex_provider_identity_source" // credential-source record key
	codexFingerprintStateKey = "codex_fingerprint_ids"                 // staged fingerprint object key
)

type (
	// codexIdentityState retains the credential-source record selected for one attempt.
	codexIdentityState struct {
		// source references the selected ExecutionProvider record. Preparation stores
		// that view directly so identity consumers read the same credential-source object.
		source *provider.Record
	}
	// codexFingerprintState holds the fingerprint object shared by attempt header and body rewriting.
	codexFingerprintState struct {
		// ids is the staged pointer, including nil to clear a prior attempt.
		// Body metadata rewriting can capture its original session value on this object.
		ids *openai.FingerprintIDs
	}
)

// PrepareCodexIdentity resolves shadow credentials for each attempt and replaces the previous identity.
// A nil provider yields a nil record through ExecutionProvider.View. Shadow
// resolution uses reader and returns errors before context staging changes;
// success stores the selected record pointer when a Gin context is supplied.
// Callers stop preparation on error because an earlier staged source can still
// remain in that context.
func PrepareCodexIdentity(ctx context.Context, c *gin.Context, reader gatewayadapter.ExecutionProviderReader, value *gatewayadapter.ExecutionProvider) (*provider.Record, error) {
	source := value
	if value != nil && value.View().IsShadow() {
		// A shadow executes with its parent's credentials. Resolve that source
		// before publishing identity so headers and session isolation name the credential owner.
		resolved, err := gatewayadapter.CredentialProvider(ctx, reader, value)
		if err != nil {
			return nil, err
		}
		source = resolved
	}
	record := source.View()
	if c != nil {
		c.Set(codexIdentityStateKey, codexIdentityState{source: record})
	}
	return record, nil
}

// CodexIdentityRecord returns the staged credential source or the supplied fallback.
// Nil contexts, absent or mistyped state, and nil staged records use fallback.
// The selected record is shared with attempt preparation and is returned directly.
func CodexIdentityRecord(c *gin.Context, fallback *provider.Record) *provider.Record {
	if c != nil {
		if value, ok := c.Get(codexIdentityStateKey); ok {
			if state, ok := value.(codexIdentityState); ok && state.source != nil {
				return state.source
			}
		}
	}
	return fallback
}

// StageCodexFingerprintIDs replaces staged IDs, including with nil, so failover uses the current provider.
// A nil context is ignored. The pointer is stored directly for header and
// body consumers to share one attempt's identity and original-body-session capture state.
// Callers serialize body rewrites sharing this object because original-session
// capture mutates its fields without an internal lock.
func StageCodexFingerprintIDs(c *gin.Context, ids *openai.FingerprintIDs) {
	if c != nil {
		c.Set(codexFingerprintStateKey, codexFingerprintState{ids: ids})
	}
}

// StagedCodexFingerprintIDs returns staged IDs eligible for the supplied Codex provider.
// Missing or unusable state returns nil. Eligibility requires matching ProviderID
// or ParentProviderID, and the returned pointer is the same object installed by staging.
//
// Codex protocol eligibility is checked before fingerprint ownership. A shadow
// can execute with its parent's credentials, so IDs derived for that parent
// remain eligible for the shadow. A record matching neither provider nor parent
// ID receives nil, making upstream header and body helpers leave its identity alone.
func StagedCodexFingerprintIDs(c *gin.Context, value *provider.Record) *openai.FingerprintIDs {
	if c == nil || value == nil || !value.UsesOpenAICodexProtocol() {
		return nil
	}
	stored, ok := c.Get(codexFingerprintStateKey)
	if !ok {
		return nil
	}
	state, ok := stored.(codexFingerprintState)
	if !ok || state.ids == nil {
		return nil
	}
	ids := state.ids
	// Shadow providers can use fingerprint IDs derived from their credential parent.
	// Reject other provider IDs so a later attempt cannot inherit an unrelated identity.
	if ids.ProviderID != value.ID && (value.ParentProviderID == nil || *value.ParentProviderID != ids.ProviderID) {
		return nil
	}
	return ids
}

// ApplyStagedCodexFingerprintHeaders writes eligible staged identity into outbound headers.
// The upstream helper tolerates nil headers or IDs. Callers apply this after
// forwarding inbound headers and before enforcing the final Codex provider identity.
func ApplyStagedCodexFingerprintHeaders(c *gin.Context, value *provider.Record, h http.Header) {
	openai.ApplyCodexFingerprintHeaders(h, StagedCodexFingerprintIDs(c, value))
}

// ApplyStagedCodexFingerprintClientMetadata rewrites body identity with eligible staged IDs.
// It returns whether the upstream helper modified metadata or prompt_cache_key.
// Body and captured original-session state can change, so callers use the same
// staged fingerprint object for the corresponding outbound headers.
func ApplyStagedCodexFingerprintClientMetadata(c *gin.Context, value *provider.Record, body map[string]any) bool {
	return openai.ApplyCodexFingerprintClientMetadata(body, StagedCodexFingerprintIDs(c, value))
}
