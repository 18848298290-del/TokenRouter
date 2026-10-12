package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func TestAliasOpenAIOAuthReservedToolNames_RewritesDeclarationsAndReferences(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{
			map[string]any{"type": "function", "name": "python"},
			map[string]any{"type": "namespace", "name": "code", "tools": []any{
				map[string]any{"type": "function", "name": "shell"},
			}},
		},
		"tool_choice": map[string]any{"type": "function", "name": "python"},
		"input": []any{
			map[string]any{"type": "function_call", "name": "python", "call_id": "fc_1"},
			map[string]any{"type": "additional_tools", "tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "python"}},
			}},
		},
	}

	reverse, changed, err := openai.AliasOpenAIOAuthReservedToolNames(reqBody)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "python", reverse[openai.CodexPythonToolAlias])
	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, tools)
	firstTool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, openai.CodexPythonToolAlias, firstTool["name"])
	toolChoice, ok := reqBody["tool_choice"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, openai.CodexPythonToolAlias, toolChoice["name"])
	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, input)
	firstInput, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, openai.CodexPythonToolAlias, firstInput["name"])
	secondInput, ok := input[1].(map[string]any)
	require.True(t, ok)
	nestedTools, ok := secondInput["tools"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, nestedTools)
	nestedTool, ok := nestedTools[0].(map[string]any)
	require.True(t, ok)
	nestedFn, ok := nestedTool["function"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, openai.CodexPythonToolAlias, nestedFn["name"])
}

func TestAliasOpenAIOAuthReservedToolNames_CollisionDoesNotMutate(t *testing.T) {
	reqBody := map[string]any{"tools": []any{
		map[string]any{"type": "function", "name": "python"},
		map[string]any{"type": "function", "name": openai.CodexPythonToolAlias},
	}}
	before, err := json.Marshal(reqBody)
	require.NoError(t, err)

	reverse, changed, err := openai.AliasOpenAIOAuthReservedToolNames(reqBody)
	require.ErrorContains(t, err, `both normalize to "python__tokenrouter"`)
	require.False(t, changed)
	require.Nil(t, reverse)
	after, marshalErr := json.Marshal(reqBody)
	require.NoError(t, marshalErr)
	require.JSONEq(t, string(before), string(after))
}

func TestApplyCodexOAuthTransform_ReservedPythonNameIsOAuthOnly(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.5",
		"tools": []any{map[string]any{"type": "function", "name": "PYTHON"}},
	}

	result := gatewayprovider.ApplyCodexOAuthTransform(reqBody, true, false)
	require.NoError(t, result.Error)
	require.Equal(t, "PYTHON", result.ToolNameReverse[openai.CodexPythonToolAlias])
	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, tools)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, openai.CodexPythonToolAlias, tool["name"])

	apiKeyBody := []byte(`{"type":"response.create","tools":[{"type":"function","name":"python"}]}`)
	normalized, changed, err := gatewayprovider.NormalizeOpenAIResponsesWebSocketCompatibilityBody(apiKeyBody, gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}), false)
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, string(apiKeyBody), string(normalized))
}

func TestAliasOpenAIOAuthReservedToolNames_SessionUpdateOnlyTouchesFunctionProtocolNodes(t *testing.T) {
	body := []byte(`{"type":"session.update","session":{"tools":[{"type":"function","name":"python"},{"type":"image_generation","name":"python"},{"type":"namespace","name":"python","tools":[{"type":"function","name":"shell"}]}]},"metadata":{"name":"python"},"sequence":900719925474099312345}`)

	aliased, reverse, changed, err := openai.AliasOpenAIOAuthReservedToolNamesBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "python", reverse[openai.CodexPythonToolAlias])
	require.Equal(t, openai.CodexPythonToolAlias, gjson.GetBytes(aliased, "session.tools.0.name").String())
	require.Equal(t, "python", gjson.GetBytes(aliased, "session.tools.1.name").String())
	require.Equal(t, "python", gjson.GetBytes(aliased, "session.tools.2.name").String())
	require.Equal(t, "shell", gjson.GetBytes(aliased, "session.tools.2.tools.0.name").String())
	require.Equal(t, "python", gjson.GetBytes(aliased, "metadata.name").String())
	require.Equal(t, "900719925474099312345", gjson.GetBytes(aliased, "sequence").Raw)
}

