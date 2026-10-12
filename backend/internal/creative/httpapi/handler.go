package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

const (
	// creativeMaxUploadPartSize is the default file-part limit; reading limit+1 detects oversize files.
	creativeMaxUploadPartSize = 40 << 20

	// Allow multipart fields and boundaries in addition to the image input byte limit.
	creativeMultipartOverheadBytes = 1 << 20
)

// CreativeHandler handles user-facing creative requests.
type CreativeHandler struct {
	enter   func() (func(), error)
	service CreativeUseCases
}

// creativeCreateRunRequest contains parsed multipart run inputs.
type creativeCreateRunRequest struct {
	GroupID       int64
	Model         string
	Operation     string
	Prompt        string
	SourceImages  []creative.CreativeInputImage
	Mask          *creative.CreativeInputImage
	ImageSize     string
	AspectRatio   string
	Quality       string
	Background    string
	ThinkingLevel string
}

// CreativeUseCases supplies run operations to the HTTP handler.
type CreativeUseCases interface {
	ListModels(context.Context, int64) (*creative.CreativeModelsResponse, error)
	GetCapabilities(context.Context) *creative.CreativeCapabilitiesResponse
	MaxAssetBytes() int64
	MaxTotalInputBytes() int64
	CreateRun(context.Context, creative.CreativeRunScope, creative.CreateCreativeRunParamsPublic, string) (*creative.CreativeRunPublic, error)
	ListRuns(context.Context, creative.CreativeRunScope, creative.CreativeRunFilter) (*creative.CreativeListRunsResponse, error)
	GetRun(context.Context, creative.CreativeRunScope, string) (*creative.CreativeRunPublic, error)
	GetOutputContent(context.Context, creative.CreativeRunScope, string, int) (*creative.CreativeOutputContent, error)
	AckOutput(context.Context, creative.CreativeRunScope, string, int) error
}

// NewCreativeHandler creates a handler with the supplied run operations.
func NewCreativeHandler(service CreativeUseCases) *CreativeHandler {
	return &CreativeHandler{service: service}
}

// creativeRunScopeFromRequest validates the browser workspace and binds it to the authenticated user.
func creativeRunScopeFromRequest(c *gin.Context, userID int64) (creative.CreativeRunScope, error) {
	workspaceID, err := creative.NormalizeCreativeWorkspaceID(c.GetHeader(creative.CreativeWorkspaceHeader))
	if err != nil {
		return creative.CreativeRunScope{}, err
	}
	return creative.CreativeRunScope{UserID: userID, WorkspaceID: workspaceID}, nil
}

