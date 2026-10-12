//go:build e2e

package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	// claudeAPIKeyEnv names the environment variable containing the Claude E2E key.
	claudeAPIKeyEnv = "CLAUDE_API_KEY"

	// geminiAPIKeyEnv names the environment variable containing the Gemini E2E key.
	geminiAPIKeyEnv = "GEMINI_API_KEY"
)

var (
	baseURL = getEnv("BASE_URL", "http://localhost:8080")
	// endpointPrefix selects the endpoint family on the test deployment.
	// An empty ENDPOINT_PREFIX uses /v1/messages and /v1beta/models.
	// A value of /antigravity targets the corresponding legacy prefixed endpoints.
	endpointPrefix = getEnv("ENDPOINT_PREFIX", "")
	testInterval   = 1 * time.Second // Delay between requests to reduce rate-limit pressure.

	// claudeModels contains model names expected by the E2E deployment.
	claudeModels = []string{
		// Opus fixtures.
		"claude-opus-4-5-thinking", // Expected direct model.
		"claude-opus-4",            // Fixture expects a mapping to claude-opus-4-5-thinking.
		"claude-opus-4-5-20251101", // Fixture expects a mapping to claude-opus-4-5-thinking.
		// Sonnet fixtures.
		"claude-sonnet-4-5",          // Expected direct model.
		"claude-sonnet-4-5-thinking", // Expected direct model.
		"claude-sonnet-4-5-20250929", // Fixture expects a mapping to claude-sonnet-4-5-thinking.
		"claude-3-5-sonnet-20241022", // Fixture expects a mapping to claude-sonnet-4-5.
		// Haiku fixtures expect a mapping to gemini-3-flash.
		"claude-haiku-4",
		"claude-haiku-4-5",
		"claude-haiku-4-5-20251001",
		"claude-3-haiku-20240307",
	}

	// geminiModels contains model names expected by the E2E deployment.
	geminiModels = []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-3-flash",
		"gemini-3-pro-low",
		"gemini-3-pro-high",
	}
)

func requireClaudeAPIKey(t *testing.T) string {
	t.Helper()
	key := strings.TrimSpace(os.Getenv(claudeAPIKeyEnv))
	if key == "" {
		t.Skipf("未设置 %s，跳过 Claude 相关 E2E 测试", claudeAPIKeyEnv)
	}
	return key
}

func requireGeminiAPIKey(t *testing.T) string {
	t.Helper()
	key := strings.TrimSpace(os.Getenv(geminiAPIKeyEnv))
	if key == "" {
		t.Skipf("未设置 %s，跳过 Gemini 相关 E2E 测试", geminiAPIKeyEnv)
	}
	return key
}

// TestClaudeModelsList checks GET /v1/models under the configured endpoint prefix.
func TestClaudeModelsList(t *testing.T) {
	claudeKey := requireClaudeAPIKey(t)
	url := baseURL + endpointPrefix + "/v1/models"

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+claudeKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if result["object"] != "list" {
		t.Errorf("期望 object=list, 得到 %v", result["object"])
	}

	data, ok := result["data"].([]any)
	if !ok {
		t.Fatal("响应缺少 data 数组")
	}
	t.Logf("✅ 返回 %d 个模型", len(data))
}

// TestGeminiModelsList checks GET /v1beta/models under the configured endpoint prefix.
func TestGeminiModelsList(t *testing.T) {
	geminiKey := requireGeminiAPIKey(t)
	url := baseURL + endpointPrefix + "/v1beta/models"

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+geminiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	models, ok := result["models"].([]any)
	if !ok {
		t.Fatal("响应缺少 models 数组")
	}
	t.Logf("✅ 返回 %d 个模型", len(models))
}

// TestClaudeMessages checks streaming and non-streaming Messages requests.
func TestClaudeMessages(t *testing.T) {
	claudeKey := requireClaudeAPIKey(t)
	for i, model := range claudeModels {
		if i > 0 {
			time.Sleep(testInterval)
		}
		t.Run(model+"_非流式", func(t *testing.T) {
			testClaudeMessage(t, claudeKey, model, false)
		})
		time.Sleep(testInterval)
		t.Run(model+"_流式", func(t *testing.T) {
			testClaudeMessage(t, claudeKey, model, true)
		})
	}
}

