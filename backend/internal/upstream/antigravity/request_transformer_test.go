package antigravity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// Antigravity conversion follows mapped-model signature policy while retaining
// client model selection and custom instructions. Fixtures exercise thinking
// downgrade and dynamic budgets, grouped function/search declarations, moved
// system turns, and catalogue-driven omission of generation fields and empty
// tool configuration.

// TestBuildParts_ThinkingBlockWithoutSignature converts unsigned, signed, and
// dummy-enabled thinking fixtures through the shared parts builder. Selected
// parts must retain supplied/dummy signatures or ordinary text according to
// policy. The returned downgrade flag is discarded by this test.
func TestBuildParts_ThinkingBlockWithoutSignature(t *testing.T) {
	tests := []struct {
		name              string // human-readable table case name
		content           string // encoded input content block array
		allowDummyThought bool   // selected dummy thought signature policy
		expectedParts     int    // expected converted part count
		description       string // case-specific assertion diagnostic text
	}{
		{
			name: "Claude model - downgrade thinking to text without signature",
			content: `[
				{"type": "text", "text": "Hello"},
				{"type": "thinking", "thinking": "Let me think...", "signature": ""},
				{"type": "text", "text": "World"}
			]`,
			allowDummyThought: false,
			expectedParts:     3, // thinking becomes an ordinary text part
			description:       "Claude模型缺少signature时应将thinking降级为text，并在上层禁用thinking mode",
		},
		{
			name: "Claude model - preserve thinking block with signature",
			content: `[
				{"type": "text", "text": "Hello"},
				{"type": "thinking", "thinking": "Let me think...", "signature": "sig_real_123"},
				{"type": "text", "text": "World"}
			]`,
			allowDummyThought: false,
			expectedParts:     3,
			description:       "Claude模型应透传带 signature 的 thinking block（用于 Vertex 签名链路）",
		},
		{
			name: "Gemini model - use dummy signature",
			content: `[
				{"type": "text", "text": "Hello"},
				{"type": "thinking", "thinking": "Let me think...", "signature": ""},
				{"type": "text", "text": "World"}
			]`,
			allowDummyThought: true,
			expectedParts:     3, // all blocks remain; thinking receives a dummy signature
			description:       "Gemini模型应该为无signature的thinking block使用dummy signature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolIDToName := make(map[string]string)
			parts, _, err := bridge.BuildParts(json.RawMessage(tt.content), toolIDToName, tt.allowDummyThought)
			if err != nil {
				t.Fatalf("buildParts() error = %v", err)
			}

			if len(parts) != tt.expectedParts {
				t.Errorf("%s: got %d parts, want %d parts", tt.description, len(parts), tt.expectedParts)
			}

			switch tt.name {
			case "Claude model - preserve thinking block with signature":
				if len(parts) != 3 {
					t.Fatalf("expected 3 parts, got %d", len(parts))
				}
				if !parts[1].Thought || parts[1].ThoughtSignature != "sig_real_123" {
					t.Fatalf("expected thought part with signature sig_real_123, got thought=%v signature=%q",
						parts[1].Thought, parts[1].ThoughtSignature)
				}
			case "Claude model - downgrade thinking to text without signature":
				if len(parts) != 3 {
					t.Fatalf("expected 3 parts, got %d", len(parts))
				}
				if parts[1].Thought {
					t.Fatalf("expected downgraded text part, got thought=%v signature=%q",
						parts[1].Thought, parts[1].ThoughtSignature)
				}
				if parts[1].Text != "Let me think..." {
					t.Fatalf("expected downgraded text %q, got %q", "Let me think...", parts[1].Text)
				}
			case "Gemini model - use dummy signature":
				if len(parts) != 3 {
					t.Fatalf("expected 3 parts, got %d", len(parts))
				}
				if !parts[1].Thought || parts[1].ThoughtSignature != DummyThoughtSignature {
					t.Fatalf("expected dummy thought signature, got thought=%v signature=%q",
						parts[1].Thought, parts[1].ThoughtSignature)
				}
			}
		})
	}
}

