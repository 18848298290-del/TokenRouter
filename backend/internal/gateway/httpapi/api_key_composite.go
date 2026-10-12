package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

const CompositeKeyNoGroupContextKey = "composite_key_no_group"

// CompositeModelResponseWriter restores the client composite model in protocol response metadata.
type CompositeModelResponseWriter struct {
	gin.ResponseWriter
	clientModel string
	actualModel string
}

// ResolveCompositeAPIKeyRequest selects the group from the model prefix and strips that prefix for forwarding.
// @project-doc docs/domains/composite_api_keys.md#group_selection
func ResolveCompositeAPIKeyRequest(c *gin.Context, apiKeyService *apikey.APIKeyService, apiKey *apikey.APIKey) (*apikey.APIKey, error) {
	if apiKey == nil || !apiKey.IsComposite {
		return apiKey, nil
	}
	if IsCompositeKeyUnsupportedEndpoint(c.Request.Method, c.Request.URL.Path, c.FullPath()) {
		return nil, apikey.ErrCompositeKeyUnsupported
	}
	if IsCompositeKeyNoModelEndpoint(c.Request.Method, c.Request.URL.Path) {
		c.Set(CompositeKeyNoGroupContextKey, true)
		return apiKey, nil
	}

	var originalModel string
	var actualModel string
	var binding *apikey.APIKeyCompositeGroup
	var err error
	if IsGeminiNativeModelEndpoint(c.Request.URL.Path) {
		originalModel, err = CompositeGeminiModelFromParams(c)
		if err == nil {
			binding, actualModel, err = apiKey.ResolveCompositeModel(originalModel)
		}
		if err == nil {
			RewriteCompositeGeminiParams(c, actualModel)
		}
	} else {
		originalModel, err = CompositeModelFromRequest(c.Request)
		if err == nil {
			binding, actualModel, err = apiKey.ResolveCompositeModel(originalModel)
		}
		if err == nil {
			err = RewriteCompositeAdditionalModels(c.Request, apiKey, binding)
		}
		if err == nil {
			err = RewriteCompositeRequestModel(c.Request, actualModel)
		}
	}
	if err != nil {
		return nil, err
	}

	selected, err := apiKeyService.SelectCompositeGroupForRequest(c.Request.Context(), apiKey, binding)
	if err != nil {
		return nil, err
	}
	SetCompositeModelContext(c, originalModel, actualModel)
	return selected, nil
}

// SetCompositeModelContext records client and internal model names for logging and response restoration.
func SetCompositeModelContext(c *gin.Context, clientModel, actualModel string) {
	if c == nil {
		return
	}
	c.Set("composite_client_model", clientModel)
	c.Set("composite_actual_model", actualModel)
	c.Writer = &CompositeModelResponseWriter{
		ResponseWriter: c.Writer,
		clientModel:    clientModel,
		actualModel:    actualModel,
	}
	if c.Request != nil {
		ctx := context.WithValue(c.Request.Context(), telemetry.ClientModel, clientModel)
		c.Request = c.Request.WithContext(ctx)
	}
}

func (w *CompositeModelResponseWriter) Write(data []byte) (int, error) {
	w.Header().Del("Content-Length")
	rewritten := ReplaceCompositeResponseModel(data, w.actualModel, w.clientModel)
	_, err := w.ResponseWriter.Write(rewritten)
	return len(data), err
}

func (w *CompositeModelResponseWriter) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

// ReplaceCompositeResponseModel rewrites protocol model fields while leaving content text unchanged.
func ReplaceCompositeResponseModel(data []byte, actualModel, clientModel string) []byte {
	return modeltrace.ReplaceModelMetadata(data, actualModel, clientModel)
}

// GetCompositeModelFromContext returns the composite key's client and internal model names.
func GetCompositeModelFromContext(c *gin.Context) (clientModel, actualModel string, ok bool) {
	if c == nil {
		return "", "", false
	}
	clientModel = c.GetString("composite_client_model")
	actualModel = c.GetString("composite_actual_model")
	return clientModel, actualModel, clientModel != "" && actualModel != ""
}

// IsCompositeKeyNoModelEndpoint reports whether an endpoint uses key identity or aggregates all group mappings.
func IsCompositeKeyNoModelEndpoint(method, path string) bool {
	if IsCompositeKeyModelListEndpoint(method, path) || IsAPIKeyUsageRequest(method, path) {
		return true
	}
	return IsBatchImageBillingBypassRequest(method, path) || IsGrokVideoTaskRead(method, path)
}

// IsCompositeKeyModelListEndpoint identifies model lists that aggregate composite key mappings.
func IsCompositeKeyModelListEndpoint(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	switch strings.TrimSuffix(path, "/") {
	case "/v1/models", "/models", "/v1beta/models", "/antigravity/models", "/antigravity/v1/models", "/antigravity/v1beta/models", "/v1/images/batches/models":
		return true
	default:
		return false
	}
}