func TestAliasOpenAIOAuthReservedToolNames_PromptCompatibilityRunsFirst(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","prompt":[{"type":"function_call","name":"python","call_id":"fc_1"}],"functions":[{"name":"python"}],"function_call":{"name":"python"},"sequence":900719925474099312345}`)
	reqBody, err := requeststate.DecodeOpenAIRequestBody(body)
	require.NoError(t, err)
	result := gatewayprovider.ApplyCodexOAuthTransform(reqBody, true, false)
	require.NoError(t, result.Error)
	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, input)
	firstInput, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, openai.CodexPythonToolAlias, firstInput["name"])
	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, tools)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, openai.CodexPythonToolAlias, tool["name"])
	toolChoice, ok := reqBody["tool_choice"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, openai.CodexPythonToolAlias, toolChoice["name"])
	encoded, err := json.Marshal(reqBody)
	require.NoError(t, err)
	require.Equal(t, "900719925474099312345", gjson.GetBytes(encoded, "sequence").Raw)
}

// TestLegacyPythonAliasContinuation checks stable call IDs and distinguishes declared client tools from
// legacy aliases.
func TestLegacyPythonAliasContinuation(t *testing.T) {
	for _, declared := range []bool{false, true} {
		tools := []any{map[string]any{"type": "function", "name": "python"}}
		if declared {
			tools = append(tools, map[string]any{"type": "function", "name": "python__sub2api"})
		}
		call := map[string]any{"type": "function_call", "name": "python__sub2api", "call_id": "fc_existing"}
		result := map[string]any{"type": "function_call_output", "call_id": "fc_existing", "output": "ok"}
		body := map[string]any{"tools": tools, "input": []any{call, result}}
		reverse, changed, err := openai.AliasOpenAIOAuthReservedToolNames(body)
		require.NoError(t, err)
		require.True(t, changed)
		want := "python__sub2api"
		if declared {
			want = openai.CodexPythonToolAlias
		}
		declaration, ok := tools[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, want, declaration["name"])
		require.Equal(t, "python", reverse[want])
		require.Equal(t, "python__sub2api", call["name"])
		require.Equal(t, result["call_id"], call["call_id"])
	}
}

// TestLegacyCodexPromptMarkers checks that legacy markers prevent duplicate instruction injection.
func TestLegacyCodexPromptMarkers(t *testing.T) {
	body := map[string]any{"instructions": "<sub2api-codex-image-generation>existing", "tools": []any{map[string]any{"type": "image_generation"}}}
	require.False(t, openai.ApplyCodexImageGenerationBridgeInstructions(body, false))
	body["instructions"] = "<sub2api-codex-spark-image-unsupported>existing"
	require.False(t, openai.ApplyCodexSparkImageUnsupportedInstructions(body))
	require.True(t, gatewayprovider.IsOpenAICompatMessagesBridgeBody([]byte(`{"input":[{"role":"developer","content":"<sub2api-claude-code-todo-guard>existing"}]}`)))
}

// TestCodexBrandRenamePreservesStableIDs checks fixed session, thread, and call ID derivations.
func TestCodexBrandRenamePreservesStableIDs(t *testing.T) {
	require.Equal(t, "d4c42b33-6e2b-4490-bbb7-24d247c0086c", openai.ResolveConvergedSessionID("fixture"))
	require.Equal(t, "d7459669-7037-4803-8349-a74132ac7fee", openai.ResolveConvergedThreadID("fixture", "client"))
	require.Equal(t, "fc_cd5526d43f09899a7c972860565aad45b5cc5dc0c297895dd60c7e3ca4227", openai.CompactCodexCallIDForItemType("function_call", "fixture"))
}

func TestOpenAIRequestBodyMayContainEmptyBase64InputImageSeesEscapedJSON(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","content":[{"type":"input_image","image_` + "\\u0075" + `rl":"data:image/png;base64` + "\\u002c" + `   "}]}]}`)

	require.True(t, openai.OpenAIRequestBodyMayContainEmptyBase64InputImage(body))
}

func TestOpenAIRequestBodyMayContainEmptyBase64InputImageSeesEscapedImageType(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","content":[{"type":"input_` + "\\u0069" + `mage","image_url":"data:image/png;base64,   "}]}]}`)

	require.True(t, openai.OpenAIRequestBodyMayContainEmptyBase64InputImage(body))
}

func TestOpenAIRequestBodyMayContainEmptyBase64InputImageSeesEscapedInputPrefix(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","content":[{"type":"inp` + "\\u0075" + `t_image","image_url":"data:image/png;base64,   "}]}]}`)

	require.True(t, openai.OpenAIRequestBodyMayContainEmptyBase64InputImage(body))
}

func TestGetOpenAIRequestBodyMap_DoesNotWriteContextCache(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	adapter := openAIForwardTransformAdapter{openAIForwardPreludeAdapter: openAIForwardPreludeAdapter{c: c}}
	got, err := adapter.Decode([]byte(`{"model":"gpt-5","stream":true}`))
	require.NoError(t, err)
	require.Equal(t, "gpt-5", got["model"])
	require.Empty(t, c.Keys)
}

