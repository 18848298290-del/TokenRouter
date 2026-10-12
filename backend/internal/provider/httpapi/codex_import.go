package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// CodexImportHandler binds administrator import requests and applies response idempotency.
type CodexImportHandler struct {
	idempotencyhttp.Executor
	core *provider.CodexImporter
}

func NewCodexImportHandler(core *provider.CodexImporter) *CodexImportHandler {
	return &CodexImportHandler{core: core}
}

func (h *CodexImportHandler) ImportCodexSession(c *gin.Context) {
	var req provider.CodexSessionImportRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
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
		response.BadRequest(c, "请输入 accessToken 或 Codex session JSON")
		return
	}

	h.ExecuteAdminIdempotentJSON(c, "admin.providers.import_codex_session", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.core.Import(ctx, req, entries)
	})
}