// IsCompositeKeyBillingBypassEndpoint identifies reads of existing data that bypass consumption checks.
// Model lists still require key quota, balance, and subscription checks.
func IsCompositeKeyBillingBypassEndpoint(method, path string) bool {
	if IsAPIKeyUsageRequest(method, path) {
		return true
	}
	return IsBatchImageBillingBypassRequest(method, path) || IsGrokVideoTaskRead(method, path)
}

// IsGrokVideoTaskRead identifies video queries authorized by task ownership rather than a request model.
func IsGrokVideoTaskRead(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	cleanPath := strings.TrimSuffix(path, "/")
	return strings.HasPrefix(cleanPath, "/v1/videos/") || strings.HasPrefix(cleanPath, "/videos/")
}

// IsCompositeKeyUnsupportedEndpoint identifies realtime endpoints that can use multiple models per connection.
func IsCompositeKeyUnsupportedEndpoint(method, path, routePath string) bool {
	cleanPath := strings.TrimSuffix(path, "/")
	if cleanPath == "/v1/live" || cleanPath == "/backend-api/codex/realtime/calls" {
		return true
	}
	// Codex Live sideband shares its prefix with ordinary HTTP endpoints, so use the matched route template.
	if method == http.MethodGet && routePath == "/backend-api/codex/:call_id" {
		return true
	}
	if method == http.MethodGet && (strings.HasPrefix(cleanPath, "/v1/live/") || cleanPath == "/v1/responses" || cleanPath == "/responses" || cleanPath == "/backend-api/codex/responses") {
		return true
	}
	return false
}

func IsGeminiNativeModelEndpoint(path string) bool {
	return strings.Contains(path, "/v1beta/models/")
}

// CompositeGeminiModelFromParams extracts the prefixed model from Gemini URL parameters.
func CompositeGeminiModelFromParams(c *gin.Context) (string, error) {
	if c.Request.Method == http.MethodGet {
		model := strings.TrimPrefix(strings.TrimSpace(c.Param("model")), "/")
		if model == "" {
			return "", apikey.ErrCompositeKeyPrefixRequired
		}
		return model, nil
	}
	if prefix := strings.TrimSpace(c.Param("prefix")); prefix != "" {
		model := strings.TrimPrefix(strings.TrimSpace(c.Param("model")), "/")
		if model == "" {
			return "", apikey.ErrCompositeKeyPrefixRequired
		}
		return prefix + "/" + model, nil
	}
	modelAction := strings.TrimPrefix(strings.TrimSpace(c.Param("modelAction")), "/")
	separator := strings.LastIndex(modelAction, ":")
	if separator <= 0 {
		return "", apikey.ErrCompositeKeyPrefixRequired
	}
	return modelAction[:separator], nil
}

// RewriteCompositeGeminiParams replaces Gin model parameters with the model stripped of its group prefix.
func RewriteCompositeGeminiParams(c *gin.Context, actualModel string) {
	for i := range c.Params {
		switch c.Params[i].Key {
		case "model":
			c.Params[i].Value = actualModel
		case "modelAction":
			value := strings.TrimPrefix(c.Params[i].Value, "/")
			if separator := strings.LastIndex(value, ":"); separator > 0 {
				c.Params[i].Value = "/" + actualModel + value[separator:]
			}
		}
	}
}

// CompositeModelFromRequest reads the top-level model from JSON or multipart input.
func CompositeModelFromRequest(request *http.Request) (string, error) {
	mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") {
		return MultipartModel(request)
	}
	body, err := ReadAndRestoreRequestBody(request)
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return "", apikey.ErrCompositeKeyPrefixRequired
	}
	return model, nil
}

func RewriteCompositeRequestModel(request *http.Request, actualModel string) error {
	mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") {
		return RewriteMultipartModel(request, actualModel)
	}
	body, err := ReadAndRestoreRequestBody(request)
	if err != nil {
		return err
	}
	rewritten, err := sjson.SetBytes(body, "model", actualModel)
	if err != nil {
		return apikey.ErrCompositeKeyPrefixRequired
	}
	SetRequestBody(request, rewritten)
	return nil
}

