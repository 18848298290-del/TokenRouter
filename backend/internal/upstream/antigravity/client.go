package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	proxyurl "github.com/TokenFlux/TokenRouter/internal/infra/httpclient/proxy"
	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	googlewire "github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

const (
	// proxyDialTimeout bounds the TCP connection to the proxy.
	proxyDialTimeout = 5 * time.Second
	// proxyTLSHandshakeTimeout bounds the TLS handshake through the proxy.
	proxyTLSHandshakeTimeout = 5 * time.Second
	// clientTimeout covers connection setup through reading the response body.
	clientTimeout = 10 * time.Second

	// privacyBaseURL sends privacy API calls to the daily endpoint.
	privacyBaseURL = antigravityDailyBaseURL
)

// ForbiddenError reports a 403 response from fetchAvailableModels.
type ForbiddenError struct {
	StatusCode int
	Body       string
}

// Client calls the Antigravity API.
type Client struct {
	httpClient *http.Client
}

type AvailableCredit = googlewire.AntigravityAvailableCredit

type DeprecatedModelInfo = googlewire.AntigravityDeprecatedModelInfo

type FetchAvailableModelsRequest = googlewire.AntigravityFetchAvailableModelsRequest

type FetchAvailableModelsResponse = googlewire.AntigravityFetchAvailableModelsResponse

type FetchUserInfoRequest = googlewire.AntigravityFetchUserInfoRequest

type FetchUserInfoResponse = googlewire.AntigravityFetchUserInfoResponse

type LoadCodeAssistRequest = googlewire.AntigravityLoadCodeAssistRequest

type LoadCodeAssistResponse = googlewire.AntigravityLoadCodeAssistResponse

type ModelInfo = googlewire.AntigravityModelInfo

type ModelQuotaInfo = googlewire.AntigravityModelQuotaInfo

type OnboardUserRequest = googlewire.AntigravityOnboardUserRequest

type OnboardUserResponse = googlewire.AntigravityOnboardUserResponse

type PaidTierInfo = googlewire.AntigravityPaidTierInfo

type SetUserSettingsRequest = googlewire.AntigravitySetUserSettingsRequest

type SetUserSettingsResponse = googlewire.AntigravitySetUserSettingsResponse

type TierInfo = googlewire.AntigravityTierInfo

type TokenResponse = googlewire.AntigravityTokenResponse

type UserInfo = googlewire.AntigravityUserInfo

// ClientOptions allows callers to supply an HTTP client.
type ClientOptions struct{ HTTPClient *http.Client }

func (e *ForbiddenError) Error() string {
	return fmt.Sprintf("fetchAvailableModels failed (HTTP %d): %s", e.StatusCode, e.Body)
}

// NewAPIRequestWithURL builds a v1internal request using the supplied base URL.
func NewAPIRequestWithURL(ctx context.Context, baseURL, action, accessToken string, body []byte) (*http.Request, error) {
	// Streaming uses SSE framing.
	apiURL := fmt.Sprintf("%s/v1internal:%s", baseURL, action)
	isStream := action == "streamGenerateContent"
	if isStream {
		apiURL += "?alt=sse"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// These headers identify and authenticate the Antigravity client.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", GetUserAgentForContext(ctx))

	return req, nil
}

// NewAPIRequest builds a v1internal request using BaseURL.
func NewAPIRequest(ctx context.Context, action, accessToken string, body []byte) (*http.Request, error) {
	return NewAPIRequestWithURL(ctx, BaseURL, action, accessToken, body)
}

func NewClient(proxyURL string) (*Client, error) {
	client := &http.Client{
		Timeout: clientTimeout,
	}

	_, parsed, err := proxyurl.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	if parsed != nil {
		transport := &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: proxyDialTimeout,
			}).DialContext,
			TLSHandshakeTimeout: proxyTLSHandshakeTimeout,
		}
		if err := proxyurl.ConfigureTransportProxy(transport, parsed); err != nil {
			return nil, fmt.Errorf("configure proxy: %w", err)
		}
		client.Transport = transport
	}
	return &Client{
		httpClient: client,
	}, nil
}

// IsConnectionError reports network timeouts, network operation errors, and URL errors.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}

	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// shouldFallbackToNextURL reports whether another endpoint should be tried.