// TestBuildParts_ToolUseSignatureHandling compares tool-use conversion under
// dummy-enabled and strict policies. Supplied signatures survive both
// policies, while unsigned dummy-enabled input receives the shared
// placeholder.
func TestBuildParts_ToolUseSignatureHandling(t *testing.T) {
	content := `[
		{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "ls"}, "signature": "sig_tool_abc"}
	]`

	t.Run("Gemini preserves provided tool_use signature", func(t *testing.T) {
		toolIDToName := make(map[string]string)
		parts, _, err := bridge.BuildParts(json.RawMessage(content), toolIDToName, true)
		if err != nil {
			t.Fatalf("buildParts() error = %v", err)
		}
		if len(parts) != 1 || parts[0].FunctionCall == nil {
			t.Fatalf("expected 1 functionCall part, got %+v", parts)
		}
		if parts[0].ThoughtSignature != "sig_tool_abc" {
			t.Fatalf("expected preserved tool signature %q, got %q", "sig_tool_abc", parts[0].ThoughtSignature)
		}
	})

	t.Run("Gemini falls back to dummy tool_use signature when missing", func(t *testing.T) {
		contentNoSig := `[
			{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "ls"}}
		]`
		toolIDToName := make(map[string]string)
		parts, _, err := bridge.BuildParts(json.RawMessage(contentNoSig), toolIDToName, true)
		if err != nil {
			t.Fatalf("buildParts() error = %v", err)
		}
		if len(parts) != 1 || parts[0].FunctionCall == nil {
			t.Fatalf("expected 1 functionCall part, got %+v", parts)
		}
		if parts[0].ThoughtSignature != DummyThoughtSignature {
			t.Fatalf("expected dummy tool signature %q, got %q", DummyThoughtSignature, parts[0].ThoughtSignature)
		}
	})

	t.Run("Claude model - preserve valid signature for tool_use", func(t *testing.T) {
		toolIDToName := make(map[string]string)
		parts, _, err := bridge.BuildParts(json.RawMessage(content), toolIDToName, false)
		if err != nil {
			t.Fatalf("buildParts() error = %v", err)
		}
		if len(parts) != 1 || parts[0].FunctionCall == nil {
			t.Fatalf("expected 1 functionCall part, got %+v", parts)
		}
		// The strict conversion policy retains this supplied fixture signature on
		// the returned part; this assertion does not establish vendor validation.
		if parts[0].ThoughtSignature != "sig_tool_abc" {
			t.Fatalf("expected preserved tool signature %q, got %q", "sig_tool_abc", parts[0].ThoughtSignature)
		}
	})
}