func testClaudeMessage(t *testing.T, claudeKey string, model string, stream bool) {
	url := baseURL + endpointPrefix + "/v1/messages"

	payload := map[string]any{
		"model":      model,
		"max_tokens": 50,
		"stream":     stream,
		"messages": []map[string]string{
			{"role": "user", "content": "Say 'hello' in one word."},
		},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+claudeKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if stream {
		// Stop after three SSE data lines; at least one is required.
		scanner := bufio.NewScanner(resp.Body)
		eventCount := 0
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				eventCount++
				if eventCount >= 3 {
					break
				}
			}
		}
		if eventCount == 0 {
			t.Fatal("未收到任何 SSE 事件")
		}
		t.Logf("✅ 收到 %d+ 个 SSE 事件", eventCount)
	} else {
		// A non-streaming response must decode as a message.
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if result["type"] != "message" {
			t.Errorf("期望 type=message, 得到 %v", result["type"])
		}
		t.Logf("✅ 收到消息响应 id=%v", result["id"])
	}
}

// TestGeminiGenerateContent checks streaming and non-streaming Gemini generation requests.
func TestGeminiGenerateContent(t *testing.T) {
	geminiKey := requireGeminiAPIKey(t)
	for i, model := range geminiModels {
		if i > 0 {
			time.Sleep(testInterval)
		}
		t.Run(model+"_非流式", func(t *testing.T) {
			testGeminiGenerate(t, geminiKey, model, false)
		})
		time.Sleep(testInterval)
		t.Run(model+"_流式", func(t *testing.T) {
			testGeminiGenerate(t, geminiKey, model, true)
		})
	}
}

func testGeminiGenerate(t *testing.T, geminiKey string, model string, stream bool) {
	action := "generateContent"
	if stream {
		action = "streamGenerateContent"
	}
	url := fmt.Sprintf("%s%s/v1beta/models/%s:%s", baseURL, endpointPrefix, model, action)
	if stream {
		url += "?alt=sse"
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role": "user",
				"parts": []map[string]string{
					{"text": "Say 'hello' in one word."},
				},
			},
		},
		"generationConfig": map[string]int{
			"maxOutputTokens": 50,
		},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+geminiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if stream {
		// Stop after three SSE data lines; at least one is required.
		scanner := bufio.NewScanner(resp.Body)
		eventCount := 0
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				eventCount++
				if eventCount >= 3 {
					break
				}
			}
		}
		if eventCount == 0 {
			t.Fatal("未收到任何 SSE 事件")
		}
		t.Logf("✅ 收到 %d+ 个 SSE 事件", eventCount)
	} else {
		// A non-streaming response must contain candidates.
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if _, ok := result["candidates"]; !ok {
			t.Error("响应缺少 candidates 字段")
		}
		t.Log("✅ 收到 candidates 响应")
	}
}

// TestClaudeMessagesWithComplexTools sends Claude Code-style tool schemas
// with fields that exercise upstream schema conversion.
func TestClaudeMessagesWithComplexTools(t *testing.T) {
	claudeKey := requireClaudeAPIKey(t)
	// Exercise representative Claude and Gemini-mapped fixtures.
	models := []string{
		"claude-opus-4-5-20251101",  // Claude fixture.
		"claude-haiku-4-5-20251001", // Fixture expects Gemini routing.
	}

	for i, model := range models {
		if i > 0 {
			time.Sleep(testInterval)
		}
		t.Run(model+"_复杂工具", func(t *testing.T) {
			testClaudeMessageWithTools(t, claudeKey, model)
		})
	}
}

