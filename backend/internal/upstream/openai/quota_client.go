package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Quota exchanges authenticate each request with bearer or Agent Identity
// credentials and retain the task ID used for signing. If a 401 response
// identifies an invalid task, recovery can replace that task and replay the
// request once under refreshed authentication.
//
// Every response is read within the captured-body limit and closed before
// recovery or status reporting. Replay clones headers and recreates a body
// through GetBody, so a failed request cannot resend an already-consumed
// payload. The recovery flag prevents a second registration loop; final failure
// text is redacted before the observer and application error receive it. Raw
// response access supports array-shaped endpoints, while the object wrapper
// decodes map responses for quota operations.

// Quota constants choose the request timeout and Codex desktop header values
// used by authenticated account and redemption exchanges through ChatGPT backend endpoints.
const (
	openaiQuotaUpstreamTimeout  = 20 * time.Second
	openaiQuotaCodexBeta        = "codex-1"
	openaiQuotaCodexOriginator  = "Codex Desktop"
	openaiQuotaCodexLanguageTag = "zh-CN"
	openaiQuotaSecFetchSite     = "none"
	openaiQuotaSecFetchMode     = "no-cors"
	openaiQuotaSecFetchDest     = "empty"
)

// QuotaClientOptions supplies quota authentication, task recovery, and HTTP
// observation callbacks.
type QuotaClientOptions struct {
	// Available permits request authentication when true; false rejects
	// ApplyHeaders before its callbacks so missing execution context cannot create
	// a quota request.
	Available       bool
	UserAgent       string                                                                 // quota desktop User-Agent value
	URL             func(string, map[string]string) (string, error)                        // required backend endpoint builder
	Authenticate    func(context.Context) (authorization string, taskID string, err error) // required authorization and task resolver
	ProviderHeaders func(http.Header)                                                      // required account header writer
	Do              func(*http.Request) (*http.Response, error)                            // required HTTP exchange callback
	IsAgentIdentity func() bool                                                            // required agent authentication predicate
	// RecoverTask is invoked once for Agent Identity invalid-task evidence and
	// receives the task ID used by the failed request assertion.
	RecoverTask func(context.Context, string) error
	Redact      func(context.Context, []byte) []byte // required diagnostic body redactor
	Failure     func(int, string)                    // required quota status observer
}

// QuotaClient executes quota requests using the supplied authentication and
// transport options.
type QuotaClient struct {
	Options QuotaClientOptions // quota exchange callback configuration
}

// GetJSON builds and authenticates a quota GET and returns decoded object JSON.
// URL, authentication, transport, and observation callbacks are required on
// their paths; the request uses a twenty-second context and permits one
// invalid-task recovery.
func (s *QuotaClient) GetJSON(ctx context.Context, path string, query map[string]string) (map[string]any, error) {
	target, err := s.Options.URL(path, query)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
	// Keep the request and its one-time recovery within the same budget;
	// release this timeout when the quota operation returns to its provider caller.
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	expectedTaskID, err := s.ApplyHeaders(req)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to build upstream authentication: %v", err)
	}
	return s.DoJSON(req, expectedTaskID)
}

// GetJSONRaw builds an authenticated quota GET and returns bounded successful
// response bytes. The twenty-second context covers exchange and optional task
// recovery; callers can decode arrays or other top-level JSON shapes
// themselves.
func (s *QuotaClient) GetJSONRaw(ctx context.Context, path string, query map[string]string) ([]byte, error) {
	target, err := s.Options.URL(path, query)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
	// Keep the request and its one-time recovery within the same budget;
	// release this timeout when the quota operation returns to its provider caller.
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	expectedTaskID, err := s.ApplyHeaders(req)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to build upstream authentication: %v", err)
	}
	return s.DoJSONRaw(req, expectedTaskID)
}

// PostJSON builds an authenticated quota POST with a replayable JSON body and
// decodes object output. Unsupported body values fail before sending; the
// twenty-second context covers sending and optional one-time invalid-task
// recovery.
func (s *QuotaClient) PostJSON(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	target, err := s.Options.URL(path, nil)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
	// Keep the request and its one-time recovery within the same budget;
	// release this timeout when the quota operation returns to its provider caller.
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	expectedTaskID, err := s.ApplyHeaders(req)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to build upstream authentication: %v", err)
	}
	return s.DoJSON(req, expectedTaskID)
}

