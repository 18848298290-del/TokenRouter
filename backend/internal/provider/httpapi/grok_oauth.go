package httpapi

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/httpapi/dto"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type GrokGenerateAuthURLRequest struct {
	ProxyID     *int64 `json:"proxy_id"`
	RedirectURI string `json:"redirect_uri"`
}

type GrokExchangeCodeRequest struct {
	SessionID   string `json:"session_id" binding:"required"`
	Code        string `json:"code" binding:"required"`
	State       string `json:"state"`
	RedirectURI string `json:"redirect_uri"`
	ProxyID     *int64 `json:"proxy_id"`
}

type GrokRefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
	RT           string `json:"rt"`
	ClientID     string `json:"client_id"`
	ProxyID      *int64 `json:"proxy_id"`
}

type GrokSSOTokenRequest struct {
	SSOToken string `json:"sso_token"`
	ProxyID  *int64 `json:"proxy_id"`
}

type GrokPasswordAuthorizeRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	ProxyID  *int64 `json:"proxy_id"`
}

type GrokOAuthReconcileRequest struct {
	DryRun               *bool `json:"dry_run"`
	Apply                bool  `json:"apply"`
	AfterID              int64 `json:"after_id"`
	Limit                int   `json:"limit"`
	RefreshWindowSeconds int64 `json:"refresh_window_seconds"`
}

type GrokSSOToOAuthRequest = providercore.GrokSSOToOAuthRequest

type GrokSSOToOAuthItemResult struct {
	Index    int           `json:"index"`
	Name     string        `json:"name,omitempty"`
	Email    string        `json:"email,omitempty"`
	Provider *dto.Provider `json:"provider,omitempty"`
	Error    string        `json:"error,omitempty"`
}

type GrokSSOToOAuthResponse struct {
	Created []GrokSSOToOAuthItemResult `json:"created"`
	Failed  []GrokSSOToOAuthItemResult `json:"failed"`
}

// GrokOAuthReconciler reconciles authorized credentials with provider records.
type GrokOAuthReconciler interface {
	ReconcileGrokOAuth(context.Context, providercore.GrokOAuthReconcileInput) (*providercore.GrokOAuthReconcileResult, error)
}
type GrokOAuthHTTPOptions struct {
	ProxyURL      func(context.Context, int64) (string, bool, error)
	RuntimeSanity func() any
	Reconciler    GrokOAuthReconciler
}
type GrokOAuthHandler struct {
	grokOAuthService *providercore.GrokAuthorization
	imports          *providercore.GrokProviderImport
	quotaService     *providercore.GrokQuotaService
	reconciler       GrokOAuthReconciler
	options          GrokOAuthHTTPOptions
}

func NewGrokOAuthHandler(auth *providercore.GrokAuthorization, imports *providercore.GrokProviderImport, quota *providercore.GrokQuotaService, options GrokOAuthHTTPOptions) *GrokOAuthHandler {
	return &GrokOAuthHandler{grokOAuthService: auth, imports: imports, quotaService: quota, reconciler: options.Reconciler, options: options}
}

func (h *GrokOAuthHandler) GetCapabilities(c *gin.Context) {
	response.Success(c, h.grokOAuthService.GetCapabilities())
}

