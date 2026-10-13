package codeassist

import (
	"context"
	"fmt"
	"time"

	"github.com/imroc/req/v3"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
)

// Code Assist discovery and onboarding use v1internal account endpoints with
// the Gemini CLI client identity. Missing request inputs select the
// Antigravity metadata defaults, while provider authorization interprets
// returned tier/project information and handles unfinished onboarding
// operations.

// DefaultTestModel supplies the preselected Gemini model used by provider
// connection tests when their caller has not selected another test model.
const DefaultTestModel = "gemini-2.0-flash"

// Client exchanges Code Assist account requests against its configured
// endpoint prefix.
type Client struct{ BaseURL string } // BaseURL is the account API endpoint prefix.

type (
	// LoadCodeAssistRequest carries project hints and client metadata for account
	// discovery.
	LoadCodeAssistRequest = wire.LoadCodeAssistRequest
	// LoadCodeAssistMetadata describes the IDE, platform, and plugin used for
	// account discovery.
	LoadCodeAssistMetadata = wire.LoadCodeAssistMetadata
	// TierInfo represents a tier identifier and metadata returned by Code Assist.
	TierInfo = wire.TierInfo
	// LoadCodeAssistResponse describes account tiers and the discovered companion
	// project.
	LoadCodeAssistResponse = wire.LoadCodeAssistResponse
)

type (
	// OnboardUserRequest carries the selected tier and client metadata for
	// enrollment.
	OnboardUserRequest = wire.OnboardUserRequest
	// OnboardUserResponse represents onboarding completion and its optional
	// companion project result.
	OnboardUserResponse = wire.OnboardUserResponse
)

// NewClient creates a Code Assist client using the Gemini CLI API endpoint.
func NewClient() *Client { return &Client{BaseURL: GeminiCliBaseURL} }

// LoadCodeAssist returns decoded tier and project observations for a bearer
// token. A nil reqBody selects default client metadata; disabled-service
// failures return activation guidance, while other failed HTTP statuses
// include bounded log-body text.
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

		// Translate a disabled-service envelope into activation guidance so account
		// authorization can show the required project setup step; other failures
		// retain bounded diagnostics.
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

// OnboardUser posts one enrollment observation using the bearer token and CLI
// identity. A nil reqBody selects LEGACY metadata; callers inspect Done and
// project output and perform any subsequent polling.
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

		// Translate a disabled-service envelope into activation guidance so account
		// authorization can show the required project setup step; other failures
		// retain bounded diagnostics.
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

// createGeminiCliReqClient selects the shared req client for proxyURL with a
// thirty-second timeout. Construction errors return to the account operation;
// the shared client retains its configured transport behavior.
func createGeminiCliReqClient(proxyURL string) (*req.Client, error) {
	return httpclient.GetSharedReqClient(httpclient.ReqClientOptions{
		ProxyURL: proxyURL,
		Timeout:  30 * time.Second,
	})
}

// defaultLoadCodeAssistRequest returns fresh Antigravity metadata for Code
// Assist discovery.
func defaultLoadCodeAssistRequest() *LoadCodeAssistRequest {
	return &LoadCodeAssistRequest{
		Metadata: LoadCodeAssistMetadata{
			IDEType:    "ANTIGRAVITY",
			Platform:   "PLATFORM_UNSPECIFIED",
			PluginType: "GEMINI",
		},
	}
}

// defaultOnboardUserRequest returns fresh LEGACY enrollment metadata for the
// Gemini plugin.
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
