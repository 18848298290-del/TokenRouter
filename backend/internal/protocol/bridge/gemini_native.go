package bridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// NativeGeminiUsage contains token counts extracted from a Gemini response.
type NativeGeminiUsage struct{ InputTokens, OutputTokens, CacheReadInputTokens, ImageOutputTokens int }

// NativeGeminiOptions contains caller-selected signature and thinking settings.
type NativeGeminiOptions struct {
	DummyThoughtSignature string
	Thinking              *GeminiThinkingConfig
}

// NativeGeminiRuntime supplies message and tool ID generators.
type NativeGeminiRuntime struct {
	MessageID func() string
	RandomHex func(int) string
}

func NativeConvertGeminiToClaudeMessage(runtime NativeGeminiRuntime, geminiResp map[string]any, originalModel string, rawData []byte, includeInlineData bool) (map[string]any, *NativeGeminiUsage) {
	usage := NativeExtractGeminiUsage(rawData)
	if usage == nil {
		usage = &NativeGeminiUsage{}
	}

	contentBlocks := make([]any, 0)
	sawToolUse := false
	if candidates, ok := geminiResp["candidates"].([]any); ok && len(candidates) > 0 {
		if cand, ok := candidates[0].(map[string]any); ok {
			if content, ok := cand["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok {
					for _, part := range parts {
						pm, ok := part.(map[string]any)
						if !ok {
							continue
						}
						if text, ok := pm["text"].(string); ok && text != "" {
							if thought, _ := pm["thought"].(bool); thought {
								contentBlocks = append(contentBlocks, map[string]any{
									"type":     "thinking",
									"thinking": text,
								})
							} else {
								contentBlocks = append(contentBlocks, map[string]any{
									"type": "text",
									"text": text,
								})
							}
						}
						if inlineData, ok := pm["inlineData"].(map[string]any); includeInlineData && ok {
							mimeType, _ := inlineData["mimeType"].(string)
							data, _ := inlineData["data"].(string)
							if NativeIsGeminiInlineImageMIMEType(mimeType) && NativeIsValidBase64(data) {
								contentBlocks = append(contentBlocks, map[string]any{
									"type": "text",
									"text": fmt.Sprintf("![image](data:%s;base64,%s)", mimeType, data),
								})
							}
						}
						if fc, ok := pm["functionCall"].(map[string]any); ok {
							name, _ := fc["name"].(string)
							if strings.TrimSpace(name) == "" {
								name = "tool"
							}
							args := fc["args"]
							sawToolUse = true
							contentBlocks = append(contentBlocks, map[string]any{
								"type":  "tool_use",
								"id":    "toolu_" + runtime.RandomHex(8),
								"name":  name,
								"input": args,
							})
						}
					}
				}
			}
		}
	}

	stopReason := NativeMapGeminiFinishReasonToClaudeStopReason(NativeExtractGeminiFinishReason(geminiResp))
	if sawToolUse {
		stopReason = "tool_use"
	}

	resp := map[string]any{
		"id":            runtime.MessageID(),
		"type":          "message",
		"role":          "assistant",
		"model":         originalModel,
		"content":       contentBlocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
		},
	}

	return resp, usage
}