// TestBuildTools_CustomTypeTools compares standard and nested custom tool
// declarations with invalid inputs. Valid functions must be grouped into
// declarations, while unsupported or incomplete fixtures are skipped as
// asserted.
func TestBuildTools_CustomTypeTools(t *testing.T) {
	tests := []struct {
		name        string       // human-readable table case name
		tools       []ClaudeTool // input tool declaration sequence
		expectedLen int          // expected converted declaration count
		description string       // case-specific assertion diagnostic text
	}{
		{
			name: "Standard tool format",
			tools: []ClaudeTool{
				{
					Name:        "get_weather",
					Description: "Get weather information",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{"type": "string"},
						},
					},
				},
			},
			expectedLen: 1,
			description: "标准工具格式应该正常转换",
		},
		{
			name: "Custom type tool (MCP format)",
			tools: []ClaudeTool{
				{
					Type: "custom",
					Name: "mcp_tool",
					Custom: &ClaudeCustomToolSpec{
						Description: "MCP tool description",
						InputSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"param": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
			expectedLen: 1,
			description: "Custom类型工具应该从Custom字段读取description和input_schema",
		},
		{
			name: "Mixed standard and custom tools",
			tools: []ClaudeTool{
				{
					Name:        "standard_tool",
					Description: "Standard tool",
					InputSchema: map[string]any{"type": "object"},
				},
				{
					Type: "custom",
					Name: "custom_tool",
					Custom: &ClaudeCustomToolSpec{
						Description: "Custom tool",
						InputSchema: map[string]any{"type": "object"},
					},
				},
			},
			expectedLen: 1, // one Gemini tool entry contains two function declarations
			description: "混合标准和custom工具应该都能正确转换",
		},
		{
			name: "Invalid custom tool - nil Custom field",
			tools: []ClaudeTool{
				{
					Type: "custom",
					Name: "invalid_custom",
					// Custom is intentionally nil.
				},
			},
			expectedLen: 0, // invalid declaration is skipped
			description: "Custom字段为nil的custom工具应该被跳过",
		},
		{
			name: "Invalid custom tool - nil InputSchema",
			tools: []ClaudeTool{
				{
					Type: "custom",
					Name: "invalid_custom",
					Custom: &ClaudeCustomToolSpec{
						Description: "Invalid",
						// InputSchema is intentionally nil.
					},
				},
			},
			expectedLen: 0, // invalid declaration is skipped
			description: "InputSchema为nil的custom工具应该被跳过",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildTools(tt.tools)

			if len(result) != tt.expectedLen {
				t.Errorf("%s: got %d tool declarations, want %d", tt.description, len(result), tt.expectedLen)
			}

			if len(result) > 0 && result[0].FunctionDeclarations != nil {
				if len(result[0].FunctionDeclarations) != len(tt.tools) {
					t.Errorf("%s: got %d function declarations, want %d",
						tt.description, len(result[0].FunctionDeclarations), len(tt.tools))
				}
			}
		})
	}
}

// TestBuildTools_PreservesWebSearchAlongsideFunctions converts function tools
// and a search declaration together. The result must retain separate function
// and search entries, including the configured enhanced image-search result
// limit.
func TestBuildTools_PreservesWebSearchAlongsideFunctions(t *testing.T) {
	tools := []ClaudeTool{
		{
			Name:        "get_weather",
			Description: "Get weather information",
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Type: "web_search_20250305",
			Name: "web_search",
		},
	}

	result := buildTools(tools)
	require.Len(t, result, 2)
	require.Len(t, result[0].FunctionDeclarations, 1)
	require.Equal(t, "get_weather", result[0].FunctionDeclarations[0].Name)
	require.NotNil(t, result[1].GoogleSearch)
	require.NotNil(t, result[1].GoogleSearch.EnhancedContent)
	require.NotNil(t, result[1].GoogleSearch.EnhancedContent.ImageSearch)
	require.Equal(t, 5, result[1].GoogleSearch.EnhancedContent.ImageSearch.MaxResultCount)
}