func TestSanitizeEmptyBase64InputImagesInOpenAIRequestBodyMap(t *testing.T) {
	var reqBody map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"gpt-5.4",
		"input":[
			{"role":"user","content":[
				{"type":"input_text","text":"Describe this"},
				{"type":"input_image","image_url":"data:image/png;base64,   "},
				{"type":"input_image","image_url":"data:image/png;base64,abc123"}
			]},
			{"role":"user","content":[
				{"type":"input_image","image_url":"data:image/png;base64,"}
			]},
			{"type":"input_image","image_url":"data:image/png;base64,"},
			{"type":"input_image","image_url":"data:image/png;base64,top-level-valid"}
		]
	}`), &reqBody))

	require.True(t, openai.SanitizeEmptyBase64InputImagesInOpenAIRequestBodyMap(reqBody))

	normalized, err := json.Marshal(reqBody)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"model":"gpt-5.4",
		"input":[
			{"role":"user","content":[
				{"type":"input_text","text":"Describe this"},
				{"type":"input_image","image_url":"data:image/png;base64,abc123"}
			]},
			{"type":"input_image","image_url":"data:image/png;base64,top-level-valid"}
		]
	}`, string(normalized))
}

func TestSanitizeEmptyBase64InputImagesInOpenAIBody(t *testing.T) {
	body, changed, err := openai.SanitizeEmptyBase64InputImagesInOpenAIBody([]byte(`{
		"model":"gpt-5.4",
		"stream":true,
		"input":[
			{"role":"user","content":[
				{"type":"input_text","text":"Describe this"},
				{"type":"input_image","image_url":"data:image/png;base64,"}
			]}
		]
	}`))
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{
		"model":"gpt-5.4",
		"stream":true,
		"input":[
			{"role":"user","content":[
				{"type":"input_text","text":"Describe this"}
			]}
		]
	}`, string(body))
}

// TestOpenAIGatewayService_CodexImageGenerationBridgeOverridePrecedence checks group protocol overrides,
// provider settings, and ignored legacy group flags.
func TestOpenAIGatewayService_CodexImageGenerationBridgeOverridePrecedence(t *testing.T) {
	for _, tt := range []struct {
		name     string
		global   bool
		group    string
		provider *bool
		legacy   bool
		want     bool
	}{
		{name: "全局开启", global: true, want: true},
		{name: "忽略旧分组开启值", legacy: true, want: false},
		{name: "忽略旧分组关闭值", global: true, want: true},
		{name: "提供商关闭覆盖全局", global: true, provider: routing.BoolOverridePtr(false), want: false},
		{name: "提供商开启覆盖全局", provider: routing.BoolOverridePtr(true), want: true},
		{name: "分组协议开启优先于提供商关闭", group: "enabled", provider: routing.BoolOverridePtr(false), want: true},
		{name: "分组协议关闭优先于提供商开启", group: "disabled", provider: routing.BoolOverridePtr(true), want: false},
		{name: "分组协议屏蔽优先于提供商开启", group: "block", provider: routing.BoolOverridePtr(true), want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newOpenAIImageGenerationControlTestService(&auxiliaryHTTPRecorder{})
			svc.ImageBridge.DefaultEnabled = tt.global
			provider := newOpenAIImageGenerationControlTestProvider()
			if tt.provider != nil {
				provider.Record.Extra = map[string]any{providercore.CodexImageGenerationBridgeKey: *tt.provider}
			}
			groupID := int64(4242)
			key := &apikey.APIKey{GroupID: &groupID, Group: &routing.Group{ID: groupID, ResponsesImagePolicy: tt.group, RoutingPolicy: routing.GroupRoutingPolicy{
				Enabled: true, FeaturesConfig: map[string]any{providercore.CodexImageGenerationBridgeKey: map[string]any{capability.PlatformOpenAI: tt.legacy}},
			}}}
			require.Equal(t, tt.want, svc.ImageBridge.Enabled(context.Background(), provider, key))
		})
	}
}

func TestApplyCodexOAuthTransform_PreservesLiteNamespaceToolChoice(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-terra",
		"input": []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type": "namespace",
				"name": "collaboration",
			}},
		}},
		"tool_choice": map[string]any{"type": "namespace", "name": "collaboration"},
	}

	gatewayprovider.ApplyCodexOAuthTransform(reqBody, true, false)

	require.Equal(t, map[string]any{"type": "namespace", "name": "collaboration"}, reqBody["tool_choice"])
}