// Connection errors, 429, 408, 404, and statuses at least 500 permit another attempt.
func shouldFallbackToNextURL(err error, statusCode int) bool {
	if IsConnectionError(err) {
		return true
	}
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusNotFound ||
		statusCode >= 500
}

// NewClientWithOptions uses the supplied HTTP client when present.
func NewClientWithOptions(proxyURL string, options ClientOptions) (*Client, error) {
	client, err := NewClient(proxyURL)
	if err != nil {
		return nil, err
	}
	if options.HTTPClient != nil {
		client.httpClient = options.HTTPClient
	}
	return client, nil
}

// ExchangeCode exchanges an authorization code and PKCE verifier for tokens.
func (c *Client) ExchangeCode(ctx context.Context, code, codeVerifier string) (*TokenResponse, error) {
	clientSecret, err := getClientSecret()
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("client_id", ClientID)
	params.Set("client_secret", clientSecret)
	params.Set("code", code)
	params.Set("redirect_uri", RedirectURI)
	params.Set("grant_type", "authorization_code")
	params.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := servertiming.Do(c.httpClient, req)
	if err != nil {
		return nil, fmt.Errorf("send token exchange request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	return &tokenResp, nil
}

// RefreshToken exchanges a refresh token for a new access token.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	clientSecret, err := getClientSecret()
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("client_id", ClientID)
	params.Set("client_secret", clientSecret)
	params.Set("refresh_token", refreshToken)
	params.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := servertiming.Do(c.httpClient, req)
	if err != nil {
		return nil, fmt.Errorf("send token refresh request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	return &tokenResp, nil
}

// GetUserInfo fetches the authenticated Google user.
func (c *Client) GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := servertiming.Do(c.httpClient, req)
	if err != nil {
		return nil, fmt.Errorf("send user info request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch user info failed (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var userInfo UserInfo
	if err := json.Unmarshal(bodyBytes, &userInfo); err != nil {
		return nil, fmt.Errorf("parse user info: %w", err)
	}

	return &userInfo, nil
}

// LoadCodeAssist returns decoded account details and the raw JSON object.
// Endpoints are tried in BaseURLs order.
func (c *Client) LoadCodeAssist(ctx context.Context, accessToken string) (*LoadCodeAssistResponse, map[string]any, error) {
	reqBody := LoadCodeAssistRequest{}
	reqBody.Metadata.IDEType = "ANTIGRAVITY"
	reqBody.Metadata.IDEVersion = GetUserAgentVersionForContext(ctx)
	reqBody.Metadata.IDEName = "antigravity"

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("encode request: %w", err)
	}

	// Use the configured endpoint order.
	availableURLs := BaseURLs

	var lastErr error
	for urlIdx, baseURL := range availableURLs {
		apiURL := baseURL + "/v1internal:loadCodeAssist"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(string(bodyBytes)))
		if err != nil {
			lastErr = fmt.Errorf("create request: %w", err)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", GetUserAgentForContext(ctx))

		resp, err := servertiming.Do(c.httpClient, req)
		if err != nil {
			lastErr = fmt.Errorf("loadCodeAssist request failed: %w", err)
			if shouldFallbackToNextURL(err, 0) && urlIdx < len(availableURLs)-1 {
				log.Printf("[antigravity] loadCodeAssist URL fallback: %s -> %s", baseURL, availableURLs[urlIdx+1])
				continue
			}
			return nil, nil, lastErr
		}

		respBodyBytes, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close() // Close before the next attempt so response bodies do not accumulate.
		if err != nil {
			return nil, nil, fmt.Errorf("read response: %w", err)
		}

		if shouldFallbackToNextURL(nil, resp.StatusCode) && urlIdx < len(availableURLs)-1 {
			log.Printf("[antigravity] loadCodeAssist URL fallback (HTTP %d): %s -> %s", resp.StatusCode, baseURL, availableURLs[urlIdx+1])
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("loadCodeAssist failed (HTTP %d): %s", resp.StatusCode, string(respBodyBytes))
		}

		var loadResp LoadCodeAssistResponse
		if err := json.Unmarshal(respBodyBytes, &loadResp); err != nil {
			return nil, nil, fmt.Errorf("parse response: %w", err)
		}

		var rawResp map[string]any
		_ = json.Unmarshal(respBodyBytes, &rawResp)

		// Record availability for callers that prioritize successful endpoints.
		DefaultURLAvailability.MarkSuccess(baseURL)
		return &loadResp, rawResp, nil
	}

	return nil, nil, lastErr
}

// OnboardUser initializes the account and returns its project ID.
//
// loadCodeAssist may omit cloudaicompanionProject until onboarding completes.
// Poll the onboarding operation until the project is available or attempts run out.
func (c *Client) OnboardUser(ctx context.Context, accessToken, tierID string) (string, error) {
	tierID = strings.TrimSpace(tierID)
	if tierID == "" {
		return "", fmt.Errorf("tier_id is required")
	}

	reqBody := OnboardUserRequest{TierID: tierID}
	reqBody.Metadata.IDEType = "ANTIGRAVITY"
	reqBody.Metadata.Platform = "PLATFORM_UNSPECIFIED"
	reqBody.Metadata.PluginType = "GEMINI"

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	availableURLs := BaseURLs
	var lastErr error

	for urlIdx, baseURL := range availableURLs {
		apiURL := baseURL + "/v1internal:onboardUser"

		for attempt := 1; attempt <= 5; attempt++ {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
			if err != nil {
				lastErr = fmt.Errorf("create request: %w", err)
				break
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", GetUserAgentForContext(ctx))

			resp, err := servertiming.Do(c.httpClient, req)
			if err != nil {
				lastErr = fmt.Errorf("onboardUser request failed: %w", err)
				if shouldFallbackToNextURL(err, 0) && urlIdx < len(availableURLs)-1 {
					log.Printf("[antigravity] onboardUser URL fallback: %s -> %s", baseURL, availableURLs[urlIdx+1])
					break
				}
				return "", lastErr
			}

			respBodyBytes, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return "", fmt.Errorf("read response: %w", err)
			}

			if shouldFallbackToNextURL(nil, resp.StatusCode) && urlIdx < len(availableURLs)-1 {
				log.Printf("[antigravity] onboardUser URL fallback (HTTP %d): %s -> %s", resp.StatusCode, baseURL, availableURLs[urlIdx+1])
				break
			}

			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("onboardUser failed (HTTP %d): %s", resp.StatusCode, string(respBodyBytes))
				return "", lastErr
			}

			var onboardResp OnboardUserResponse
			if err := json.Unmarshal(respBodyBytes, &onboardResp); err != nil {
				lastErr = fmt.Errorf("onboardUser response parsing failed: %w", err)
				return "", lastErr
			}

			if onboardResp.Done {
				if projectID := extractProjectIDFromOnboardResponse(onboardResp.Response); projectID != "" {
					DefaultURLAvailability.MarkSuccess(baseURL)
					return projectID, nil
				}
				lastErr = fmt.Errorf("onboardUser completed without project_id")
				return "", lastErr
			}

			// The operation is still pending; wait before polling again.
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("onboardUser did not return project_id")
}

func extractProjectIDFromOnboardResponse(resp map[string]any) string {
	if len(resp) == 0 {
		return ""
	}

	if v, ok := resp["cloudaicompanionProject"]; ok {
		switch project := v.(type) {
		case string:
			return strings.TrimSpace(project)
		case map[string]any:
			if id, ok := project["id"].(string); ok {
				return strings.TrimSpace(id)
			}
		}
	}

	return ""
}

// FetchAvailableModels returns decoded models and quotas plus the raw JSON object.
// Endpoints are tried in BaseURLs order; bodyLimit must be positive.
func (c *Client) FetchAvailableModels(ctx context.Context, accessToken, projectID string, bodyLimit int64) (*FetchAvailableModelsResponse, map[string]any, error) {
	if c == nil || c.httpClient == nil {
		return nil, nil, errors.New("antigravity client is not configured")
	}
	if bodyLimit <= 0 {
		return nil, nil, errors.New("fetchAvailableModels body limit must be positive")
	}

	reqBody := FetchAvailableModelsRequest{Project: projectID}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("encode request: %w", err)
	}

	// Use the configured endpoint order.
	availableURLs := BaseURLs

	fetchClient := c.fetchAvailableModelsHTTPClient()
	var lastErr error
	for urlIdx, baseURL := range availableURLs {
		apiURL := baseURL + "/v1internal:fetchAvailableModels"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(string(bodyBytes)))
		if err != nil {
			lastErr = fmt.Errorf("create request: %w", err)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", GetUserAgentForContext(ctx))

		resp, err := servertiming.Do(fetchClient, req)
		if err != nil {
			lastErr = fmt.Errorf("fetchAvailableModels request failed: %w", err)
			if shouldFallbackToNextURL(err, 0) && urlIdx < len(availableURLs)-1 {
				log.Printf("[antigravity] fetchAvailableModels URL fallback: %s -> %s", baseURL, availableURLs[urlIdx+1])
				continue
			}
			return nil, nil, lastErr
		}

		respBodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
		_ = resp.Body.Close() // Close before the next attempt so response bodies do not accumulate.
		if err != nil {
			return nil, nil, fmt.Errorf("read response: %w", err)
		}
		if int64(len(respBodyBytes)) > bodyLimit {
			return nil, nil, fmt.Errorf("response exceeds %d bytes", bodyLimit)
		}

		if shouldFallbackToNextURL(nil, resp.StatusCode) && urlIdx < len(availableURLs)-1 {
			log.Printf("[antigravity] fetchAvailableModels URL fallback (HTTP %d): %s -> %s", resp.StatusCode, baseURL, availableURLs[urlIdx+1])
			continue
		}

		if resp.StatusCode == http.StatusForbidden {
			return nil, nil, &ForbiddenError{
				StatusCode: resp.StatusCode,
				Body:       string(respBodyBytes),
			}
		}

		if resp.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("fetchAvailableModels failed (HTTP %d): %s", resp.StatusCode, string(respBodyBytes))
		}

		var modelsResp FetchAvailableModelsResponse
		if err := json.Unmarshal(respBodyBytes, &modelsResp); err != nil {
			return nil, nil, fmt.Errorf("parse response: %w", err)
		}

		var rawResp map[string]any
		_ = json.Unmarshal(respBodyBytes, &rawResp)

		// Record availability for callers that prioritize successful endpoints.
		DefaultURLAvailability.MarkSuccess(baseURL)
		return &modelsResp, rawResp, nil
	}

	return nil, nil, lastErr
}

func (c *Client) fetchAvailableModelsHTTPClient() *http.Client {
	fetchClient := *c.httpClient
	fetchClient.CheckRedirect = checkFetchAvailableModelsRedirect
	return &fetchClient
}

func checkFetchAvailableModelsRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req == nil || req.URL == nil {
		return errors.New("redirect url is nil")
	}
	if !isAllowedFetchAvailableModelsRedirectHost(req.URL.Hostname()) {
		return fmt.Errorf("redirect to unsupported host: %s", req.URL.Hostname())
	}
	return nil
}

func isAllowedFetchAvailableModelsRedirectHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, baseURL := range BaseURLs {
		parsed, err := url.Parse(baseURL)
		if err != nil {
			continue
		}
		if strings.EqualFold(host, parsed.Hostname()) {
			return true
		}
	}
	return false
}

