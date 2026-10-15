package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
)

// batch_client.go uploads Gemini batch inputs, manages batch resources, and
// opens authenticated output downloads.
// Gemini image batches upload JSONL as a file resource and submit that
// resource to a model action. Job observations retain the server state and
// result references, while the task owner decides polling, cancellation, and
// resource cleanup. API errors keep bounded diagnostic text for internal
// interpretation before MapClientError selects a public message.
//
// File downloads first read metadata, preferring downloadUri over download_url
// and then the local resource action. The selected initial URL requires HTTPS
// and an allowed hostname before the API key is attached. The supplied HTTP
// client governs subsequent redirects. Successful downloads transfer
// response-body ownership to the caller; other exchanges close their bodies
// after decoding or checking status.

type (
	// GeminiUploadedFile describes the file resource returned by a JSONL upload.
	GeminiUploadedFile = wire.GeminiUploadedFile
	// GeminiBatchJob represents server job state and its possible output
	// references.
	GeminiBatchJob = wire.GeminiBatchJob
	// GeminiBatchDest carries alternative output-file spellings in the batch
	// destination.
	GeminiBatchDest = wire.GeminiBatchDest
	// GeminiBatchResponse carries file references or inline results from the
	// server.
	GeminiBatchResponse = wire.GeminiBatchResponse
	// GeminiBatchError holds the server-reported failure details for a batch job.
	GeminiBatchError = wire.GeminiBatchError
	// GeminiBatchHTTPClient exchanges API-key batch resources through a supplied
	// HTTP client.
	GeminiBatchHTTPClient struct {
		missingAPIKey error  // configured blank API-key error
		baseURL       string // normalized batch API base
		// client is retained directly for batch exchanges. Callers configure it
		// before sharing this wrapper because later configuration changes affect requests.
		client *http.Client
	}
)

// GeminiAPIError retains HTTP status and the selected Gemini error details.
type GeminiAPIError struct {
	StatusCode int    // upstream HTTP status code
	Code       string // upstream symbolic error status
	Message    string // selected upstream diagnostic text
}

// NewGeminiBatchHTTPClient retains a normalized API base, HTTP client, and
// missing-key error. Blank base uses AI Studio, nil client uses
// DefaultBatchHTTPClient, and nil missingAPIKey selects the standard provider
// input error.
func NewGeminiBatchHTTPClient(baseURL string, client *http.Client, missingAPIKey error) *GeminiBatchHTTPClient {
	if missingAPIKey == nil {
		missingAPIKey = apperror.BadRequest("BATCH_IMAGE_PROVIDER_MISSING_API_KEY", "batch image provider is missing api key")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = codeassist.AIStudioBaseURL
	}
	if client == nil {
		client = httpclient.DefaultBatchHTTPClient()
	}
	return &GeminiBatchHTTPClient{baseURL: baseURL, client: client, missingAPIKey: missingAPIKey}
}

// UploadJSONL buffers metadata and r into a multipart file upload, then
// decodes the returned file resource. The caller retains reader ownership;
// buffering occurs before request creation and its context-controlled
// exchange. A wrapped file is preferred over an unwrapped resource, and a
// successful response containing neither can return nil.
func (c *GeminiBatchHTTPClient) UploadJSONL(ctx context.Context, apiKey string, displayName string, r io.Reader) (*GeminiUploadedFile, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := textproto.MIMEHeader{}
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json; charset=utf-8")
	metadataPart, err := writer.CreatePart(metadataHeader)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{"file": map[string]any{"displayName": displayName, "mimeType": "application/jsonl"}}
	if err := json.NewEncoder(metadataPart).Encode(metadata); err != nil {
		return nil, err
	}
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", `form-data; name="file"; filename="batch.jsonl"`)
	fileHeader.Set("Content-Type", "application/jsonl")
	filePart, err := writer.CreatePart(fileHeader)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(filePart, r); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/upload/v1beta/files?uploadType=multipart", apiKey, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	var resp struct {
		// File and the embedded GeminiUploadedFile decode wrapped and unwrapped
		// upload forms. A nonnil File takes precedence; an absent resource in both
		// forms returns nil.
		File                *GeminiUploadedFile `json:"file"` // optional wrapped upload resource
		*GeminiUploadedFile                     // optional unwrapped upload resource
	}
	if err := c.doJSON(req, &resp); err != nil {
		return nil, err
	}
	// Decode both server upload forms and prefer the wrapper when present so a
	// file resource has one selected identity for batch submission.
	if resp.File != nil {
		return resp.File, nil
	}
	return resp.GeminiUploadedFile, nil
}