// ListModels returns group and image-model combinations available to the current user.
// GET /api/v1/creative/models
func (h *CreativeHandler) ListModels(c *gin.Context) {
	if h.enter != nil {
		done, err := h.enter()
		if err != nil {
			response.ErrorFrom(c, apperror.New(apperror.CategoryServiceUnavailable, "TASKS_STOPPED", "task service is stopping"))
			return
		}
		defer done()
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	got, err := h.service.ListModels(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Place the array directly in the response envelope data field.
	response.Success(c, got.Data)
}

// ListCapabilities returns creative input limits and accepted image MIME types.
// GET /api/v1/creative/capabilities
func (h *CreativeHandler) ListCapabilities(c *gin.Context) {
	if h.enter != nil {
		done, err := h.enter()
		if err != nil {
			response.ErrorFrom(c, apperror.New(apperror.CategoryServiceUnavailable, "TASKS_STOPPED", "task service is stopping"))
			return
		}
		defer done()
	}

	if _, ok := middleware2.GetAuthSubjectFromContext(c); !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	response.Success(c, h.service.GetCapabilities(c.Request.Context()))
}

// CreateRun parses multipart/form-data and creates a creative run.
// POST /api/v1/creative/runs
func (h *CreativeHandler) CreateRun(c *gin.Context) {
	if h.enter != nil {
		done, err := h.enter()
		if err != nil {
			response.ErrorFrom(c, apperror.New(apperror.CategoryServiceUnavailable, "TASKS_STOPPED", "task service is stopping"))
			return
		}
		defer done()
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	scope, err := creativeRunScopeFromRequest(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	partLimit := int64(creativeMaxUploadPartSize)
	totalInputLimit := int64(64 << 20)
	if h.service != nil && h.service.MaxAssetBytes() > 0 {
		partLimit = h.service.MaxAssetBytes()
	}
	if h.service != nil && h.service.MaxTotalInputBytes() > 0 {
		totalInputLimit = h.service.MaxTotalInputBytes()
	}
	// Limit the body before parsing so uploaded files cannot accumulate beyond the service input limit.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, totalInputLimit+creativeMultipartOverheadBytes)
	req, err := parseCreativeCreateRunMultipart(c, partLimit, totalInputLimit)
	if err != nil {
		if errors.Is(err, creative.ErrCreativeAssetTooLarge) || errors.Is(err, creative.ErrCreativeInputTooLarge) {
			response.ErrorFrom(c, err)
			return
		}
		response.ErrorFrom(c, creative.ErrCreativeInvalidParams)
		return
	}
	got, err := h.service.CreateRun(c.Request.Context(), scope, creative.CreateCreativeRunParamsPublic{
		GroupID:       req.GroupID,
		Model:         req.Model,
		Operation:     req.Operation,
		Prompt:        req.Prompt,
		SourceImages:  req.SourceImages,
		Mask:          req.Mask,
		ImageSize:     req.ImageSize,
		AspectRatio:   req.AspectRatio,
		Quality:       req.Quality,
		Background:    req.Background,
		ThinkingLevel: req.ThinkingLevel,
	}, c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, got)
}

// parseCreativeCreateRunMultipart parses run fields and file parts under byte limits.
// It accepts model, operation, prompt, size, aspect ratio, quality, background,
// thinking level, and group ID fields.
// source_images accepts multiple uploads; mask supplies a single image.
func parseCreativeCreateRunMultipart(c *gin.Context, partLimit, totalInputLimit int64) (*creativeCreateRunRequest, error) {
	contentType := c.GetHeader("Content-Type")
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, http.ErrMissingBoundary
	}
	req := &creativeCreateRunRequest{}
	if totalInputLimit <= 0 {
		totalInputLimit = 64 << 20
	}
	var totalFileBytes int64
	reader := multipart.NewReader(c.Request.Body, boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(part.FormName())
		if name == "" {
			_ = part.Close()
			continue
		}
		fileName := strings.TrimSpace(part.FileName())
		if fileName != "" {
			if partLimit <= 0 {
				partLimit = creativeMaxUploadPartSize
			}
			remaining := totalInputLimit - totalFileBytes
			if remaining <= 0 {
				_ = part.Close()
				return nil, creative.ErrCreativeInputTooLarge
			}
			readLimit := min(remaining, partLimit)
			data, readErr := io.ReadAll(io.LimitReader(part, readLimit+1))
			_ = part.Close()
			if readErr != nil {
				if _, ok := errors.AsType[*http.MaxBytesError](readErr); ok {
					return nil, creative.ErrCreativeInputTooLarge
				}
				return nil, readErr
			}
			if int64(len(data)) > readLimit {
				if readLimit < partLimit {
					return nil, creative.ErrCreativeInputTooLarge
				}
				return nil, creative.ErrCreativeAssetTooLarge
			}
			totalFileBytes += int64(len(data))
			partMIME := normalizeCreativeUploadMime(strings.TrimSpace(part.Header.Get("Content-Type")), data)
			switch {
			case name == "mask":
				if len(data) > 0 {
					req.Mask = &creative.CreativeInputImage{Bytes: data, Mime: partMIME}
				}
			case name == "source_images" || name == "source_images[]" || strings.HasPrefix(name, "source_images["):
				if len(data) > 0 {
					req.SourceImages = append(req.SourceImages, creative.CreativeInputImage{Bytes: data, Mime: partMIME})
				}
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(part, 1<<20))
		_ = part.Close()
		if readErr != nil {
			return nil, readErr
		}
		value := strings.TrimSpace(string(data))
		switch name {
		case "group_id":
			groupID, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || groupID <= 0 {
				return nil, creative.ErrCreativeInvalidParams
			}
			req.GroupID = groupID
		case "model":
			req.Model = value
		case "operation":
			req.Operation = strings.ToLower(value)
		case "prompt":
			req.Prompt = value
		case "image_size":
			req.ImageSize = value
		case "aspect_ratio":
			req.AspectRatio = value
		case "quality":
			req.Quality = value
		case "output_format", "output_compression", "response_mime_type":
			// Output format is determined by the provider adapter; client format options are rejected.
			return nil, creative.ErrCreativeInvalidParams
		case "background":
			req.Background = strings.ToLower(value)
		case "thinking_level":
			req.ThinkingLevel = strings.ToLower(value)
		}
	}
	if req.GroupID <= 0 {
		return nil, creative.ErrCreativeInvalidParams
	}
	return req, nil
}

// normalizeCreativeUploadMime normalizes known image MIME types, then tries byte signatures.
func normalizeCreativeUploadMime(headerMIME string, data []byte) string {
	switch strings.ToLower(strings.TrimSpace(headerMIME)) {
	case "image/png":
		return "image/png"
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/webp":
		return "image/webp"
	}
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return strings.ToLower(strings.TrimSpace(headerMIME))
}

// ListRuns returns runs for the authenticated user and browser workspace.
// GET /api/v1/creative/runs
func (h *CreativeHandler) ListRuns(c *gin.Context) {
	if h.enter != nil {
		done, err := h.enter()
		if err != nil {
			response.ErrorFrom(c, apperror.New(apperror.CategoryServiceUnavailable, "TASKS_STOPPED", "task service is stopping"))
			return
		}
		defer done()
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	scope, err := creativeRunScopeFromRequest(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	got, err := h.service.ListRuns(c.Request.Context(), scope, creative.CreativeRunFilter{
		Status: strings.TrimSpace(c.Query("status")),
		Limit:  limit,
		Offset: 0,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Place the run array directly in the response envelope data field.
	response.Success(c, got.Data)
}

// ListActiveRuns pages runs awaiting a deliverable terminal state using an opaque cursor.
// GET /api/v1/creative/runs/active?limit=100&cursor=<opaque>
func (h *CreativeHandler) ListActiveRuns(c *gin.Context) {
	if h.enter != nil {
		done, err := h.enter()
		if err != nil {
			response.ErrorFrom(c, apperror.New(apperror.CategoryServiceUnavailable, "TASKS_STOPPED", "task service is stopping"))
			return
		}
		defer done()
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	scope, err := creativeRunScopeFromRequest(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	offset := 0
	if rawCursor := strings.TrimSpace(c.Query("cursor")); rawCursor != "" {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(rawCursor)
		if decodeErr != nil {
			response.ErrorFrom(c, creative.ErrCreativeInvalidParams)
			return
		}
		offset, err = strconv.Atoi(string(decoded))
		if err != nil || offset < 0 {
			response.ErrorFrom(c, creative.ErrCreativeInvalidParams)
			return
		}
	}
	got, err := h.service.ListRuns(c.Request.Context(), scope, creative.CreativeRunFilter{
		Status: "active",
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	nextCursor := ""
	if got.HasMore {
		nextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + len(got.Data))))
	}
	response.Success(c, gin.H{
		"items":       got.Data,
		"next_cursor": nextCursor,
		"has_more":    got.HasMore,
	})
}

// GetRun returns details for a run in the authenticated browser workspace.
// GET /api/v1/creative/runs/:id
func (h *CreativeHandler) GetRun(c *gin.Context) {
	if h.enter != nil {
		done, err := h.enter()
		if err != nil {
			response.ErrorFrom(c, apperror.New(apperror.CategoryServiceUnavailable, "TASKS_STOPPED", "task service is stopping"))
			return
		}
		defer done()
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	scope, err := creativeRunScopeFromRequest(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	got, err := h.service.GetRun(c.Request.Context(), scope, c.Param("id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, got)
}

// GetOutputContent returns transient image bytes or an expiry error mapped to HTTP 410.
// GET /api/v1/creative/runs/:id/outputs/:index/content
func (h *CreativeHandler) GetOutputContent(c *gin.Context) {
	if h.enter != nil {
		done, err := h.enter()
		if err != nil {
			response.ErrorFrom(c, apperror.New(apperror.CategoryServiceUnavailable, "TASKS_STOPPED", "task service is stopping"))
			return
		}
		defer done()
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	scope, err := creativeRunScopeFromRequest(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	outputIndex, err := strconv.Atoi(c.Param("index"))
	if err != nil || outputIndex < 0 {
		response.ErrorFrom(c, creative.ErrCreativeOutputNotFound)
		return
	}
	content, err := h.service.GetOutputContent(c.Request.Context(), scope, c.Param("id"), outputIndex)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, content.ContentType, content.Content)
}

// AckOutput records client receipt and requests deletion of the transient output.
// POST /api/v1/creative/runs/:id/outputs/:index/ack
func (h *CreativeHandler) AckOutput(c *gin.Context) {
	if h.enter != nil {
		done, err := h.enter()
		if err != nil {
			response.ErrorFrom(c, apperror.New(apperror.CategoryServiceUnavailable, "TASKS_STOPPED", "task service is stopping"))
			return
		}
		defer done()
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	scope, err := creativeRunScopeFromRequest(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	outputIndex, err := strconv.Atoi(c.Param("index"))
	if err != nil || outputIndex < 0 {
		response.ErrorFrom(c, creative.ErrCreativeOutputNotFound)
		return
	}
	if err := h.service.AckOutput(c.Request.Context(), scope, c.Param("id"), outputIndex); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"acked": true})
}

// BindActivity installs the shutdown admission check before serving requests; activity covers the response.
func (h *CreativeHandler) BindActivity(enter func() (func(), error)) { h.enter = enter }
