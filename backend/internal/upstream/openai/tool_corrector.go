package openai

import (
	"bytes"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// codexToolNameMapping maps Codex tool names and client aliases to OpenCode names.
var codexToolNameMapping = map[string]string{
	"apply_patch":  "edit",
	"applyPatch":   "edit",
	"update_plan":  "todowrite",
	"updatePlan":   "todowrite",
	"read_plan":    "todoread",
	"readPlan":     "todoread",
	"search_files": "grep",
	"searchFiles":  "grep",
	"list_files":   "glob",
	"listFiles":    "glob",
	"read_file":    "read",
	"readFile":     "read",
	"write_file":   "write",
	"writeFile":    "write",
	"execute_bash": "bash",
	"executeBash":  "bash",
	"exec_bash":    "bash",
	"execBash":     "bash",

	// Some clients output generic fetch names.
	"fetch":     "webfetch",
	"web_fetch": "webfetch",
	"webFetch":  "webfetch",
}

// ToolCorrectionStats counts tool renames and exposes the counts for JSON serialization.
type ToolCorrectionStats struct {
	TotalCorrected    int            `json:"total_corrected"`
	CorrectionsByTool map[string]int `json:"corrections_by_tool"`
}

// CodexToolCorrector adapts tool names and arguments to OpenCode and tracks renames.
type CodexToolCorrector struct {
	stats ToolCorrectionStats
	mu    sync.RWMutex
}

// NewCodexToolCorrector returns a corrector with empty rename counters.
func NewCodexToolCorrector() *CodexToolCorrector {
	return &CodexToolCorrector{
		stats: ToolCorrectionStats{
			CorrectionsByTool: make(map[string]int),
		},
	}
}

// CorrectToolCallsInSSEData adapts tool calls in an SSE data payload.
// The boolean reports whether the payload changed.
func (c *CodexToolCorrector) CorrectToolCallsInSSEData(data string) (string, bool) {
	if data == "" || data == "\n" {
		return data, false
	}
	correctedBytes, corrected := c.CorrectToolCallsInSSEBytes([]byte(data))
	if !corrected {
		return data, false
	}
	return string(correctedBytes), true
}

// CorrectToolCallsInSSEBytes adapts tool calls in a JSON payload.
// The boolean reports whether the payload changed; unchanged input is returned directly.
func (c *CodexToolCorrector) CorrectToolCallsInSSEBytes(data []byte) ([]byte, bool) {
	if len(bytes.TrimSpace(data)) == 0 {
		return data, false
	}
	if !mayContainToolCallPayload(data) {
		return data, false
	}
	if !gjson.ValidBytes(data) {
		return data, false
	}

	updated := data
	corrected := false
	collect := func(changed bool, next []byte) {
		if changed {
			corrected = true
			updated = next
		}
	}

	if next, changed := c.correctToolCallsArrayAtPath(updated, "tool_calls"); changed {
		collect(changed, next)
	}
	if next, changed := c.correctFunctionAtPath(updated, "function_call"); changed {
		collect(changed, next)
	}
	if next, changed := c.correctToolCallsArrayAtPath(updated, "delta.tool_calls"); changed {
		collect(changed, next)
	}
	if next, changed := c.correctFunctionAtPath(updated, "delta.function_call"); changed {
		collect(changed, next)
	}

	choicesCount := int(gjson.GetBytes(updated, "choices.#").Int())
	for i := range choicesCount {
		prefix := "choices." + strconv.Itoa(i)
		if next, changed := c.correctToolCallsArrayAtPath(updated, prefix+".message.tool_calls"); changed {
			collect(changed, next)
		}
		if next, changed := c.correctFunctionAtPath(updated, prefix+".message.function_call"); changed {
			collect(changed, next)
		}
		if next, changed := c.correctToolCallsArrayAtPath(updated, prefix+".delta.tool_calls"); changed {
			collect(changed, next)
		}
		if next, changed := c.correctFunctionAtPath(updated, prefix+".delta.function_call"); changed {
			collect(changed, next)
		}
	}

	if !corrected {
		return data, false
	}
	return updated, true
}

func mayContainToolCallPayload(data []byte) bool {
	// Skip JSON parsing for the common text and token events that have no tool fields.
	return bytes.Contains(data, []byte(`"tool_calls"`)) ||
		bytes.Contains(data, []byte(`"function_call"`)) ||
		bytes.Contains(data, []byte(`"function":{"name"`))
}

// correctToolCallsArrayAtPath adapts the names and arguments of tools at the given path.
func (c *CodexToolCorrector) correctToolCallsArrayAtPath(data []byte, toolCallsPath string) ([]byte, bool) {
	count := int(gjson.GetBytes(data, toolCallsPath+".#").Int())
	if count <= 0 {
		return data, false
	}
	updated := data
	corrected := false
	for i := range count {
		functionPath := toolCallsPath + "." + strconv.Itoa(i) + ".function"
		if next, changed := c.correctFunctionAtPath(updated, functionPath); changed {
			updated = next
			corrected = true
		}
	}
	return updated, corrected
}

// correctFunctionAtPath adapts one function's name and arguments at the given path.
func (c *CodexToolCorrector) correctFunctionAtPath(data []byte, functionPath string) ([]byte, bool) {
	namePath := functionPath + ".name"
	nameResult := gjson.GetBytes(data, namePath)
	if !nameResult.Exists() || nameResult.Type != gjson.String {
		return data, false
	}
	name := strings.TrimSpace(nameResult.Str)
	if name == "" {
		return data, false
	}
	updated := data
	corrected := false

	if correctName, found := codexToolNameMapping[name]; found {
		if next, err := sjson.SetBytes(updated, namePath, correctName); err == nil {
			updated = next
			c.recordCorrection(name, correctName)
			corrected = true
			name = correctName // Use the OpenCode name to select argument corrections.
		}
	}

	if next, changed := c.correctToolParametersAtPath(updated, functionPath+".arguments", name); changed {
		updated = next
		corrected = true
	}
	return updated, corrected
}

// correctToolParametersAtPath adapts arguments encoded as either an object or a JSON string.
func (c *CodexToolCorrector) correctToolParametersAtPath(data []byte, argumentsPath, toolName string) ([]byte, bool) {
	if toolName != "bash" && toolName != "edit" {
		return data, false
	}

	args := gjson.GetBytes(data, argumentsPath)
	if !args.Exists() {
		return data, false
	}

	switch args.Type {
	case gjson.String:
		argsJSON := strings.TrimSpace(args.Str)
		if !gjson.Valid(argsJSON) {
			return data, false
		}
		if !gjson.Parse(argsJSON).IsObject() {
			return data, false
		}
		nextArgsJSON, corrected := c.correctToolArgumentsJSON(argsJSON, toolName)
		if !corrected {
			return data, false
		}
		next, err := sjson.SetBytes(data, argumentsPath, nextArgsJSON)
		if err != nil {
			return data, false
		}
		return next, true
	case gjson.JSON:
		if !args.IsObject() || !gjson.Valid(args.Raw) {
			return data, false
		}
		nextArgsJSON, corrected := c.correctToolArgumentsJSON(args.Raw, toolName)
		if !corrected {
			return data, false
		}
		next, err := sjson.SetRawBytes(data, argumentsPath, []byte(nextArgsJSON))
		if err != nil {
			return data, false
		}
		return next, true
	default:
		return data, false
	}
}

// correctToolArgumentsJSON adapts a JSON argument object and reports whether it changed.
func (c *CodexToolCorrector) correctToolArgumentsJSON(argsJSON, toolName string) (string, bool) {
	if !gjson.Valid(argsJSON) {
		return argsJSON, false
	}
	if !gjson.Parse(argsJSON).IsObject() {
		return argsJSON, false
	}

	updated := argsJSON
	corrected := false

	switch toolName {
	case "bash":
		// OpenCode bash expects workdir; some clients send work_dir.
		if !gjson.Get(updated, "workdir").Exists() {
			if next, changed := moveJSONField(updated, "work_dir", "workdir"); changed {
				updated = next
				corrected = true
				logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Renamed 'work_dir' to 'workdir' in bash tool")
			}
		} else {
			if next, changed := deleteJSONField(updated, "work_dir"); changed {
				updated = next
				corrected = true
				logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Removed duplicate 'work_dir' parameter from bash tool")
			}
		}

	case "edit":
		// OpenCode edit expects camelCase keys such as filePath, oldString, and newString.
		if !gjson.Get(updated, "filePath").Exists() {
			if next, changed := moveJSONField(updated, "file_path", "filePath"); changed {
				updated = next
				corrected = true
				logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Renamed 'file_path' to 'filePath' in edit tool")
			} else if next, changed := moveJSONField(updated, "path", "filePath"); changed {
				updated = next
				corrected = true
				logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Renamed 'path' to 'filePath' in edit tool")
			} else if next, changed := moveJSONField(updated, "file", "filePath"); changed {
				updated = next
				corrected = true
				logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Renamed 'file' to 'filePath' in edit tool")
			}
		}

		if next, changed := moveJSONField(updated, "old_string", "oldString"); changed {
			updated = next
			corrected = true
			logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Renamed 'old_string' to 'oldString' in edit tool")
		}

		if next, changed := moveJSONField(updated, "new_string", "newString"); changed {
			updated = next
			corrected = true
			logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Renamed 'new_string' to 'newString' in edit tool")
		}

		if next, changed := moveJSONField(updated, "replace_all", "replaceAll"); changed {
			updated = next
			corrected = true
			logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Renamed 'replace_all' to 'replaceAll' in edit tool")
		}
	}
	return updated, corrected
}

func moveJSONField(input, from, to string) (string, bool) {
	if gjson.Get(input, to).Exists() {
		return input, false
	}
	src := gjson.Get(input, from)
	if !src.Exists() {
		return input, false
	}
	next, err := sjson.SetRaw(input, to, src.Raw)
	if err != nil {
		return input, false
	}
	next, err = sjson.Delete(next, from)
	if err != nil {
		return input, false
	}
	return next, true
}

func deleteJSONField(input, path string) (string, bool) {
	if !gjson.Get(input, path).Exists() {
		return input, false
	}
	next, err := sjson.Delete(input, path)
	if err != nil {
		return input, false
	}
	return next, true
}

// recordCorrection increments the total and per-rename counters under the statistics lock.
func (c *CodexToolCorrector) recordCorrection(from, to string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.TotalCorrected++
	key := fmt.Sprintf("%s->%s", from, to)
	c.stats.CorrectionsByTool[key]++

	logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Corrected tool call: %s -> %s (total: %d)",
		from, to, c.stats.TotalCorrected)
}

// GetStats returns a snapshot whose map can be modified independently of the corrector.
func (c *CodexToolCorrector) GetStats() ToolCorrectionStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	statsCopy := ToolCorrectionStats{
		TotalCorrected:    c.stats.TotalCorrected,
		CorrectionsByTool: make(map[string]int, len(c.stats.CorrectionsByTool)),
	}
	maps.Copy(statsCopy.CorrectionsByTool, c.stats.CorrectionsByTool)

	return statsCopy
}

// CorrectToolName returns the OpenCode name and whether a mapping exists.
func CorrectToolName(name string) (string, bool) {
	if correctName, found := codexToolNameMapping[name]; found {
		return correctName, true
	}
	return name, false
}

// CorrectResponseBody adapts tool calls and normalizes Responses function arguments.
func (c *CodexToolCorrector) CorrectResponseBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	updated := body
	if c != nil {
		if corrected, changed := c.CorrectToolCallsInSSEBytes(updated); changed {
			updated = corrected
		}
	}
	if normalized, changed := protocolopenai.NormalizeOpenAIResponsesFunctionCallArguments(updated); changed {
		updated = normalized
	}
	return updated
}