// CreateBatch submits an uploaded file resource to the trimmed and
// path-escaped model action. It closes and decodes the response; callers
// validate model and file identity and own the later batch lifecycle.
func (c *GeminiBatchHTTPClient) CreateBatch(ctx context.Context, apiKey string, model string, fileName string, displayName string) (*GeminiBatchJob, error) {
	body := map[string]any{
		"batch": map[string]any{
			"displayName": displayName,
			"inputConfig": map[string]any{
				"fileName": fileName,
			},
		},
	}
	// The request contains fixed maps and string values, so JSON encoding cannot
	// encounter an unsupported value or a cycle before submission.
	payload, _ := json.Marshal(body)
	path := fmt.Sprintf("/v1beta/models/%s:batchGenerateContent", url.PathEscape(strings.TrimSpace(model)))
	req, err := c.newRequest(ctx, http.MethodPost, path, apiKey, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doBatchJob(req)
}

// GetBatch reads a caller-supplied batch resource name after removing leading
// slashes. It returns one decoded server observation and closes the response;
// the caller decides subsequent polling.
func (c *GeminiBatchHTTPClient) GetBatch(ctx context.Context, apiKey string, batchName string) (*GeminiBatchJob, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1beta/"+strings.TrimLeft(batchName, "/"), apiKey, nil)
	if err != nil {
		return nil, err
	}
	return c.doBatchJob(req)
}

// CancelBatch posts to the named cancellation action with API-key
// authentication and closes the response. A successful status returns nil;
// callers observe later job state to establish cancellation completion.
func (c *GeminiBatchHTTPClient) CancelBatch(ctx context.Context, apiKey string, batchName string) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1beta/"+strings.TrimLeft(batchName, "/")+":cancel", apiKey, nil)
	if err != nil {
		return err
	}
	return c.doNoBody(req)
}

// DownloadFile resolves metadata and opens the selected authenticated file
// stream with its content type. The caller closes a successful body, and the
// request context participates in cancellation; metadata and error-response
// bodies are closed here.
//
// Metadata downloadUri takes precedence over download_url, then the configured
// resource download action is used. The initial destination is checked before
// adding the API key; redirect behavior belongs to the supplied client.
// Content type prefers the response header, then metadata, then
// application/octet-stream.
func (c *GeminiBatchHTTPClient) DownloadFile(ctx context.Context, apiKey string, fileName string) (io.ReadCloser, string, error) {
	metaReq, err := c.newRequest(ctx, http.MethodGet, "/v1beta/"+strings.TrimLeft(fileName, "/"), apiKey, nil)
	if err != nil {
		return nil, "", err
	}
	var metadata struct {
		// DownloadURI and DownloadURL represent metadata alternatives. A nonblank
		// trimmed DownloadURI wins, then DownloadURL, and an absent pair selects the
		// configured resource download action.
		DownloadURI string `json:"downloadUri"`  // preferred download URI spelling
		DownloadURL string `json:"download_url"` // alternate download URL spelling
		// MimeType supplies download content type when the response header is empty;
		// an empty metadata type selects the generic binary default.
		MimeType string `json:"mimeType"` // metadata response MIME type
	}
	if err := c.doJSON(metaReq, &metadata); err != nil {
		return nil, "", err
	}
	downloadURL := strings.TrimSpace(metadata.DownloadURI)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(metadata.DownloadURL)
	}
	if downloadURL == "" {
		downloadURL = c.baseURL + "/v1beta/" + strings.TrimLeft(fileName, "/") + ":download"
	}
	// Check the selected initial destination before attaching the API key;
	// upstream metadata can otherwise send authentication to a different
	// hostname.
	if err := validateGeminiDownloadHost(downloadURL, c.baseURL); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("x-goog-api-key", apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, "", readGeminiAPIError(resp)
	}
	// Select response MIME information before metadata and the generic binary
	// default so the consumer receives the most direct description of these
	// downloaded bytes.
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = metadata.MimeType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return resp.Body, contentType, nil
}

// DeleteFile deletes a caller-supplied file resource after removing leading
// slashes and closes the response. Non-success statuses return GeminiAPIError;
// the task owner coordinates cleanup with consumers still reading that file.
func (c *GeminiBatchHTTPClient) DeleteFile(ctx context.Context, apiKey string, fileName string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/v1beta/"+strings.TrimLeft(fileName, "/"), apiKey, nil)
	if err != nil {
		return err
	}
	return c.doNoBody(req)
}

// doBatchJob exchanges req and returns a decoded job with an empty auxiliary
// Raw map. Raw is excluded from JSON, so callers populate auxiliary
// observations separately from server fields.
func (c *GeminiBatchHTTPClient) doBatchJob(req *http.Request) (*GeminiBatchJob, error) {
	var job GeminiBatchJob
	if err := c.doJSON(req, &job); err != nil {
		return nil, err
	}
	job.Raw = map[string]any{}
	return &job, nil
}

