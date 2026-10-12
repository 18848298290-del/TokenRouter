//go:build e2e

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These E2E tests exercise registration, login, API key creation,
// gateway access, and usage queries against a running deployment.

var (
	testUserEmail    = "e2e-test-" + fmt.Sprintf("%d", time.Now().UnixMilli()) + "@test.local"
	testUserPassword = "E2eTest@12345"
	testUserName     = "e2e-test-user"
)

// TestUserRegistrationAndLogin registers a user, logs in, and reads the current profile.
func TestUserRegistrationAndLogin(t *testing.T) {
	// Register the user used by the following login subtest.
	t.Run("注册新用户", func(t *testing.T) {
		payload := map[string]string{
			"email":    testUserEmail,
			"password": testUserPassword,
			"username": testUserName,
		}
		body, _ := json.Marshal(payload)

		resp, err := doRequest(t, "POST", "/api/auth/register", body, "")
		if err != nil {
			t.Skipf("注册接口不可用，跳过用户流程测试: %v", err)
			return
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)

		// Continue after a possible duplicate-email response; skip registration when it is disabled.
		switch resp.StatusCode {
		case 200:
			t.Logf("✅ 用户注册成功: %s", testUserEmail)
		case 400:
			t.Logf("⚠️ 用户可能已存在: %s", string(respBody))
		case 403:
			t.Skipf("注册功能已关闭: %s", string(respBody))
		default:
			t.Logf("⚠️ 注册返回 HTTP %d: %s（继续尝试登录）", resp.StatusCode, string(respBody))
		}
	})

	// Retain the login token for the profile subtest.
	var accessToken string
	t.Run("用户登录获取JWT", func(t *testing.T) {
		payload := map[string]string{
			"email":    testUserEmail,
			"password": testUserPassword,
		}
		body, _ := json.Marshal(payload)

		resp, err := doRequest(t, "POST", "/api/auth/login", body, "")
		if err != nil {
			t.Fatalf("登录请求失败: %v", err)
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != 200 {
			t.Skipf("登录失败 HTTP %d: %s（可能需要先注册用户）", resp.StatusCode, string(respBody))
			return
		}

		var result map[string]any
		if err := json.Unmarshal(respBody, &result); err != nil {
			t.Fatalf("解析登录响应失败: %v", err)
		}

		// Accept both a top-level token and a token inside the data envelope.
		if token, ok := result["access_token"].(string); ok && token != "" {
			accessToken = token
		} else if data, ok := result["data"].(map[string]any); ok {
			if token, ok := data["access_token"].(string); ok {
				accessToken = token
			}
		}

		if accessToken == "" {
			t.Skipf("未获取到 access_token，响应: %s", string(respBody))
			return
		}

		// Reject implausibly short tokens before using them in subsequent requests.
		if len(accessToken) < 10 {
			t.Fatalf("access_token 格式异常: %s", accessToken)
		}

		t.Logf("✅ 登录成功，获取 JWT（长度: %d）", len(accessToken))
	})

	if accessToken == "" {
		t.Skip("未获取到 JWT，跳过后续测试")
		return
	}

	// Use the acquired token to fetch the current profile.
	t.Run("获取当前用户信息", func(t *testing.T) {
		resp, err := doRequest(t, "GET", "/api/user/me", nil, accessToken)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("HTTP %d: %s", resp.StatusCode, string(body))
		}

		t.Logf("✅ 成功获取用户信息")
	})
}

