package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// codex_import.go validates Codex session import requests and delegates
// accepted entries through response idempotency.
// Codex session imports accept token text or structured session entries,
// including Agent Identity material. The handler removes retired Extra values
// before response idempotency, rejects invalid numeric options and empty parsed
// input, and leaves per-entry normalization, identity matching and provider
// writes to CodexImporter.

// CodexImportHandler binds strict Codex import requests and returns idempotent
// item outcomes.
type CodexImportHandler struct {
	idempotencyhttp.Executor                         // administrator write idempotency binding
	core                     *provider.CodexImporter // borrowed credential import use case
}

// NewCodexImportHandler borrows the Codex importer for administrator credential
// imports. Bind response idempotency separately when desired, and keep importer
// dependencies available until mounted HTTP requests have completed.
func NewCodexImportHandler(core *provider.CodexImporter) *CodexImportHandler {
	return &CodexImportHandler{core: core}
}

// ImportCodexSession strictly binds and validates numeric import options before
// parsing the supplied session content. Retired Extra keys are removed before
// fingerprinting; empty parsed entries return 400 and admitted requests use
// administrator response idempotency.
func (h *CodexImportHandler) ImportCodexSession(c *gin.Context) {
	var req provider.CodexSessionImportRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// Retired Extra content is ignored by import. Remove it before fingerprinting
	// so malformed historical values do not distinguish otherwise equivalent requests.
	provider.DiscardDeprecatedExtra(req.Extra)
	if req.Concurrency != nil && *req.Concurrency < 0 {
		response.BadRequest(c, "concurrency must be >= 0")
		return
	}
	if req.Priority != nil && *req.Priority < 0 {
		response.BadRequest(c, "priority must be >= 0")
		return
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	if req.LoadFactor != nil && *req.LoadFactor > 10000 {
		response.BadRequest(c, "load_factor must be <= 10000")
		return
	}

	entries, err := provider.ParseCodexSessionImportEntries(req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if len(entries) == 0 {
		response.BadRequest(c, "Enter an accessToken or Codex session JSON")
		return
	}

	h.ExecuteAdminIdempotentJSON(c, "admin.providers.import_codex_session", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.core.Import(ctx, req, entries)
	})
}