func testClaudeMessageWithTools(t *testing.T, claudeKey string, model string) {
	url := baseURL + endpointPrefix + "/v1/messages"

	// Claude Code-style tool definitions include schema keywords
	// that may need normalization for the selected upstream.
	tools := []map[string]any{
		{
			"name":        "read_file",
			"description": "Read file contents",
			"input_schema": map[string]any{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"type":    "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path",
						"minLength":   1,
						"maxLength":   4096,
						"pattern":     "^[^\\x00]+$",
					},
					"encoding": map[string]any{
						"type":    []string{"string", "null"},
						"default": "utf-8",
						"enum":    []string{"utf-8", "ascii", "latin-1"},
					},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "write_file",
			"description": "Write content to file",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":      "string",
						"minLength": 1,
					},
					"content": map[string]any{
						"type":      "string",
						"maxLength": 1048576,
					},
				},
				"required":             []string{"path", "content"},
				"additionalProperties": false,
				"strict":               true,
			},
		},
		{
			"name":        "list_files",
			"description": "List files in directory",
			"input_schema": map[string]any{
				"$id":  "https://example.com/list-files.schema.json",
				"type": "object",
				"properties": map[string]any{
					"directory": map[string]any{
						"type": "string",
					},
					"patterns": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type":      "string",
							"minLength": 1,
						},
						"minItems":    1,
						"maxItems":    100,
						"uniqueItems": true,
					},
					"recursive": map[string]any{
						"type":    "boolean",
						"default": false,
					},
				},
				"required":             []string{"directory"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "search_code",
			"description": "Search code in files",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":      "string",
						"minLength": 1,
						"format":    "regex",
					},
					"max_results": map[string]any{
						"type":             "integer",
						"minimum":          1,
						"maximum":          1000,
						"exclusiveMinimum": 0,
						"default":          100,
					},
				},
				"required":             []string{"query"},
				"additionalProperties": false,
				"examples": []map[string]any{
					{"query": "function.*test", "max_results": 50},
				},
			},
		},
		// Exercise a required name absent from properties.
		{
			"name":        "invalid_required_tool",
			"description": "Tool with invalid required field",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string",
					},
				},
				// The converter must remove this nonexistent required field for the intended upstream.
				"required": []string{"name", "nonexistent_field"},
			},
		},
		// Exercise an object schema with no properties.
		{
			"name":        "no_properties_tool",
			"description": "Tool without properties",
			"input_schema": map[string]any{
				"type":     "object",
				"required": []string{"should_be_removed"},
			},
		},
		// Exercise a schema whose object type must be inferred from properties.
		{
			"name":        "no_type_tool",
			"description": "Tool without type",
			"input_schema": map[string]any{
				"properties": map[string]any{
					"value": map[string]any{
						"type": "string",
					},
				},
			},
		},
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 100,
		"stream":     false,
		"messages": []map[string]string{
			{"role": "user", "content": "List files in the current directory"},
		},
		"tools": tools,
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+claudeKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Treat a bad request as a failure of this schema-conversion scenario.
	if resp.StatusCode == 400 {
		t.Fatalf("Schema 清理失败，收到 400 错误: %s", string(respBody))
	}

	// Skip when the deployment reports temporary unavailability.
	if resp.StatusCode == 503 {
		t.Skipf("提供商暂时不可用 (503): %s", string(respBody))
	}

	// Skip when the deployment rate-limits the request.
	if resp.StatusCode == 429 {
		t.Skipf("请求被限流 (429): %s", string(respBody))
	}

	if resp.StatusCode != 200 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if result["type"] != "message" {
		t.Errorf("期望 type=message, 得到 %v", result["type"])
	}
	t.Logf("✅ 复杂工具 schema 测试通过, id=%v", result["id"])
}

// TestClaudeMessagesWithThinkingAndTools checks a thinking request with unsigned tool history.
// The fixture expects Gemini routing and exercises signature repair for an assistant tool_use block.
// The assertion checks acceptance of the converted request.
func TestClaudeMessagesWithThinkingAndTools(t *testing.T) {
	claudeKey := requireClaudeAPIKey(t)
	models := []string{
		"claude-haiku-4-5-20251001", // Fixture expects gemini-3-flash routing.
	}
	for i, model := range models {
		if i > 0 {
			time.Sleep(testInterval)
		}
		t.Run(model+"_thinking模式工具调用", func(t *testing.T) {
			testClaudeThinkingWithToolHistory(t, claudeKey, model)
		})
	}
}

func testClaudeThinkingWithToolHistory(t *testing.T, claudeKey string, model string) {
	url := baseURL + endpointPrefix + "/v1/messages"

	// Continue a conversation after an assistant tool call and its result.
	// Omit the tool signature to exercise signature repair.
	payload := map[string]any{
		"model":      model,
		"max_tokens": 200,
		"stream":     false,
		// Enable thinking for the continuation.
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": 1024,
		},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "List files in the current directory",
			},
			// The assistant history contains an unsigned tool_use block.
			map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{
						"type": "text",
						"text": "I'll list the files for you.",
					},
					{
						"type":  "tool_use",
						"id":    "toolu_01XGmNv",
						"name":  "Bash",
						"input": map[string]any{"command": "ls -la"},
						// Signature intentionally omitted.
					},
				},
			},
			// Supply the result of the preceding tool call.
			map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_01XGmNv",
						"content":     "file1.txt\nfile2.txt\ndir1/",
					},
				},
			},
		},
		"tools": []map[string]any{
			{
				"name":        "Bash",
				"description": "Execute bash commands",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type": "string",
						},
					},
					"required": []string{"command"},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+claudeKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Treat a bad request as a failure of this unsigned-tool-history scenario.
	if resp.StatusCode == 400 {
		t.Fatalf("thought_signature 处理失败，收到 400 错误: %s", string(respBody))
	}

	// Skip when the deployment reports temporary unavailability.
	if resp.StatusCode == 503 {
		t.Skipf("提供商暂时不可用 (503): %s", string(respBody))
	}

	// Skip when the deployment rate-limits the request.
	if resp.StatusCode == 429 {
		t.Skipf("请求被限流 (429): %s", string(respBody))
	}

	if resp.StatusCode != 200 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if result["type"] != "message" {
		t.Errorf("期望 type=message, 得到 %v", result["type"])
	}
	t.Logf("✅ thinking 模式工具调用测试通过, id=%v", result["id"])
}