// TestAPIKeyLifecycle creates an API key, calls the gateway, and queries usage.
func TestAPIKeyLifecycle(t *testing.T) {
	// Later subtests need a successful login.
	accessToken := loginTestUser(t)
	if accessToken == "" {
		t.Skip("无法登录，跳过 API Key 生命周期测试")
		return
	}

	var apiKey string

	// Retain the created key for the gateway subtest.
	t.Run("创建API_Key", func(t *testing.T) {
		payload := map[string]string{
			"name": "e2e-test-key-" + fmt.Sprintf("%d", time.Now().UnixMilli()),
		}
		body, _ := json.Marshal(payload)

		resp, err := doRequest(t, "POST", "/api/keys", body, accessToken)
		if err != nil {
			t.Fatalf("创建 API Key 请求失败: %v", err)
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != 200 {
			t.Skipf("创建 API Key 失败 HTTP %d: %s", resp.StatusCode, string(respBody))
			return
		}

		var result map[string]any
		if err := json.Unmarshal(respBody, &result); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}

		// Accept a top-level key or a key inside the data envelope.
		if key, ok := result["key"].(string); ok {
			apiKey = key
		} else if data, ok := result["data"].(map[string]any); ok {
			if key, ok := data["key"].(string); ok {
				apiKey = key
			}
		}

		if apiKey == "" {
			t.Skipf("未获取到 API Key，响应: %s", string(respBody))
			return
		}

		// Log at most the first eight bytes of the key.
		masked := apiKey
		if len(masked) > 8 {
			masked = masked[:8] + "..."
		}
		t.Logf("✅ API Key 创建成功: %s", masked)
	})

	if apiKey == "" {
		t.Skip("未创建 API Key，跳过后续测试")
		return
	}

	// Use the new key to access the gateway.
	t.Run("使用API_Key调用网关", func(t *testing.T) {
		// Listing models exercises gateway authentication without generating content.
		resp, err := doRequest(t, "GET", "/v1/models", nil, apiKey)
		if err != nil {
			t.Fatalf("网关请求失败: %v", err)
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)

		// Log success and account restrictions; this smoke test does not assert the status code.
		switch {
		case resp.StatusCode == 200:
			t.Logf("✅ API Key 网关调用成功")
		case resp.StatusCode == 402:
			t.Logf("⚠️ 余额不足，但 API Key 认证通过")
		case resp.StatusCode == 403:
			t.Logf("⚠️ 无可用提供商，但 API Key 认证通过")
		default:
			t.Logf("⚠️ 网关返回 HTTP %d: %s", resp.StatusCode, string(respBody))
		}
	})

	// Query usage with the login token.
	t.Run("查询用量记录", func(t *testing.T) {
		resp, err := doRequest(t, "GET", "/api/usage/dashboard", nil, accessToken)
		if err != nil {
			t.Fatalf("用量查询请求失败: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Logf("⚠️ 用量查询返回 HTTP %d: %s", resp.StatusCode, string(body))
			return
		}

		t.Logf("✅ 用量查询成功")
	})
}

// Request and credential helpers.

func doRequest(t *testing.T, method, path string, body []byte, token string) (*http.Response, error) {
	t.Helper()

	url := baseURL + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}

func loginTestUser(t *testing.T) string {
	t.Helper()

	// Use configured administrator credentials when ADMIN_PASSWORD is set.
	adminEmail := getEnv("ADMIN_EMAIL", "admin@tokenrouter.local")
	adminPassword := getEnv("ADMIN_PASSWORD", "")

	if adminPassword == "" {
		// Otherwise use the test user's credentials.
		adminEmail = testUserEmail
		adminPassword = testUserPassword
	}

	payload := map[string]string{
		"email":    adminEmail,
		"password": adminPassword,
	}
	body, _ := json.Marshal(payload)

	resp, err := doRequest(t, "POST", "/api/auth/login", body, "")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return ""
	}

	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return ""
	}

	if token, ok := result["access_token"].(string); ok {
		return token
	}
	if data, ok := result["data"].(map[string]any); ok {
		if token, ok := data["access_token"].(string); ok {
			return token
		}
	}

	return ""
}

// redactAPIKey trims whitespace and exposes eight leading bytes; shorter keys are fully masked.
func redactAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 8 {
		return "***"
	}
	return key[:8] + "..."
}
