package codeassist

import (
	"context"
	"fmt"
	"time"

	"github.com/imroc/req/v3"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
)

// DefaultTestModel is the preselected model for provider tests.
const DefaultTestModel = "gemini-2.0-flash"

type Client struct{ BaseURL string }

type (
	// LoadCodeAssistRequest is a request to load the Code Assist account.
	LoadCodeAssistRequest  = wire.LoadCodeAssistRequest
	LoadCodeAssistMetadata = wire.LoadCodeAssistMetadata
	TierInfo               = wire.TierInfo
	LoadCodeAssistResponse = wire.LoadCodeAssistResponse
)

type (
	// OnboardUserRequest is a request to enroll a Code Assist user.
	OnboardUserRequest  = wire.OnboardUserRequest
	OnboardUserResponse = wire.OnboardUserResponse
)

func NewClient() *Client { return &Client{BaseURL: GeminiCliBaseURL} }

func (c *Client) LoadCodeAssist(ctx context.Context, accessToken, proxyURL string, reqBody *LoadCodeAssistRequest) (*LoadCodeAssistResponse, error) {
	if reqBody == nil {
		reqBody = defaultLoadCodeAssistRequest()
	}

	var out LoadCodeAssistResponse
	client, err := createGeminiCliReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Content-Type", "application/json").
		SetHeader("User-Agent", GeminiCLIUserAgent).
		SetBody(reqBody).
		SetSuccessResult(&out).
		Post(c.BaseURL + "/v1internal:loadCodeAssist")
	if err != nil {
		fmt.Printf("[CodeAssist] LoadCodeAssist request error: %v\n", err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if !resp.IsSuccessState() {
		body := resp.String()
		sanitizedBody := SanitizeBodyForLogs(body)
		fmt.Printf("[CodeAssist] LoadCodeAssist failed: status %d, body: %s\n", resp.StatusCode, sanitizedBody)

		if IsServiceDisabledError(body) {
			activationURL := ExtractActivationURL(body)
			if activationURL != "" {
				return nil, fmt.Errorf("gemini API not enabled for this project, please enable it by visiting: %s\n\nAfter enabling the API, wait a few minutes for the changes to propagate, then try again", activationURL)
			}
			return nil, fmt.Errorf("gemini API not enabled for this project, please enable it in the Google Cloud Console at: https://console.cloud.google.com/apis/library/cloudaicompanion.googleapis.com")
		}

		return nil, fmt.Errorf("loadCodeAssist failed: status %d, body: %s", resp.StatusCode, sanitizedBody)
	}
	fmt.Printf("[CodeAssist] LoadCodeAssist success: status %d, response: %+v\n", resp.StatusCode, out)
	return &out, nil
}

func (c *Client) OnboardUser(ctx context.Context, accessToken, proxyURL string, reqBody *OnboardUserRequest) (*OnboardUserResponse, error) {
	if reqBody == nil {
		reqBody = defaultOnboardUserRequest()
	}

	fmt.Printf("[CodeAssist] OnboardUser request body: %+v\n", reqBody)

	var out OnboardUserResponse
	client, err := createGeminiCliReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Content-Type", "application/json").
		SetHeader("User-Agent", GeminiCLIUserAgent).
		SetBody(reqBody).
		SetSuccessResult(&out).
		Post(c.BaseURL + "/v1internal:onboardUser")
	if err != nil {
		fmt.Printf("[CodeAssist] OnboardUser request error: %v\n", err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if !resp.IsSuccessState() {
		body := resp.String()
		sanitizedBody := SanitizeBodyForLogs(body)
		fmt.Printf("[CodeAssist] OnboardUser failed: status %d, body: %s\n", resp.StatusCode, sanitizedBody)

		if IsServiceDisabledError(body) {
			activationURL := ExtractActivationURL(body)
			if activationURL != "" {
				return nil, fmt.Errorf("gemini API not enabled for this project, please enable it by visiting: %s\n\nAfter enabling the API, wait a few minutes for the changes to propagate, then try again", activationURL)
			}
			return nil, fmt.Errorf("gemini API not enabled for this project, please enable it in the Google Cloud Console at: https://console.cloud.google.com/apis/library/cloudaicompanion.googleapis.com")
		}

		return nil, fmt.Errorf("onboardUser failed: status %d, body: %s", resp.StatusCode, sanitizedBody)
	}
	fmt.Printf("[CodeAssist] OnboardUser success: status %d, response: %+v\n", resp.StatusCode, out)
	return &out, nil
}

func createGeminiCliReqClient(proxyURL string) (*req.Client, error) {
	return httpclient.GetSharedReqClient(httpclient.ReqClientOptions{
		ProxyURL: proxyURL,
		Timeout:  30 * time.Second,
	})
}

func defaultLoadCodeAssistRequest() *LoadCodeAssistRequest {
	return &LoadCodeAssistRequest{
		Metadata: LoadCodeAssistMetadata{
			IDEType:    "ANTIGRAVITY",
			Platform:   "PLATFORM_UNSPECIFIED",
			PluginType: "GEMINI",
		},
	}
}

func defaultOnboardUserRequest() *OnboardUserRequest {
	return &OnboardUserRequest{
		TierID: "LEGACY",
		Metadata: LoadCodeAssistMetadata{
			IDEType:    "ANTIGRAVITY",
			Platform:   "PLATFORM_UNSPECIFIED",
			PluginType: "GEMINI",
		},
	}
}