// TestBuildGenerationConfig_ThinkingDynamicBudget compares enabled, adaptive,
// and disabled thinking across selected models. The output must use explicit,
// dynamic, or catalogue adaptive budget values according to each fixture.
func TestBuildGenerationConfig_ThinkingDynamicBudget(t *testing.T) {
	tests := []struct {
		name        string          // human-readable table case name
		model       string          // input catalogue model identity
		thinking    *ThinkingConfig // input thinking mode and budget
		wantBudget  int             // expected selected token budget
		wantPresent bool            // expected thinking configuration presence
	}{
		{
			name:        "enabled without budget defaults to dynamic (-1)",
			model:       "claude-opus-4-6-thinking",
			thinking:    &ThinkingConfig{Type: "enabled"},
			wantBudget:  -1,
			wantPresent: true,
		},
		{
			name:        "enabled with budget uses the provided value",
			model:       "claude-opus-4-6-thinking",
			thinking:    &ThinkingConfig{Type: "enabled", BudgetTokens: 1024},
			wantBudget:  1024,
			wantPresent: true,
		},
		{
			name:        "enabled with -1 budget uses dynamic (-1)",
			model:       "claude-opus-4-6-thinking",
			thinking:    &ThinkingConfig{Type: "enabled", BudgetTokens: -1},
			wantBudget:  -1,
			wantPresent: true,
		},
		{
			name:        "adaptive on opus4.6 maps to high budget (24576)",
			model:       "claude-opus-4-6-thinking",
			thinking:    &ThinkingConfig{Type: "adaptive", BudgetTokens: 20000},
			wantBudget:  ClaudeAdaptiveHighThinkingBudgetTokens,
			wantPresent: true,
		},
		{
			name:        "adaptive on non-opus model keeps default dynamic (-1)",
			model:       "claude-sonnet-4-5-thinking",
			thinking:    &ThinkingConfig{Type: "adaptive"},
			wantBudget:  -1,
			wantPresent: true,
		},
		{
			name:        "disabled does not emit thinkingConfig",
			model:       "claude-opus-4-6-thinking",
			thinking:    &ThinkingConfig{Type: "disabled", BudgetTokens: 1024},
			wantBudget:  0,
			wantPresent: false,
		},
		{
			name:        "nil thinking does not emit thinkingConfig",
			model:       "claude-opus-4-6-thinking",
			thinking:    nil,
			wantBudget:  0,
			wantPresent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &ClaudeRequest{
				Model:    tt.model,
				Thinking: tt.thinking,
			}
			cfg := buildGenerationConfig(req)
			if cfg == nil {
				t.Fatalf("expected non-nil generationConfig")
			}

			if tt.wantPresent {
				if cfg.ThinkingConfig == nil {
					t.Fatalf("expected thinkingConfig to be present")
				}
				if !cfg.ThinkingConfig.IncludeThoughts {
					t.Fatalf("expected includeThoughts=true")
				}
				if cfg.ThinkingConfig.ThinkingBudget != tt.wantBudget {
					t.Fatalf("expected thinkingBudget=%d, got %d", tt.wantBudget, cfg.ThinkingConfig.ThinkingBudget)
				}
				return
			}

			if cfg.ThinkingConfig != nil {
				t.Fatalf("expected thinkingConfig to be nil, got %+v", cfg.ThinkingConfig)
			}
		})
	}
}

// TestTransformClaudeToGeminiWithOptions_PreservesBillingHeaderSystemBlock
// provides billing text in string and text-block system forms. Conversion must
// retain the selected billing instruction among output system parts for both
// representations.
func TestTransformClaudeToGeminiWithOptions_PreservesBillingHeaderSystemBlock(t *testing.T) {
	tests := []struct {
		name   string          // human-readable table case name
		system json.RawMessage // encoded top-level system input
	}{
		{
			name:   "system array",
			system: json.RawMessage(`[{"type":"text","text":"x-anthropic-billing-header keep"}]`),
		},
		{
			name:   "system string",
			system: json.RawMessage(`"x-anthropic-billing-header keep"`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeReq := &ClaudeRequest{
				Model:  "claude-3-5-sonnet-latest",
				System: tt.system,
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`[{"type":"text","text":"hello"}]`),
					},
				},
			}

			body, err := TransformClaudeToGeminiWithOptions(claudeReq, "project-1", "gemini-2.5-flash", DefaultTransformOptions())
			require.NoError(t, err)

			var req V1InternalRequest
			require.NoError(t, json.Unmarshal(body, &req))
			require.NotNil(t, req.Request.SystemInstruction)

			found := false
			for _, part := range req.Request.SystemInstruction.Parts {
				if strings.Contains(part.Text, "x-anthropic-billing-header keep") {
					found = true
					break
				}
			}

			require.True(t, found, "转换后的 systemInstruction 应保留 x-anthropic-billing-header 内容")
		})
	}
}