func (h *GrokOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req GrokGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = GrokGenerateAuthURLRequest{}
	}
	result, err := h.grokOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, req.RedirectURI)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *GrokOAuthHandler) ExchangeCode(c *gin.Context) {
	var req GrokExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.grokOAuthService.ExchangeCode(c.Request.Context(), &providercore.GrokExchangeCodeInput{
		SessionID: req.SessionID,

		Code: req.Code,

		State: req.State,

		RedirectURI: req.RedirectURI,

		ProxyID: req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *GrokOAuthHandler) RefreshToken(c *gin.Context) {
	var req GrokRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(req.RT)
	}
	if refreshToken == "" {
		response.BadRequest(c, "refresh_token is required")
		return
	}

	var proxyURL string
	if req.ProxyID != nil {
		value, found, err := h.options.ProxyURL(c.Request.Context(), *req.ProxyID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if !found {
			response.BadRequest(c, "GROK_OAUTH_PROXY_NOT_FOUND: proxy not found")
			return
		}
		proxyURL = value
	}
	tokenInfo, err := h.grokOAuthService.RefreshToken(c.Request.Context(), refreshToken, proxyURL, req.ClientID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

// ValidateSSOToken exchanges a Web SSO cookie for Build OAuth tokens.
// The response contains the resulting OAuth token information.
func (h *GrokOAuthHandler) ValidateSSOToken(c *gin.Context) {
	var req GrokSSOTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.grokOAuthService.ValidateSSOToken(c.Request.Context(), req.SSOToken, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

// AuthorizePassword exchanges an email and password for Build OAuth tokens through SSO.
// The response excludes the password and raw SSO token.
func (h *GrokOAuthHandler) AuthorizePassword(c *gin.Context) {
	var req GrokPasswordAuthorizeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.grokOAuthService.AuthorizePassword(c.Request.Context(), req.Email, req.Password, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *GrokOAuthHandler) RefreshProviderToken(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	value, err := h.imports.RefreshProvider(c.Request.Context(), id)
	if err != nil {
		if input, ok := errors.AsType[*providercore.GrokImportInputError](err); ok {
			response.BadRequest(c, input.Message)
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	response.Success(c, dto.ProviderFromRecord(value))
}

func (h *GrokOAuthHandler) ReconcileOAuthProviders(c *gin.Context) {
	var req GrokOAuthReconcileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}
	if req.Apply == dryRun {
		response.ErrorFrom(c, providercore.ErrGrokOAuthReconcileMode)
		return
	}
	if req.RefreshWindowSeconds < 0 || req.RefreshWindowSeconds > int64((24*time.Hour)/time.Second) {
		response.ErrorFrom(c, providercore.ErrGrokOAuthReconcileWindow)
		return
	}
	if h.reconciler == nil {
		response.InternalError(c, "Grok OAuth reconciliation service is unavailable")
		return
	}
	result, err := h.reconciler.ReconcileGrokOAuth(c.Request.Context(), providercore.GrokOAuthReconcileInput{
		DryRun: dryRun,

		Apply: req.Apply,

		AfterID: req.AfterID,

		Limit: req.Limit,

		RefreshWindow: time.Duration(req.RefreshWindowSeconds) * time.Second,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *GrokOAuthHandler) CreateProviderFromOAuth(c *gin.Context) {
	var req struct {
		SessionID   string  `json:"session_id" binding:"required"`
		Code        string  `json:"code" binding:"required"`
		State       string  `json:"state"`
		RedirectURI string  `json:"redirect_uri"`
		ProxyID     *int64  `json:"proxy_id"`
		Name        string  `json:"name"`
		Concurrency int     `json:"concurrency"`
		Priority    int     `json:"priority"`
		GroupIDs    []int64 `json:"group_ids"`
	}
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	value, err := h.imports.CreateFromOAuth(c.Request.Context(), providercore.GrokOAuthProviderCreateInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
		Name:        req.Name,
		Concurrency: req.Concurrency,
		Priority:    req.Priority,
		GroupIDs:    req.GroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ProviderFromRecord(value))
}

func (h *GrokOAuthHandler) CreateProvidersFromSSO(c *gin.Context) {
	var req GrokSSOToOAuthRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.imports.CreateFromSSO(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, providercore.ErrGrokSSOInput) {
			response.BadRequest(c, err.Error())
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	out := GrokSSOToOAuthResponse{Created: make([]GrokSSOToOAuthItemResult, 0, len(result.Created)), Failed: make([]GrokSSOToOAuthItemResult, 0, len(result.Failed))}
	convert := func(item providercore.GrokSSOToOAuthItemResult) GrokSSOToOAuthItemResult {
		return GrokSSOToOAuthItemResult{Index: item.Index, Name: item.Name, Email: item.Email, Error: item.Error, Provider: dto.ProviderFromRecord(item.Provider)}
	}
	for _, v := range result.Created {
		out.Created = append(out.Created, convert(v))
	}
	for _, v := range result.Failed {
		out.Failed = append(out.Failed, convert(v))
	}
	response.Success(c, out)
}

func (h *GrokOAuthHandler) QueryQuota(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "grok quota service is not enabled")
		return
	}
	result, err := h.quotaService.QueryQuota(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *GrokOAuthHandler) ResetQuota(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "grok quota service is not enabled")
		return
	}
	// ResetQuota returns GROK_QUOTA_RESET_UNSUPPORTED because xAI has no OAuth quota reset endpoint.
	//nolint:staticcheck // SA4023
	result, err := h.quotaService.ResetQuota(c.Request.Context(), providerID)
	if err != nil { //nolint:staticcheck // SA4023
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *GrokOAuthHandler) RuntimeSanity(c *gin.Context) {
	response.Success(c, h.options.RuntimeSanity())
}
