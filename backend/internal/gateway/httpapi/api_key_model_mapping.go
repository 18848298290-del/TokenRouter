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

// api_key_model_mapping.go applies API-key model redirects and restores client
// model names in responses. A key alias is resolved once before group and
// provider mappings run, so later routing starts from the key's selected
// target. HTTP input may carry the alias in JSON, multipart, or Gemini route
// parameters; tool-model aliases can be redirected even when the primary model
// has no matching rule. Parsing failures leave validation to the protocol
// handler instead of producing a redirect here.
//
// Response restoration follows the request's shared model trace as internal
// names become known. With a composite key, the inner writer restores the
// unprefixed alias and the already-installed outer writer restores its group
// prefix. Client telemetry keeps the prefixed identity while routing and response
// matching use the names appropriate to their stage.

// ApiKeyModelResponseWriter restores protocol model metadata for registered internal model names.
type ApiKeyModelResponseWriter struct {
	gin.ResponseWriter // downstream response output
	// trace is shared with request execution; modeltrace synchronizes registration
	// and snapshots while this writer discovers names from response payloads.
	trace *modeltrace.APIKeyModelRedirectTrace
	// clientModel is the restoration target. Composite requests use their unprefixed
	// model here so the outer CompositeModelResponseWriter can restore the prefix afterward.
	clientModel string
}

// ApplyAPIKeyModelRedirect applies key-level model redirection after composite group selection.
// On parse failure, the protocol handler receives the request and reports validation errors.
// Missing contexts, requests, keys, or mappings are ignored. The primary model
// resolves one rule, and tool models are considered independently of whether
// that primary rule matched.
//
// A successful primary rewrite installs a shared trace and response writer after
// tool-model rewriting. Failed primary rewriting leaves that restoration state
// uninstalled; tool rewrite errors are ignored so the protocol handler can
// validate the remaining request. Callers run this after prefix selection so
// a key alias cannot change which composite group was chosen.
// @project-doc docs/domains/api_key_model_redirects.md#redirect_order
func ApplyAPIKeyModelRedirect(c *gin.Context, apiKey *apikey.APIKey) {
	if c == nil || c.Request == nil || apiKey == nil || len(apiKey.ModelMapping) == 0 {
		return
	}

	sourceModel, geminiModel, ok := ApiKeyRequestModel(c)
	if !ok {
		// Tools may have usable aliases even when the primary model cannot be
		// parsed; leave rewriting failures for the protocol handler's request validation.
		_ = RewriteAPIKeyAdditionalModels(c.Request, apiKey)
		return
	}
	targetModel, matched := apiKey.ResolveModelMapping(sourceModel)
	if !matched {
		// A primary model without a key rule still permits independent tool aliases.
		// Ignore tool rewriting errors so the protocol handler validates this body.
		_ = RewriteAPIKeyAdditionalModels(c.Request, apiKey)
		return
	}

	if geminiModel {
		RewriteCompositeGeminiParams(c, targetModel)
	} else if err := RewriteCompositeRequestModel(c.Request, targetModel); err != nil {
		// Protocol handling supplies request validation on this path. Installing a
		// response trace here would claim a primary redirect that did not complete.
		return
	}
	// The primary redirect already succeeded. Keep its trace even if tools cannot
	// be rewritten, and let the protocol handler validate those remaining tool fields.
	_ = RewriteAPIKeyAdditionalModels(c.Request, apiKey)
	SetAPIKeyModelRedirectContext(c, sourceModel, targetModel)
}

// ApiKeyRequestModel reads the primary model and reports whether it came from a Gemini URL and was parsed successfully.
// Callers provide a context with a request. Parsing errors become a false
// success flag, allowing redirect middleware to leave validation to the protocol handler.
func ApiKeyRequestModel(c *gin.Context) (string, bool, bool) {
	// Extraction failures disable redirection at this stage. The protocol handler
	// still receives the request and owns its structural or missing-model validation.
	if IsGeminiNativeModelEndpoint(c.Request.URL.Path) {
		model, err := CompositeGeminiModelFromParams(c)
		return model, true, err == nil && strings.TrimSpace(model) != ""
	}
	model, err := CompositeModelFromRequest(c.Request)
	return model, false, err == nil && strings.TrimSpace(model) != ""
}

// RewriteAPIKeyAdditionalModels redirects additional models declared by Responses tools.
// Nil requests or keys and multipart bodies are ignored. Successful processing
// installs the rewritten readable body and length; read or replacement errors
// leave the caller to decide whether to reject or defer validation.
func RewriteAPIKeyAdditionalModels(request *http.Request, apiKey *apikey.APIKey) error {
	if request == nil || apiKey == nil {
		return nil
	}
	// Multipart fields belong to the media request parser. Other parsed types use
	// JSON tool-model handling even when content-type parameter parsing reports an error.
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

// SetAPIKeyModelRedirectContext records the client model for logs and installs
// response model restoration. Callers provide a request-bearing context after
// its primary key alias has been rewritten to targetModel.
//
// Composite requests keep the prefixed client identity in the trace, while this
// writer targets the unprefixed alias for the outer composite writer to restore.
// Existing nonempty client telemetry takes precedence over this installation.
// The request context and writer share one trace, allowing later routing stages
// and observed response models to participate in the same restoration.
func SetAPIKeyModelRedirectContext(c *gin.Context, sourceModel, targetModel string) {
	clientModel := strings.TrimSpace(sourceModel)
	responseModel := clientModel
	// The outer composite writer expects the unprefixed alias. Restore that stage
	// here while keeping the fully prefixed client identity for trace and telemetry.
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

// Write discovers response model names and restores each registered internal name.
// It removes Content-Length before sending rewritten bytes and returns the input
// byte count with the downstream error, including when the downstream write fails.
//
// Registering the current payload first lets a newly observed upstream name be
// restored in that same output chunk. Discovery requires complete JSON or valid
// SSE data lines in this slice. Each trace snapshot supplies a sorted model list,
// and replacements match complete byte patterns inside the supplied chunk;
// split patterns are not buffered across writes. Callers serialize writer use
// through their response output path.
func (w *ApiKeyModelResponseWriter) Write(data []byte) (int, error) {
	// Alias restoration can change response byte length. Drop an earlier length
	// before discovering and rewriting names so clients receive correctly framed output.
	w.Header().Del("Content-Length")
	w.trace.RegisterResponsePayload(data)
	rewritten := data
	for _, model := range w.trace.ResponseModels() {
		rewritten = ReplaceCompositeResponseModel(rewritten, model, w.clientModel)
	}
	_, err := w.ResponseWriter.Write(rewritten)
	return len(data), err
}

// WriteString sends string output through model discovery and alias restoration.
// Its result counts supplied string bytes, and downstream write errors are
// returned using the same rules as Write.
func (w *ApiKeyModelResponseWriter) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

// APIKeyModelRedirectContext applies one key-level redirect for entries whose model is outside the HTTP body.
// A nil context becomes a background context, and unmatched or nil-key input
// returns the trimmed client model. A match installs a shared redirect trace
// and client telemetry before returning the target to WS or Live execution.
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
// With no source trace, dst is returned as supplied. Otherwise a nil destination
// becomes a background context and shares the source trace, including registrations
// made by later routing or output work.
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