// TestTransformClaudeToGeminiWithOptions_MessageRoles converts ordinary
// conversation turns and embedded system messages. Assistant roles become
// model roles, system parts move to instructions, and their ordering follows
// top-level system text.
func TestTransformClaudeToGeminiWithOptions_MessageRoles(t *testing.T) {
	transform := func(t *testing.T, claudeReq *ClaudeRequest) V1InternalRequest {
		t.Helper()

		body, err := TransformClaudeToGeminiWithOptions(claudeReq, "project-1", "gemini-2.5-flash", DefaultTransformOptions())
		require.NoError(t, err)

		var req V1InternalRequest
		require.NoError(t, json.Unmarshal(body, &req))
		return req
	}

	systemText := func(content *GeminiContent) string {
		if content == nil {
			return ""
		}
		var texts []string
		for _, part := range content.Parts {
			texts = append(texts, part.Text)
		}
		return strings.Join(texts, "\n")
	}

	t.Run("message system role moves to system instruction", func(t *testing.T) {
		req := transform(t, &ClaudeRequest{
			Model: "claude-3-5-sonnet-latest",
			Messages: []ClaudeMessage{
				{
					Role:    "system",
					Content: json.RawMessage(`[{"type":"text","text":"skills context"}]`),
				},
				{
					Role:    "user",
					Content: json.RawMessage(`"hello"`),
				},
			},
		})

		require.Len(t, req.Request.Contents, 1)
		require.Equal(t, "user", req.Request.Contents[0].Role)
		require.Contains(t, systemText(req.Request.SystemInstruction), "skills context")
		for _, content := range req.Request.Contents {
			require.NotEqual(t, "system", content.Role)
		}
	})

	t.Run("assistant role still maps to model", func(t *testing.T) {
		req := transform(t, &ClaudeRequest{
			Model: "claude-3-5-sonnet-latest",
			Messages: []ClaudeMessage{
				{
					Role:    "assistant",
					Content: json.RawMessage(`"hello from assistant"`),
				},
			},
		})

		require.Len(t, req.Request.Contents, 1)
		require.Equal(t, "model", req.Request.Contents[0].Role)
		require.Equal(t, "hello from assistant", req.Request.Contents[0].Parts[0].Text)
	})

	t.Run("top level and message system instructions are merged", func(t *testing.T) {
		req := transform(t, &ClaudeRequest{
			Model:  "claude-3-5-sonnet-latest",
			System: json.RawMessage(`"top level system"`),
			Messages: []ClaudeMessage{
				{
					Role:    "system",
					Content: json.RawMessage(`"message system"`),
				},
				{
					Role:    "user",
					Content: json.RawMessage(`"hello"`),
				},
			},
		})

		mergedSystem := systemText(req.Request.SystemInstruction)
		require.Contains(t, mergedSystem, "top level system")
		require.Contains(t, mergedSystem, "message system")
		require.Less(t, strings.Index(mergedSystem, "top level system"), strings.Index(mergedSystem, "message system"))
		require.Len(t, req.Request.Contents, 1)
		require.Equal(t, "user", req.Request.Contents[0].Role)
	})

	t.Run("ordinary user assistant conversation is unchanged", func(t *testing.T) {
		req := transform(t, &ClaudeRequest{
			Model: "claude-3-5-sonnet-latest",
			Messages: []ClaudeMessage{
				{
					Role:    "user",
					Content: json.RawMessage(`"question"`),
				},
				{
					Role:    "assistant",
					Content: json.RawMessage(`"answer"`),
				},
			},
		})

		require.Len(t, req.Request.Contents, 2)
		require.Equal(t, "user", req.Request.Contents[0].Role)
		require.Equal(t, "question", req.Request.Contents[0].Parts[0].Text)
		require.Equal(t, "model", req.Request.Contents[1].Role)
		require.Equal(t, "answer", req.Request.Contents[1].Parts[0].Text)
	})
}

