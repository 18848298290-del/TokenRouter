package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// codex_invite_client.go sends authenticated Codex invitation requests and
// classifies unavailable referral responses.
// Codex invitation operations use ChatGPT backend JSON endpoints with bearer
// credentials and desktop identity headers. Responses are read within the
// error-body limit and closed before ownership returns to the provider use
// case, including decoding and upstream-status failures.
//
// Referral-unavailable 403 responses carry a distinct application error because
// the invitation entry and existing reset credits have different availability.
// The classifier extracts structured detail when possible and retains
// status/detail metadata for callers; other status failures retain the generic
// upstream error category. The fixed backend target is assembled before the
// authenticated request is sent.

// Invitation constants select the desktop request identity and ChatGPT backend
// endpoint, and define the application error used for unavailable referral entry.
const (
	CodexInviteDefaultUserAgent        = "Codex Desktop/0.0.0 (Linux; x86_64)"
	codexBackendAPIBaseURL             = "https://chatgpt.com/backend-api"
	codexInviteResetUnavailable        = "CODEX_INVITE_RESET_REFERRAL_UNAVAILABLE"
	codexInviteResetUnavailableMessage = "Codex referral invitations are temporarily unavailable; existing resets can still be used"
)

// CodexInviteClient holds credentials and callbacks for ChatGPT invitation
// exchanges.
type CodexInviteClient struct {
	Token           string                                      // backend bearer access token
	UserAgent       string                                      // desktop request User-Agent value
	ProviderHeaders func(http.Header)                           // required provider metadata writer
	Do              func(*http.Request) (*http.Response, error) // configured HTTP exchange callback
}

// GetJSON sends an authenticated backend GET with the supplied query values and
// returns decoded JSON. The context controls the exchange; an empty successful
// body returns an empty map and transport or status failures propagate.
func (s *CodexInviteClient) GetJSON(ctx context.Context, path string, query map[string]string) (map[string]any, error) {
	target, err := BuildCodexBackendURL(path, query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	s.applyHeaders(req)
	return s.doJSON(req)
}

// PostJSON sends an authenticated backend POST with body encoded as JSON. The
// context controls the exchange; unsupported body values fail before sending,
// and response reading and closing are delegated to doJSON.
func (s *CodexInviteClient) PostJSON(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	target, err := BuildCodexBackendURL(path, nil)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.applyHeaders(req)
	return s.doJSON(req)
}

// applyHeaders writes the ChatGPT host, bearer token, desktop identity, and
// request profile into req. ProviderHeaders is required and runs last, allowing
// its provider-specific metadata to supplement the prepared request.
func (s *CodexInviteClient) applyHeaders(req *http.Request) {
	*req = *req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OpenAI-Beta", openaiQuotaCodexBeta)
	req.Header.Set("OAI-Language", "zh-CN")
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("X-OpenAI-Attach-Auth", "1")
	req.Header.Set("X-OpenAI-Attach-Integrity-State", "1")
	req.Header.Set("User-Agent", s.UserAgent)
	req.Header.Set("sec-fetch-site", "none")
	req.Header.Set("sec-fetch-mode", "no-cors")
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("priority", "u=4, i")
	s.ProviderHeaders(req.Header)
}

// doJSON executes and closes one invitation response, reading at most
// ImageErrorBodyReadLimit bytes. A missing transport returns an application
// error, empty success returns an empty map, and non-success statuses are
// classified before JSON decoding.
func (s *CodexInviteClient) doJSON(req *http.Request) (map[string]any, error) {
	if s.Do == nil {
		return nil, apperror.InternalServer("HTTP_UPSTREAM_NOT_CONFIGURED", "http upstream is not configured")
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	resp, err := s.Do(req)
	if err != nil {
		return nil, err
	}
	// Close the acquired body on status, read, and decode exits so the transport
	// releases its connection; a close failure leaves the primary result intact.
	defer func() { _ = resp.Body.Close() }()

	// Reuse the bounded error-body budget for both success and failure responses,
	// limiting captured invitation data before status classification or JSON decoding.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, ImageErrorBodyReadLimit))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		if err := codexInviteResetUpstreamBusinessError(resp.StatusCode, message); err != nil {
			return nil, err
		}
		return nil, apperror.Newf(apperror.Category(resp.StatusCode), "CODEX_INVITE_RESET_UPSTREAM_ERROR", "codex invite reset upstream returned %d: %s", resp.StatusCode, message)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, nil
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode codex invite reset response: %w", err)
	}
	return result, nil
}

// codexInviteResetUpstreamBusinessError returns a referral-unavailable error
// for a matching 403 detail. Other status/detail combinations return nil so
// doJSON can construct the generic upstream error, and matched errors retain
// upstream metadata.
func codexInviteResetUpstreamBusinessError(statusCode int, body string) error {
	detail := codexInviteResetUpstreamDetail(body)
	if statusCode != http.StatusForbidden || !strings.Contains(detail, "推荐邀请不可用") {
		return nil
	}
	// This 403 describes referral entry availability; existing reset credits can
	// still be used, so expose that distinction through the dedicated application error.
	return apperror.Forbidden(codexInviteResetUnavailable, codexInviteResetUnavailableMessage).WithMetadata(map[string]string{
		"upstream_status": fmt.Sprint(statusCode),
		"upstream_detail": detail,
	})
}

// codexInviteResetUpstreamDetail reads a usable detail field from JSON when
// available. Malformed JSON, missing detail, or an empty formatted detail falls
// back to the trimmed complete body for upstream classification.
func codexInviteResetUpstreamDetail(body string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err == nil {
		if detail := codexInviteDetail(payload["detail"]); detail != "" {
			return detail
		}
	}
	// Keep an unstructured upstream body as diagnostic detail when structured
	// extraction fails, allowing the same unavailable-referral match to handle either shape.
	return strings.TrimSpace(body)
}

// BuildCodexBackendURL appends path to the fixed ChatGPT backend base and
// encodes query values. A missing leading slash is added; callers choose
// backend paths and each supplied query key has one value.
func BuildCodexBackendURL(path string, query map[string]string) (string, error) {
	base, err := url.Parse(codexBackendAPIBaseURL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	if len(query) > 0 {
		values := base.Query()
		for key, value := range query {
			values.Set(key, value)
		}
		base.RawQuery = values.Encode()
	}
	return base.String(), nil
}

// codexInviteDetail returns trimmed display text for a JSON value. Nil becomes
// empty text, while other values use fmt.Sprint so classification can examine
// scalar or structured detail values.
func codexInviteDetail(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
