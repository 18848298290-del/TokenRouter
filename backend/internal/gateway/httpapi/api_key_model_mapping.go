package httpapi

import (
	"context"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// ApiKeyModelResponseWriter restores protocol model metadata for registered internal model names.
type ApiKeyModelResponseWriter struct {
	gin.ResponseWriter
	trace       *modeltrace.APIKeyModelRedirectTrace
	clientModel string
}

// ApplyAPIKeyModelRedirect applies key-level model redirection after composite group selection.
// On parse failure, the protocol handler receives the request and reports validation errors.
// @project-doc docs/domains/api_key_model_redirects.md#redirect_order
func ApplyAPIKeyModelRedirect(c *gin.Context, apiKey *apikey.APIKey) {
	if c == nil || c.Request == nil || apiKey == nil || len(apiKey.ModelMapping) == 0 {
		return
	}

	sourceModel, geminiModel, ok := ApiKeyRequestModel(c)
	if !ok {
		_ = RewriteAPIKeyAdditionalModels(c.Request, apiKey)
		return
	}
	targetModel, matched := apiKey.ResolveModelMapping(sourceModel)
	if !matched {
		_ = RewriteAPIKeyAdditionalModels(c.Request, apiKey)
		return
	}

	if geminiModel {
		RewriteCompositeGeminiParams(c, targetModel)
	} else if err := RewriteCompositeRequestModel(c.Request, targetModel); err != nil {
		return
	}
	_ = RewriteAPIKeyAdditionalModels(c.Request, apiKey)
	SetAPIKeyModelRedirectContext(c, sourceModel, targetModel)
}

// ApiKeyRequestModel reads the primary model and reports whether it came from a Gemini URL and was parsed successfully.
func ApiKeyRequestModel(c *gin.Context) (string, bool, bool) {
	if IsGeminiNativeModelEndpoint(c.Request.URL.Path) {
		model, err := CompositeGeminiModelFromParams(c)
		return model, true, err == nil && strings.TrimSpace(model) != ""
	}
	model, err := CompositeModelFromRequest(c.Request)
	return model, false, err == nil && strings.TrimSpace(model) != ""
}

// RewriteAPIKeyAdditionalModels redirects additional models declared by Responses tools.
func RewriteAPIKeyAdditionalModels(request *http.Request, apiKey *apikey.APIKey) error {
	if request == nil || apiKey == nil {
		return nil
	}
	mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") {
		return nil
	}
	body, err := ReadAndRestoreRequestBody(request)
	if err != nil {
		return err
	}
	rewritten, err := modeltrace.RewriteAPIKeyAdditionalModels(body, apiKey.ModelMapping)
	if err != nil {
		return err
	}
	SetRequestBody(request, rewritten)
	return nil
}

// SetAPIKeyModelRedirectContext records the client model for logs and installs response model restoration.
func SetAPIKeyModelRedirectContext(c *gin.Context, sourceModel, targetModel string) {
	clientModel := strings.TrimSpace(sourceModel)
	responseModel := clientModel
	if compositeClient, compositeActual, ok := GetCompositeModelFromContext(c); ok {
		clientModel = compositeClient
		responseModel = compositeActual
	}

	trace := modeltrace.NewAPIKeyModelRedirectTrace(clientModel, sourceModel, targetModel)
	ctx := modeltrace.WithContext(c.Request.Context(), trace)
	if existing, ok := ctx.Value(telemetry.ClientModel).(string); !ok || strings.TrimSpace(existing) == "" {
		ctx = context.WithValue(ctx, telemetry.ClientModel, clientModel)
	}
	c.Request = c.Request.WithContext(ctx)
	c.Writer = &ApiKeyModelResponseWriter{
		ResponseWriter: c.Writer,
		trace:          trace,
		clientModel:    responseModel,
	}
}

func (w *ApiKeyModelResponseWriter) Write(data []byte) (int, error) {
	w.Header().Del("Content-Length")
	w.trace.RegisterResponsePayload(data)
	rewritten := data
	for _, model := range w.trace.ResponseModels() {
		rewritten = ReplaceCompositeResponseModel(rewritten, model, w.clientModel)
	}
	_, err := w.ResponseWriter.Write(rewritten)
	return len(data), err
}

func (w *ApiKeyModelResponseWriter) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

// APIKeyModelRedirectContext applies one key-level redirect for entries whose model is outside the HTTP body.
func APIKeyModelRedirectContext(ctx context.Context, apiKey *apikey.APIKey, clientModel string) (context.Context, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	clientModel = strings.TrimSpace(clientModel)
	targetModel, matched := apiKey.ResolveModelMapping(clientModel)
	if !matched {
		return ctx, clientModel
	}
	trace := modeltrace.NewAPIKeyModelRedirectTrace(clientModel, clientModel, targetModel)
	ctx = modeltrace.WithContext(ctx, trace)
	ctx = context.WithValue(ctx, telemetry.ClientModel, clientModel)
	return ctx, targetModel
}

// PropagateAPIKeyModelRedirectTrace carries the redirect trace and client model into an asynchronous task context.
func PropagateAPIKeyModelRedirectTrace(dst, src context.Context) context.Context {
	trace, ok := modeltrace.FromContext(src)
	if !ok {
		return dst
	}
	if dst == nil {
		dst = context.Background()
	}
	dst = modeltrace.WithContext(dst, trace)
	if strings.TrimSpace(trace.ClientModel) != "" {
		dst = context.WithValue(dst, telemetry.ClientModel, trace.ClientModel)
	}
	return dst
}