// doNoBody sends req and closes the response after checking HTTP status.
// Successful content is discarded; non-success responses return the bounded
// Gemini error interpretation to the caller.
func (c *GeminiBatchHTTPClient) doNoBody(req *http.Request) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readGeminiAPIError(resp)
	}
	return nil
}

// doJSON sends req and decodes one JSON value into out for successful HTTP
// statuses. It closes the response body and returns transport, server-status,
// or decoding errors.
func (c *GeminiBatchHTTPClient) doJSON(req *http.Request, out any) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readGeminiAPIError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// newRequest builds an authenticated request by appending path to the
// configured base. A blank trimmed API key returns missingAPIKey; the supplied
// key spelling is retained in the header.
func (c *GeminiBatchHTTPClient) newRequest(ctx context.Context, method, path, apiKey string, body io.Reader) (*http.Request, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, c.missingAPIKey
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", apiKey)
	return req, nil
}

// validateGeminiDownloadHost checks the initial URI for an HTTPS scheme and
// an allowed hostname. URL parsing folds scheme case; it accepts googleapis.com,
// its subdomains,
// or the configured base hostname; hostname comparison ignores ports, and
// client redirect policy is separate.
func validateGeminiDownloadHost(downloadURL, baseURL string) error {
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return ProviderError("GEMINI_INVALID_RESPONSE", "Gemini download uri is invalid", err)
	}
	if parsed.Scheme != "https" {
		return ProviderError("GEMINI_INVALID_RESPONSE", "Gemini download uri must use https", nil)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "googleapis.com" || strings.HasSuffix(host, ".googleapis.com") {
		return nil
	}
	if base, err := url.Parse(baseURL); err == nil && strings.EqualFold(base.Hostname(), host) {
		return nil
	}
	return ProviderError("GEMINI_INVALID_RESPONSE", "Gemini download uri host is not allowed", nil)
}

// Error formats the HTTP status and diagnostic message, including a symbolic
// code when present. A nil receiver produces <nil> for diagnostic formatting
// of an absent typed error.
func (e *GeminiAPIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code != "" {
		return fmt.Sprintf("gemini api error: status=%d code=%s message=%s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("gemini api error: status=%d message=%s", e.StatusCode, e.Message)
}

// readGeminiAPIError reads at most eight KiB and selects a nonempty structured
// error message when decoding succeeds. Read errors are ignored; malformed or
// message-free JSON retains its collected text as the diagnostic.
func readGeminiAPIError(resp *http.Response) error {
	// Bound collected diagnostics before structured decoding. Partial reads still
	// provide their prefix, while malformed JSON keeps that text for internal
	// failure reporting.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	message := string(body)
	var parsed struct {
		Error struct { // structured upstream error details
			Code    any    `json:"code"`    // decoded upstream error code
			Message string `json:"message"` // upstream error message text
			Status  string `json:"status"`  // upstream symbolic error status
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		message = parsed.Error.Message
		return &GeminiAPIError{StatusCode: resp.StatusCode, Code: parsed.Error.Status, Message: message}
	}
	return &GeminiAPIError{StatusCode: resp.StatusCode, Message: message}
}

// ProviderError creates a bad-gateway application error for the supplied
// public reason and message. A nonnil cause is attached for internal error
// traversal; callers choose text suitable for client display.
func ProviderError(reason, message string, cause error) error {
	err := apperror.New(apperror.CategoryBadGateway, reason, message)
	if cause != nil {
		return err.WithCause(cause)
	}
	return err
}

// MapClientError converts GeminiAPIError statuses into authentication,
// rate-limit, missing-resource, or generic provider failures. Nil input
// returns nil, and every mapped failure uses fixed client text with the
// original diagnostic omitted.
func MapClientError(err error) error {
	if err == nil {
		return nil
	}
	// Replace server diagnostics with fixed application messages because batch
	// failures can contain credential or resource details unsuitable for the
	// caller-facing result.
	if apiErr, ok := errors.AsType[*GeminiAPIError](err); ok {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ProviderError("GEMINI_AUTH_FAILED", "Gemini authentication failed", nil)
		case http.StatusTooManyRequests:
			return ProviderError("GEMINI_RATE_LIMITED", "Gemini rate limit exceeded", nil)
		case http.StatusNotFound:
			return ProviderError("GEMINI_BATCH_NOT_FOUND", "Gemini batch resource was not found", nil)
		default:
			return ProviderError("GEMINI_INVALID_RESPONSE", "Gemini API request failed", nil)
		}
	}
	return ProviderError("GEMINI_INVALID_RESPONSE", "Gemini API request failed", nil)
}