// TestTransformClaudeToGeminiWithOptions_PreservesWebSearchAlongsideFunctions
// converts search and function tools in a wrapped request. Output must retain
// both categories and enable server-side invocation handling for their
// combination.
func TestTransformClaudeToGeminiWithOptions_PreservesWebSearchAlongsideFunctions(t *testing.T) {
	claudeReq := &ClaudeRequest{
		Model: "claude-3-5-sonnet-latest",
		Messages: []ClaudeMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`[{"type":"text","text":"hello"}]`),
			},
		},
		Tools: []ClaudeTool{
			{
				Name:        "get_weather",
				Description: "Get weather information",
				InputSchema: map[string]any{"type": "object"},
			},
			{
				Type: "web_search_20250305",
				Name: "web_search",
			},
		},
	}

	body, err := TransformClaudeToGeminiWithOptions(claudeReq, "project-1", "gemini-2.5-flash", DefaultTransformOptions())
	require.NoError(t, err)

	var req V1InternalRequest
	require.NoError(t, json.Unmarshal(body, &req))
	require.Len(t, req.Request.Tools, 2)
	require.Len(t, req.Request.Tools[0].FunctionDeclarations, 1)
	require.Equal(t, "get_weather", req.Request.Tools[0].FunctionDeclarations[0].Name)
	require.NotNil(t, req.Request.Tools[1].GoogleSearch)
	require.NotNil(t, req.Request.ToolConfig)
	require.NotNil(t, req.Request.ToolConfig.IncludeServerSideToolInvocations)
	require.True(t, *req.Request.ToolConfig.IncludeServerSideToolInvocations)
}

// TestTransformClaudeToGeminiWithOptions_ToolInvocationFlagOnlyForMixedTools
// compares search-only, function-only, and mixed request tools. The invocation
// flag must be enabled for the mixed fixture and absent for either category
// alone.
func TestTransformClaudeToGeminiWithOptions_ToolInvocationFlagOnlyForMixedTools(t *testing.T) {
	functionTool := ClaudeTool{
		Name:        "get_weather",
		Description: "Get weather information",
		InputSchema: map[string]any{"type": "object"},
	}
	webSearchTool := ClaudeTool{Type: "web_search_20250305", Name: "web_search"}

	transform := func(t *testing.T, tools []ClaudeTool) *V1InternalRequest {
		t.Helper()
		body, err := TransformClaudeToGeminiWithOptions(&ClaudeRequest{
			Model: "claude-3-5-sonnet-latest",
			Messages: []ClaudeMessage{{
				Role:    "user",
				Content: json.RawMessage(`[ {"type":"text","text":"hello"} ]`),
			}},
			Tools: tools,
		}, "project-1", "gemini-2.5-flash", DefaultTransformOptions())
		require.NoError(t, err)
		var request V1InternalRequest
		require.NoError(t, json.Unmarshal(body, &request))
		return &request
	}

	functionOnly := transform(t, []ClaudeTool{functionTool})
	require.NotNil(t, functionOnly.Request.ToolConfig)
	require.Nil(t, functionOnly.Request.ToolConfig.IncludeServerSideToolInvocations)

	searchOnly := transform(t, []ClaudeTool{webSearchTool})
	require.NotNil(t, searchOnly.Request.ToolConfig)
	require.Nil(t, searchOnly.Request.ToolConfig.IncludeServerSideToolInvocations)

	mixed := transform(t, []ClaudeTool{functionTool, webSearchTool})
	require.NotNil(t, mixed.Request.ToolConfig)
	require.NotNil(t, mixed.Request.ToolConfig.IncludeServerSideToolInvocations)
	require.True(t, *mixed.Request.ToolConfig.IncludeServerSideToolInvocations)
}

