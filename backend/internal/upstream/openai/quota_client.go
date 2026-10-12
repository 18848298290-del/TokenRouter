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

const (
	openaiQuotaUpstreamTimeout  = 20 * time.Second
	openaiQuotaCodexBeta        = "codex-1"
	openaiQuotaCodexOriginator  = "Codex Desktop"
	openaiQuotaCodexLanguageTag = "zh-CN"
	openaiQuotaSecFetchSite     = "none"
	openaiQuotaSecFetchMode     = "no-cors"
	openaiQuotaSecFetchDest     = "empty"
)

type QuotaClientOptions struct {
	Available       bool
	UserAgent       string
	URL             func(string, map[string]string) (string, error)
	Authenticate    func(context.Context) (authorization string, taskID string, err error)
	ProviderHeaders func(http.Header)
	Do              func(*http.Request) (*http.Response, error)
	IsAgentIdentity func() bool
	RecoverTask     func(context.Context, string) error
	Redact          func(context.Context, []byte) []byte
	Failure         func(int, string)
}
type QuotaClient struct{ Options QuotaClientOptions }

func (s *QuotaClient) GetJSON(ctx context.Context, path string, query map[string]string) (map[string]any, error) {
	target, err := s.Options.URL(path, query)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
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

// GetJSONRaw returns response bytes so callers can accept arrays and other top-level JSON shapes.
func (s *QuotaClient) GetJSONRaw(ctx context.Context, path string, query map[string]string) ([]byte, error) {
	target, err := s.Options.URL(path, query)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
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

// ApplyHeaders authenticates each request and returns the task ID used by Agent Identity signing.
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

// DoJSONRaw reads and closes each response, recovering an invalid Agent task and replaying at most once.
func (s *QuotaClient) DoJSONRaw(req *http.Request, expectedTaskID string) ([]byte, error) {
	for recovered := false; ; {
		resp, err := s.Options.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, ImageErrorBodyReadLimit))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return body, nil
		}
		if !recovered && s.Options.IsAgentIdentity() && IsAgentTaskInvalidHTTPResponse(resp.StatusCode, body) {
			if err := s.Options.RecoverTask(req.Context(), expectedTaskID); err != nil {
				return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "agent identity task recovery failed: %v", err)
			}
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

// CloneOpenAIQuotaRequest clones headers and recreates the body for recovery replay.
// A nonempty body without GetBody cannot be replayed.
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

func GenerateOpenAIQuotaRedeemRequestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:]), nil
}

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
