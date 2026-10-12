package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const (
	codexIdentityStateKey    = "openai_codex_provider_identity_source"
	codexFingerprintStateKey = "codex_fingerprint_ids"
)

// Identity and fingerprint state use separate Gin context keys.
type (
	codexIdentityState    struct{ source *provider.Record }
	codexFingerprintState struct{ ids *openai.FingerprintIDs }
)

// PrepareCodexIdentity resolves shadow credentials for each attempt and replaces the previous identity.
func PrepareCodexIdentity(ctx context.Context, c *gin.Context, reader gatewayadapter.ExecutionProviderReader, value *gatewayadapter.ExecutionProvider) (*provider.Record, error) {
	source := value
	if value != nil && value.View().IsShadow() {
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
func StageCodexFingerprintIDs(c *gin.Context, ids *openai.FingerprintIDs) {
	if c != nil {
		c.Set(codexFingerprintStateKey, codexFingerprintState{ids: ids})
	}
}

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
	if ids.ProviderID != value.ID && (value.ParentProviderID == nil || *value.ParentProviderID != ids.ProviderID) {
		return nil
	}
	return ids
}

func ApplyStagedCodexFingerprintHeaders(c *gin.Context, value *provider.Record, h http.Header) {
	openai.ApplyCodexFingerprintHeaders(h, StagedCodexFingerprintIDs(c, value))
}

func ApplyStagedCodexFingerprintClientMetadata(c *gin.Context, value *provider.Record, body map[string]any) bool {
	return openai.ApplyCodexFingerprintClientMetadata(body, StagedCodexFingerprintIDs(c, value))
}