// RewriteCompositeAdditionalModels resolves additional models declared by Responses tools.
// It strips same-group prefixes and rejects cross-group models because a request has one selected route.
func RewriteCompositeAdditionalModels(request *http.Request, apiKey *apikey.APIKey, selected *apikey.APIKeyCompositeGroup) error {
	mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") || apiKey == nil || selected == nil {
		return nil
	}
	body, err := ReadAndRestoreRequestBody(request)
	if err != nil {
		return err
	}
	rewritten := body
	for index, tool := range gjson.GetBytes(body, "tools").Array() {
		model := strings.TrimSpace(tool.Get("model").String())
		if model == "" {
			continue
		}
		binding, actualModel, resolveErr := apiKey.ResolveCompositeModel(model)
		if resolveErr != nil {
			// Model IDs can contain slashes; interpret a prefix only when it matches a configured group.
			continue
		}
		if binding.GroupID != selected.GroupID {
			return apikey.ErrCompositeKeyUnsupported
		}
		rewritten, err = sjson.SetBytes(rewritten, fmt.Sprintf("tools.%d.model", index), actualModel)
		if err != nil {
			return apikey.ErrCompositeKeyUnsupported
		}
	}
	SetRequestBody(request, rewritten)
	return nil
}

func ReadAndRestoreRequestBody(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, apikey.ErrCompositeKeyPrefixRequired
	}
	rawBody, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	SetRequestBody(request, rawBody)

	encoding := strings.ToLower(strings.TrimSpace(request.Header.Get("Content-Encoding")))
	if encoding == "" || encoding == "identity" {
		return rawBody, nil
	}

	// Decompress a cloned request so downstream handlers can read the full compressed body after a failure.
	decodeRequest := request.Clone(request.Context())
	decodeRequest.Body = io.NopCloser(bytes.NewReader(rawBody))
	decodeRequest.ContentLength = int64(len(rawBody))
	decodedBody, err := ReadRequestBodyWithPrealloc(decodeRequest)
	if err != nil {
		return nil, err
	}
	request.Header.Del("Content-Encoding")
	SetRequestBody(request, decodedBody)
	return decodedBody, nil
}

func SetRequestBody(request *http.Request, body []byte) {
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
}

func MultipartModel(request *http.Request) (string, error) {
	body, err := ReadAndRestoreRequestBody(request)
	if err != nil {
		return "", err
	}
	mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return "", apikey.ErrCompositeKeyPrefixRequired
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return "", nextErr
		}
		if part.FormName() == "model" {
			value, readErr := io.ReadAll(part)
			if readErr != nil {
				return "", readErr
			}
			model := strings.TrimSpace(string(value))
			if model == "" {
				return "", apikey.ErrCompositeKeyPrefixRequired
			}
			return model, nil
		}
	}
	return "", apikey.ErrCompositeKeyPrefixRequired
}

func RewriteMultipartModel(request *http.Request, actualModel string) error {
	body, err := ReadAndRestoreRequestBody(request)
	if err != nil {
		return err
	}
	mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return apikey.ErrCompositeKeyPrefixRequired
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var output bytes.Buffer
	writer := multipart.NewWriter(&output)
	found := false
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		target, createErr := writer.CreatePart(part.Header)
		if createErr != nil {
			return createErr
		}
		if part.FormName() == "model" {
			_, err = io.WriteString(target, actualModel)
			found = true
		} else {
			_, err = io.Copy(target, part)
		}
		if err != nil {
			return err
		}
	}
	if !found {
		return apikey.ErrCompositeKeyPrefixRequired
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	SetRequestBody(request, output.Bytes())
	return nil
}

// AbortCompositeKeyError writes a structured composite key error in the endpoint's protocol.
func AbortCompositeKeyError(c *gin.Context, err error) {
	status := httpx.ErrorCode(err)
	code := infraerrors.Reason(err)
	message := infraerrors.Message(err)
	if IsSystemOneEndpoint(c.Request.URL.Path) {
		WriteSystemOneError(c, status, "invalid_request_error", code, "model", message)
		c.Abort()
		return
	}
	if IsOpenAICompositeEndpoint(c.Request.URL.Path) {
		c.JSON(status, gin.H{"error": gin.H{
			"message": message, "type": "invalid_request_error", "param": "model", "code": code,
		}})
		c.Abort()
		return
	}
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": "invalid_request_error", "message": message, "code": code},
	})
	c.Abort()
}

func IsOpenAICompositeEndpoint(path string) bool {
	return strings.Contains(path, "/chat/completions") || strings.Contains(path, "/responses") ||
		strings.Contains(path, "/embeddings") || strings.Contains(path, "/images/") ||
		strings.Contains(path, "/videos/") || strings.Contains(path, "/alpha/search") ||
		strings.Contains(path, "/live") || strings.Contains(path, "/realtime/") ||
		strings.HasPrefix(path, "/backend-api/codex/")
}

// AbortCompositeKeyGoogleError writes a composite key error in Google format.
func AbortCompositeKeyGoogleError(c *gin.Context, err error) {
	keyhttp.AbortGoogleError(c, httpx.ErrorCode(err), infraerrors.Reason(err)+": "+infraerrors.Message(err))
}