// SetUserSettings clears user settings and returns the decoded response.
func (c *Client) SetUserSettings(ctx context.Context, accessToken string) (*SetUserSettingsResponse, error) {
	// An empty user_settings object clears the privacy settings.
	payload := SetUserSettingsRequest{UserSettings: map[string]any{}}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	apiURL := privacyBaseURL + "/v1internal:setUserSettings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", GetUserAgentForContext(ctx))
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")
	req.Host = "daily-cloudcode-pa.googleapis.com"

	resp, err := servertiming.Do(c.httpClient, req)
	if err != nil {
		return nil, fmt.Errorf("setUserSettings request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("setUserSettings failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result SetUserSettingsResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &result, nil
}

// FetchUserInfo fetches the account privacy settings.
func (c *Client) FetchUserInfo(ctx context.Context, accessToken, projectID string) (*FetchUserInfoResponse, error) {
	reqBody := FetchUserInfoRequest{Project: projectID}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	apiURL := privacyBaseURL + "/v1internal:fetchUserInfo"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", GetUserAgentForContext(ctx))
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")
	req.Host = "daily-cloudcode-pa.googleapis.com"

	resp, err := servertiming.Do(c.httpClient, req)
	if err != nil {
		return nil, fmt.Errorf("fetchUserInfo request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetchUserInfo failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result FetchUserInfoResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &result, nil
}