// TestClaudeMessagesWithGeminiModel sends Gemini model names through /v1/messages.
// The fixture includes names expected to resolve through prefix mappings.
// It runs when ENDPOINT_PREFIX is /antigravity.
func TestClaudeMessagesWithGeminiModel(t *testing.T) {
	if endpointPrefix != "/antigravity" {
		t.Skip("仅在 Antigravity 模式下运行")
	}
	claudeKey := requireClaudeAPIKey(t)

	// Gemini names sent through the Messages endpoint.
	geminiViaClaude := []string{
		"gemini-3-flash",       // Expected direct model.
		"gemini-3-pro-low",     // Expected direct model.
		"gemini-3-pro-high",    // Expected direct model.
		"gemini-3-pro",         // Fixture expects a prefix mapping to gemini-3-pro-high.
		"gemini-3-pro-preview", // Fixture expects a prefix mapping to gemini-3-pro-high.
	}

	for i, model := range geminiViaClaude {
		if i > 0 {
			time.Sleep(testInterval)
		}
		t.Run(model+"_通过Claude端点", func(t *testing.T) {
			testClaudeMessage(t, claudeKey, model, false)
		})
		time.Sleep(testInterval)
		t.Run(model+"_通过Claude端点_流式", func(t *testing.T) {
			testClaudeMessage(t, claudeKey, model, true)
		})
	}
}

// TestClaudeMessagesWithNoSignature checks acceptance of unsigned thinking history.
// The fixture expects the configured deployment to route the request to Gemini.
func TestClaudeMessagesWithNoSignature(t *testing.T) {
	claudeKey := requireClaudeAPIKey(t)
	models := []string{
		"claude-haiku-4-5-20251001", // Fixture expects gemini-3-flash routing with unsigned thinking history.
	}
	for i, model := range models {
		if i > 0 {
			time.Sleep(testInterval)
		}
		t.Run(model+"_无signature", func(t *testing.T) {
			testClaudeWithNoSignature(t, claudeKey, model)
		})
	}
}

func testClaudeWithNoSignature(t *testing.T, claudeKey string, model string) {
	url := baseURL + endpointPrefix + "/v1/messages"

	// Include an unsigned thinking block in the conversation history.
	payload := map[string]any{
		"model":      model,
		"max_tokens": 200,
		"stream":     false,
		// Enable thinking for the continuation.
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": 1024,
		},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "What is 2+2?",
			},
			// The assistant history contains an unsigned thinking block.
			map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{
						"type":     "thinking",
						"thinking": "Let me calculate 2+2...",
						// Signature intentionally omitted.
					},
					{
						"type": "text",
						"text": "2+2 equals 4.",
					},
				},
			},
			map[string]any{
				"role":    "user",
				"content": "What is 3+3?",
			},
		},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+claudeKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 400 {
		t.Fatalf("无 signature thinking 处理失败，收到 400 错误: %s", string(respBody))
	}

	if resp.StatusCode == 503 {
		t.Skipf("提供商暂时不可用 (503): %s", string(respBody))
	}

	if resp.StatusCode == 429 {
		t.Skipf("请求被限流 (429): %s", string(respBody))
	}

	if resp.StatusCode != 200 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if result["type"] != "message" {
		t.Errorf("期望 type=message, 得到 %v", result["type"])
	}
	t.Logf("✅ 无 signature thinking 处理测试通过, id=%v", result["id"])
}

// TestGeminiEndpointWithClaudeModel sends Claude names through the Gemini endpoint.
// It runs when ENDPOINT_PREFIX is /antigravity.
func TestGeminiEndpointWithClaudeModel(t *testing.T) {
	if endpointPrefix != "/antigravity" {
		t.Skip("仅在 Antigravity 模式下运行")
	}
	geminiKey := requireGeminiAPIKey(t)

	// Claude names sent through the Gemini endpoint.
	claudeViaGemini := []string{
		"claude-sonnet-4-5",
		"claude-opus-4-5-thinking",
	}

	for i, model := range claudeViaGemini {
		if i > 0 {
			time.Sleep(testInterval)
		}
		t.Run(model+"_通过Gemini端点", func(t *testing.T) {
			testGeminiGenerate(t, geminiKey, model, false)
		})
		time.Sleep(testInterval)
		t.Run(model+"_通过Gemini端点_流式", func(t *testing.T) {
			testGeminiGenerate(t, geminiKey, model, true)
		})
	}
}