func NativeIsGeminiInlineImageMIMEType(mimeType string) bool {
	switch mimeType {
	case "image/gif", "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func NativeIsValidBase64(data string) bool {
	if data == "" {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(data)
	return err == nil
}

func NativeExtractGeminiUsage(data []byte) *NativeGeminiUsage {
	usage := gjson.GetBytes(data, "usageMetadata")
	if !usage.Exists() {
		return nil
	}
	prompt := int(usage.Get("promptTokenCount").Int())
	cand := int(usage.Get("candidatesTokenCount").Int())
	cached := int(usage.Get("cachedContentTokenCount").Int())
	thoughts := int(usage.Get("thoughtsTokenCount").Int())

	// Read the IMAGE modality count separately from total candidate tokens.
	imageTokens := 0
	candidateDetails := usage.Get("candidatesTokensDetails")
	if candidateDetails.Exists() {
		candidateDetails.ForEach(func(_, detail gjson.Result) bool {
			if detail.Get("modality").String() == "IMAGE" {
				imageTokens = int(detail.Get("tokenCount").Int())
				return false
			}
			return true
		})
	}

	// Gemini promptTokenCount includes cached tokens; Claude input_tokens excludes them.
	return &NativeGeminiUsage{
		InputTokens:          prompt - cached,
		OutputTokens:         cand + thoughts,
		CacheReadInputTokens: cached,
		ImageOutputTokens:    imageTokens,
	}
}

func NativeAsInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

func NativeEnsureGeminiFunctionCallThoughtSignatures(options NativeGeminiOptions, body []byte) []byte {
	// Skip JSON decoding when there are no function calls.
	if !bytes.Contains(body, []byte(`"functionCall"`)) {
		return body
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	contentsAny, ok := payload["contents"].([]any)
	if !ok || len(contentsAny) == 0 {
		return body
	}

	modified := false
	for _, c := range contentsAny {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		partsAny, ok := cm["parts"].([]any)
		if !ok || len(partsAny) == 0 {
			continue
		}
		for _, p := range partsAny {
			pm, ok := p.(map[string]any)
			if !ok || pm == nil {
				continue
			}
			if fc, ok := pm["functionCall"].(map[string]any); !ok || fc == nil {
				continue
			}
			ts, _ := pm["thoughtSignature"].(string)
			if strings.TrimSpace(ts) == "" {
				pm["thoughtSignature"] = options.DummyThoughtSignature
				modified = true
			}
		}
	}

	if !modified {
		return body
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return b
}

func NativeExtractGeminiFinishReason(geminiResp map[string]any) string {
	if candidates, ok := geminiResp["candidates"].([]any); ok && len(candidates) > 0 {
		if cand, ok := candidates[0].(map[string]any); ok {
			if fr, ok := cand["finishReason"].(string); ok {
				return fr
			}
		}
	}
	return ""
}

func NativeExtractGeminiParts(geminiResp map[string]any) []map[string]any {
	if candidates, ok := geminiResp["candidates"].([]any); ok && len(candidates) > 0 {
		if cand, ok := candidates[0].(map[string]any); ok {
			if content, ok := cand["content"].(map[string]any); ok {
				if partsAny, ok := content["parts"].([]any); ok && len(partsAny) > 0 {
					out := make([]map[string]any, 0, len(partsAny))
					for _, p := range partsAny {
						pm, ok := p.(map[string]any)
						if !ok {
							continue
						}
						out = append(out, pm)
					}
					return out
				}
			}
		}
	}
	return nil
}

func NativeComputeGeminiTextDelta(seen, incoming string) (delta, newSeen string) {
	incoming = strings.TrimSuffix(incoming, "\u0000")
	if incoming == "" {
		return "", seen
	}

	// Cumulative mode: incoming contains full text so far.
	if after, ok := strings.CutPrefix(incoming, seen); ok {
		return after, incoming
	}
	// Duplicate/rewind: ignore.
	if strings.HasPrefix(seen, incoming) {
		return "", seen
	}
	// Delta mode: treat incoming as incremental chunk.
	return incoming, seen + incoming
}

func NativeMapGeminiFinishReasonToClaudeStopReason(finishReason string) string {
	switch strings.ToUpper(strings.TrimSpace(finishReason)) {
	case "MAX_TOKENS":
		return "max_tokens"
	case "STOP":
		return "end_turn"
	default:
		return "end_turn"
	}
}

func NativeConvertClaudeMessagesToGeminiGenerateContent(options NativeGeminiOptions, body []byte) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	toolUseIDToName := make(map[string]string)

	systemText := NativeExtractClaudeSystemText(req["system"])
	contents, err := NativeConvertClaudeMessagesToGeminiContents(options, req["messages"], toolUseIDToName)
	if err != nil {
		return nil, err
	}

	out := make(map[string]any)
	if systemText != "" {
		out["systemInstruction"] = map[string]any{
			"parts": []any{map[string]any{"text": systemText}},
		}
	}
	out["contents"] = contents

	if tools := NativeConvertClaudeToolsToGeminiTools(req["tools"]); tools != nil {
		out["tools"] = tools
	}

	generationConfig := NativeConvertClaudeGenerationConfig(req)
	if options.Thinking != nil {
		if generationConfig == nil {
			generationConfig = map[string]any{}
		}
		generationConfig["thinkingConfig"] = options.Thinking
	}
	if generationConfig != nil {
		out["generationConfig"] = generationConfig
	}

	NativeStripGeminiFunctionIDs(out)
	return json.Marshal(out)
}

func NativeStripGeminiFunctionIDs(req map[string]any) {
	// Some upstreams reject id fields in functionCall and functionResponse.
	contents, ok := req["contents"].([]any)
	if !ok {
		return
	}
	for _, c := range contents {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		contentParts, ok := cm["parts"].([]any)
		if !ok {
			continue
		}
		for _, p := range contentParts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if fc, ok := pm["functionCall"].(map[string]any); ok && fc != nil {
				delete(fc, "id")
			}
			if fr, ok := pm["functionResponse"].(map[string]any); ok && fr != nil {
				delete(fr, "id")
			}
		}
	}
}

func NativeExtractClaudeSystemText(system any) string {
	switch v := system.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, p := range v {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := pm["type"].(string); t != "text" {
				continue
			}
			if text, ok := pm["text"].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func NativeConvertClaudeMessagesToGeminiContents(options NativeGeminiOptions, messages any, toolUseIDToName map[string]string) ([]any, error) {
	arr, ok := messages.([]any)
	if !ok {
		return nil, errors.New("messages must be an array")
	}

	out := make([]any, 0, len(arr))
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))
		gRole := "user"
		if role == "assistant" {
			gRole = "model"
		}

		parts := make([]any, 0)
		switch content := mm["content"].(type) {
		case string:
			// Preserve whitespace in string content.
			parts = append(parts, map[string]any{"text": content})
		case []any:
			// Leave single-block whitespace for upstream validation.
			singleBlock := len(content) == 1

			for _, block := range content {
				bm, ok := block.(map[string]any)
				if !ok {
					continue
				}
				bt, _ := bm["type"].(string)
				switch bt {
				case "text":
					if text, ok := bm["text"].(string); ok {
						// Drop blank text only when the message has multiple blocks.
						if singleBlock || strings.TrimSpace(text) != "" {
							parts = append(parts, map[string]any{"text": text})
						}
					}
				case "tool_use":
					id, _ := bm["id"].(string)
					name, _ := bm["name"].(string)
					if strings.TrimSpace(id) != "" && strings.TrimSpace(name) != "" {
						toolUseIDToName[id] = name
					}
					signature, _ := bm["signature"].(string)
					signature = strings.TrimSpace(signature)
					if signature == "" {
						signature = options.DummyThoughtSignature
					}
					parts = append(parts, map[string]any{
						"thoughtSignature": signature,
						"functionCall": map[string]any{
							"name": name,
							"args": bm["input"],
						},
					})
				case "tool_result":
					toolUseID, _ := bm["tool_use_id"].(string)
					name := toolUseIDToName[toolUseID]
					if name == "" {
						name = "tool"
					}
					parts = append(parts, map[string]any{
						"functionResponse": map[string]any{
							"name": name,
							"response": map[string]any{
								"content": NativeExtractClaudeContentText(bm["content"]),
							},
						},
					})
				case "image":
					if src, ok := bm["source"].(map[string]any); ok {
						if srcType, _ := src["type"].(string); srcType == "base64" {
							mediaType, _ := src["media_type"].(string)
							data, _ := src["data"].(string)
							if mediaType != "" && data != "" {
								parts = append(parts, map[string]any{
									"inlineData": map[string]any{
										"mimeType": mediaType,
										"data":     data,
									},
								})
							}
						}
					}
				default:
					// Serialize unknown blocks as text.
					if b, err := json.Marshal(bm); err == nil {
						parts = append(parts, map[string]any{"text": string(b)})
					}
				}
			}
		default:
			// Unsupported content shapes contribute no parts.
		}

		out = append(out, map[string]any{
			"role":  gRole,
			"parts": parts,
		})
	}
	return out, nil
}

func NativeExtractClaudeContentText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var sb strings.Builder
		for _, part := range t {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if pm["type"] == "text" {
				if text, ok := pm["text"].(string); ok {
					_, _ = sb.WriteString(text)
				}
			}
		}
		return sb.String()
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func NativeConvertClaudeToolsToGeminiTools(tools any) []any {
	arr, ok := tools.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}

	hasWebSearch := false
	funcDecls := make([]any, 0, len(arr))
	for _, t := range arr {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if NativeIsClaudeWebSearchToolMap(tm) {
			hasWebSearch = true
			continue
		}

		var name, desc string
		var params any

		toolType, _ := tm["type"].(string)
		if toolType == "custom" {
			// Custom tools nest description and input_schema under custom.
			custom, ok := tm["custom"].(map[string]any)
			if !ok {
				continue
			}
			name, _ = tm["name"].(string)
			desc, _ = custom["description"].(string)
			params = custom["input_schema"]
		} else {
			name, _ = tm["name"].(string)
			desc, _ = tm["description"].(string)
			params = tm["input_schema"]
		}

		if name == "" {
			continue
		}

		if params == nil {
			params = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		cleanedParams := NativeCleanToolSchema(params)

		funcDecls = append(funcDecls, map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  cleanedParams,
		})
	}

	out := make([]any, 0, 2)
	if len(funcDecls) > 0 {
		out = append(out, map[string]any{
			"functionDeclarations": funcDecls,
		})
	}
	if hasWebSearch {
		out = append(out, map[string]any{
			"googleSearch": map[string]any{},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func NativeNormalizeGeminiRequestForAIStudio(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) == 0 {
		return body
	}

	modified := false
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		googleSearch, ok := tool["googleSearch"]
		if !ok {
			continue
		}
		if _, exists := tool["google_search"]; exists {
			continue
		}
		tool["google_search"] = googleSearch
		delete(tool, "googleSearch")
		modified = true
	}

	if !modified {
		return body
	}

	normalized, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return normalized
}

func NativeIsClaudeWebSearchToolMap(tool map[string]any) bool {
	toolType, _ := tool["type"].(string)
	// Client functions named web_search remain client tools, as used by Hermes.
	// Promote a tool to googleSearch only when its type declares server-side search.
	return strings.HasPrefix(toolType, "web_search") || toolType == "google_search"
}

// NativeCleanToolSchema returns a copy with unsupported Gemini schema fields removed.
func NativeCleanToolSchema(schema any) any {
	if schema == nil {
		return nil
	}

	switch v := schema.(type) {
	case map[string]any:
		cleaned := make(map[string]any)
		for key, value := range v {
			if key == "$schema" || key == "$id" || key == "$ref" ||
				key == "$defs" || key == "definitions" ||
				key == "additionalProperties" || key == "patternProperties" || key == "minLength" ||
				key == "maxLength" || key == "minItems" || key == "maxItems" || key == "exclusiveMinimum" ||
				key == "deprecated" {
				continue
			}
			cleaned[key] = NativeCleanToolSchema(value)
		}
		if enum, ok := cleaned["enum"].([]any); ok {
			if normalized, ok := NativeNormalizeGeminiEnum(enum); ok {
				cleaned["enum"] = normalized
			} else {
				delete(cleaned, "enum")
			}
		}
		// Gemini uses uppercase type names and a single non-null type.
		if typeVal, ok := cleaned["type"].(string); ok {
			cleaned["type"] = strings.ToUpper(typeVal)
		} else if typeValues, ok := cleaned["type"].([]any); ok {
			for _, typeValue := range typeValues {
				typeName, ok := typeValue.(string)
				if ok && !strings.EqualFold(typeName, "null") {
					cleaned["type"] = strings.ToUpper(typeName)
					break
				}
			}
			if _, ok := cleaned["type"].([]any); ok {
				delete(cleaned, "type")
			}
		}
		if cleaned["type"] == "INTEGER" {
			if minimum, ok := NativeIncrementIntegralSchemaBound(v["exclusiveMinimum"]); ok {
				if existing, exists := cleaned["minimum"]; !exists || NativeSchemaNumberLess(existing, minimum) {
					cleaned["minimum"] = minimum
				}
			}
		}
		return cleaned
	case []any:
		cleaned := make([]any, len(v))
		for i, item := range v {
			cleaned[i] = NativeCleanToolSchema(item)
		}
		return cleaned
	default:
		return v
	}
}

// NativeNormalizeGeminiEnum converts scalar enum values to strings and rejects compound values.
func NativeNormalizeGeminiEnum(values []any) ([]any, bool) {
	normalized := make([]any, len(values))
	for i, value := range values {
		if stringValue, ok := value.(string); ok {
			normalized[i] = stringValue
			continue
		}

		switch value.(type) {
		case nil, bool, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, false
			}
			normalized[i] = string(encoded)
		default:
			return nil, false
		}
	}
	return normalized, true
}

// NativeIncrementIntegralSchemaBound converts an integer exclusive bound to an inclusive bound.
func NativeIncrementIntegralSchemaBound(value any) (any, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v+1 <= v {
			return nil, false
		}
		return v + 1, true
	case int:
		if v == math.MaxInt {
			return nil, false
		}
		return v + 1, true
	case int64:
		if v == math.MaxInt64 {
			return nil, false
		}
		return v + 1, true
	case json.Number:
		i, err := v.Int64()
		if err != nil || i == math.MaxInt64 {
			return nil, false
		}
		return json.Number(fmt.Sprintf("%d", i+1)), true
	default:
		return nil, false
	}
}