// TestTransformClaudeToGeminiWithOptions_GeminiReasoningSkipsInvalidArguments
// converts a registered reasoning model without tools. Catalogue rules must
// omit unsupported temperature, top-p, top-k, stop sequences, and the empty
// tool configuration.
func TestTransformClaudeToGeminiWithOptions_GeminiReasoningSkipsInvalidArguments(t *testing.T) {
	temperature := 0.7
	topP := 0.9
	topK := 40
	claudeReq := &ClaudeRequest{
		Model:       "claude-3-5-sonnet-latest",
		MaxTokens:   4096,
		Temperature: &temperature,
		TopP:        &topP,
		TopK:        &topK,
		Messages: []ClaudeMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"hello"`),
			},
		},
	}

	body, err := TransformClaudeToGeminiWithOptions(claudeReq, "project-1", "gemini-3.1-pro-high", DefaultTransformOptions())
	require.NoError(t, err)

	var req V1InternalRequest
	require.NoError(t, json.Unmarshal(body, &req))
	require.Nil(t, req.Request.ToolConfig)
	require.NotNil(t, req.Request.GenerationConfig)
	require.Nil(t, req.Request.GenerationConfig.Temperature)
	require.Nil(t, req.Request.GenerationConfig.TopP)
	require.Nil(t, req.Request.GenerationConfig.TopK)
	require.Empty(t, req.Request.GenerationConfig.StopSequences)
	require.Equal(t, 4096, req.Request.GenerationConfig.MaxOutputTokens)
}

// TestTransformClaudeToGeminiWithOptions_GeminiReasoningKeepsToolConfigWithTools
// converts a reasoning model with function tools. Tool presence must retain
// the VALIDATED calling configuration even when that model omits an empty
// configuration.
func TestTransformClaudeToGeminiWithOptions_GeminiReasoningKeepsToolConfigWithTools(t *testing.T) {
	claudeReq := &ClaudeRequest{
		Model: "claude-3-5-sonnet-latest",
		Messages: []ClaudeMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"hello"`),
			},
		},
		Tools: []ClaudeTool{{
			Name:        "get_weather",
			Description: "Get weather information",
			InputSchema: map[string]any{"type": "object"},
		}},
	}

	body, err := TransformClaudeToGeminiWithOptions(claudeReq, "project-1", "gemini-3.1-pro-high", DefaultTransformOptions())
	require.NoError(t, err)

	var req V1InternalRequest
	require.NoError(t, json.Unmarshal(body, &req))
	require.NotNil(t, req.Request.ToolConfig)
	require.NotNil(t, req.Request.ToolConfig.FunctionCallingConfig)
	require.Equal(t, "VALIDATED", req.Request.ToolConfig.FunctionCallingConfig.Mode)
	require.Len(t, req.Request.Tools, 1)
}

// TestWebSearchPreservesSelectedModel converts a web-search request with an
// explicitly mapped model. The wrapped model must equal each selected fixture
// identity; requestType is not asserted by this test.
func TestWebSearchPreservesSelectedModel(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-5", "gemini-3.6-flash"} {
		request := &ClaudeRequest{
			Model:    model,
			Messages: []ClaudeMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
			Tools:    []ClaudeTool{{Type: "web_search_20250305", Name: "web_search"}},
		}
		body, err := TransformClaudeToGeminiWithOptions(request, "project", model, DefaultTransformOptions())
		require.NoError(t, err)
		var wrapped V1InternalRequest
		require.NoError(t, json.Unmarshal(body, &wrapped))
		require.Equal(t, model, wrapped.Model)
	}
}

// TestIdentityPatchOmitsModelDeclaration converts a Claude request with the
// default identity patch. Its serialized body must contain the Antigravity
// marker and omit both model-declaration phrases asserted by the fixture.
func TestIdentityPatchOmitsModelDeclaration(t *testing.T) {
	var request ClaudeRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-sonnet-4-6","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`), &request))
	body, err := TransformClaudeToGemini(&request, "project", "claude-sonnet-4-6")
	require.NoError(t, err)
	require.Contains(t, string(body), "You are Antigravity")
	require.NotContains(t, string(body), "You are Model")
	require.NotContains(t, string(body), "ModelId is")
}

// TestReasoningRequestRules compares known reasoning catalogue identities and
// unrelated names. The empty-tool rule must match the configured models and
// remain false for the unknown fixture.
func TestReasoningRequestRules(t *testing.T) {
	for _, id := range []string{"gemini-2.5-flash-thinking", "gemini-3-pro-high", "gemini-3.1-pro-high", "gemini-3.6-flash-high", "gemini-3.6-flash-low", "gemini-3.6-flash-medium", "gemini-3.6-flash-tiered"} {
		if !IsGeminiReasoningModel(id) {
			t.Errorf("missing request rule for %s", id)
		}
	}
	if IsGeminiReasoningModel("unknown-model") {
		t.Fatal("unknown model acquired reasoning rules")
	}
}