// ApplyHeaders writes quota identity and returns the task ID used for
// authentication. Nil requests or unavailable options fail before callbacks;
// ProviderHeaders runs after the fixed request headers, and the returned task
// ID is passed into recovery.
func (s *QuotaClient) ApplyHeaders(req *http.Request) (string, error) {
	if req == nil || !s.Options.Available {
		return "", errors.New("openai quota request context is unavailable")
	}
	*req = *req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	expectedTaskID := ""
	auth, taskID, err := s.Options.Authenticate(req.Context())
	if err != nil {
		return "", err
	}
	expectedTaskID = taskID
	req.Header.Set("Authorization", auth)

	req.Header.Set("Accept", "application/json")
	req.Header.Set("OpenAI-Beta", openaiQuotaCodexBeta)
	req.Header.Set("OAI-Language", openaiQuotaCodexLanguageTag)
	req.Header.Set("originator", openaiQuotaCodexOriginator)
	req.Header.Set("X-OpenAI-Attach-Auth", "1")
	req.Header.Set("X-OpenAI-Attach-Integrity-State", "1")
	req.Header.Set("User-Agent", s.Options.UserAgent)
	req.Header.Set("sec-fetch-site", openaiQuotaSecFetchSite)
	req.Header.Set("sec-fetch-mode", openaiQuotaSecFetchMode)
	req.Header.Set("sec-fetch-dest", openaiQuotaSecFetchDest)
	req.Header.Set("priority", "u=4, i")
	s.Options.ProviderHeaders(req.Header)
	return expectedTaskID, nil
}

// DoJSON returns object JSON decoded from the bounded, recovered quota
// response. An empty successful body becomes an empty map, JSON null can return
// a nil map, and other decode failures are reported to the caller.
func (s *QuotaClient) DoJSON(req *http.Request, expectedTaskID string) (map[string]any, error) {
	body, err := s.DoJSONRaw(req, expectedTaskID)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, nil
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode openai quota response: %w", err)
	}
	return result, nil
}

// DoJSONRaw sends a quota request and returns bounded response bytes on a
// successful status. Every acquired response body is closed before returning,
// classifying failure, or attempting recovery. Callers provide the task ID used
// by ApplyHeaders and the required option callbacks for the selected
// authentication mode.
//
// The first invalid-task 401 for Agent Identity invokes RecoverTask with that
// expected ID, clones the replayable request, and signs again. Recovery is
// attempted at most once even if replay also fails. Final status failures are
// redacted before observation and application-error construction, keeping
// credentials out of those diagnostic consumers while retaining the upstream
// status category.
func (s *QuotaClient) DoJSONRaw(req *http.Request, expectedTaskID string) ([]byte, error) {
	for recovered := false; ; {
		resp, err := s.Options.Do(req)
		if err != nil {
			return nil, err
		}
		// Capture bounded bytes and close transport ownership before task registration
		// or failure classification, so neither path retains the failed exchange connection.
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, ImageErrorBodyReadLimit))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return body, nil
		}
		// Invalid task evidence permits one credential recovery. The expected ID
		// lets the coordinator distinguish this failed assertion from an already refreshed task.
		if !recovered && s.Options.IsAgentIdentity() && IsAgentTaskInvalidHTTPResponse(resp.StatusCode, body) {
			if err := s.Options.RecoverTask(req.Context(), expectedTaskID); err != nil {
				return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "agent identity task recovery failed: %v", err)
			}
			// Recreate the consumed body before signing a fresh assertion, so recovery
			// resends the same quota operation under its new registered task identity.
			retryReq, err := CloneOpenAIQuotaRequest(req)
			if err != nil {
				return nil, err
			}
			refreshedTaskID, err := s.ApplyHeaders(retryReq)
			if err != nil {
				return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to refresh upstream authentication: %v", err)
			}
			req = retryReq
			expectedTaskID = refreshedTaskID
			recovered = true
			continue
		}
		redactedBody := s.Options.Redact(req.Context(), body)
		message := strings.TrimSpace(string(redactedBody))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		s.Options.Failure(resp.StatusCode, message)
		return nil, infraerrors.Newf(infraerrors.Category(MapOpenAIQuotaUpstreamStatus(resp.StatusCode)), "OPENAI_QUOTA_UPSTREAM_ERROR", "openai quota upstream returned %d: %s", resp.StatusCode, message)
	}
}

// CloneOpenAIQuotaRequest clones headers and rebuilds body through GetBody for
// recovery. Nil input, recreation failure, or a nonempty body without GetBody
// returns an error; successful clones retain the original request context.
func CloneOpenAIQuotaRequest(req *http.Request) (*http.Request, error) {
	if req == nil {
		return nil, errors.New("openai quota request is nil")
	}
	cloned := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("clone openai quota request body: %w", err)
		}
		cloned.Body = body
	} else if req.Body != nil && req.Body != http.NoBody {
		return nil, errors.New("openai quota request body cannot be replayed")
	}
	return cloned, nil
}

// GenerateOpenAIQuotaRedeemRequestID returns a random UUID-shaped quota
// redemption request identifier.
func GenerateOpenAIQuotaRedeemRequestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Set UUID version and variant bits on random request bytes.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:]), nil
}

// MapOpenAIQuotaUpstreamStatus retains authentication and rate-limit statuses
// for quota errors. Other status values map to BadGateway, including other
// client errors, server errors, and unexpected nonerror statuses passed to this
// classifier.
func MapOpenAIQuotaUpstreamStatus(status int) int {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return status
	case status == http.StatusTooManyRequests:
		return http.StatusTooManyRequests
	case status >= 400 && status < 500:
		return http.StatusBadGateway
	case status >= 500:
		return http.StatusBadGateway
	default:
		return http.StatusBadGateway
	}
}