// NativeSchemaNumberLess compares the finite JSON numeric forms supported by the cleaner.
func NativeSchemaNumberLess(left, right any) bool {
	leftNumber, leftOK := NativeSchemaNumberFloat64(left)
	rightNumber, rightOK := NativeSchemaNumberFloat64(right)
	return leftOK && rightOK && leftNumber < rightNumber
}

// NativeSchemaNumberFloat64 converts a JSON schema number to a finite float64.
func NativeSchemaNumberFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	default:
		return 0, false
	}
}

func NativeConvertClaudeGenerationConfig(req map[string]any) map[string]any {
	out := make(map[string]any)
	if mt, ok := NativeAsInt(req["max_tokens"]); ok && mt > 0 {
		out["maxOutputTokens"] = mt
	}
	if temp, ok := req["temperature"].(float64); ok {
		out["temperature"] = temp
	}
	if topP, ok := req["top_p"].(float64); ok {
		out["topP"] = topP
	}
	if stopSeq, ok := req["stop_sequences"].([]any); ok && len(stopSeq) > 0 {
		out["stopSequences"] = stopSeq
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NativePickGeminiCollectResult prefers a response with content parts, then the last response.
func NativePickGeminiCollectResult(last map[string]any, lastWithParts map[string]any) map[string]any {
	if lastWithParts != nil {
		return lastWithParts
	}
	if last != nil {
		return last
	}
	return map[string]any{}
}

// NativeUsageProjection converts Gemini counts to shared usage; nil input returns nil.
func NativeUsageProjection(usage *NativeGeminiUsage) *protocol.TokenUsage {
	if usage == nil {
		return nil
	}
	return &protocol.TokenUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadInputTokens: usage.CacheReadInputTokens, ImageOutputTokens: usage.ImageOutputTokens}
}
