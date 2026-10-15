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

// tool_corrector.go corrects Codex tool names and argument keys in Chat
// payloads while retaining their JSON representations.
// Codex-style Chat tool calls can use names and argument keys that differ from
// OpenCode declarations. Correction selects known function locations in a JSON
// payload, applies name mappings first, then uses the mapped tool name to
// choose bash or edit argument repairs.
//
// Arguments can be JSON strings or embedded objects; repairs retain that
// representation and copy raw field values when moving keys. Existing canonical
// keys take precedence, with bash also dropping its duplicate work_dir
// spelling. Each successful name rewrite updates synchronized statistics, while
// argument changes alone do not increase those counts. Failed individual JSON
// patches keep their preceding payload value, allowing other completed
// corrections to remain available without interpreting arbitrary text as a tool
// call.

// codexToolNameMapping defines exact Codex/client aliases consumed during response
// name repair; canonical OpenCode names and unknown aliases remain as supplied.
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

// ToolCorrectionStats counts successful tool-name rewrites for corrector
// snapshots and JSON output.
type ToolCorrectionStats struct {
	// TotalCorrected counts every successful rename, and CorrectionsByTool groups
	// those same operations by original-to-final name. Argument-only repairs do
	// not enter either counter; GetStats copies the map before returning it.
	TotalCorrected    int            `json:"total_corrected"`     // successful tool-name rewrite count
	CorrectionsByTool map[string]int `json:"corrections_by_tool"` // rewrites by original-to-final name
}

// CodexToolCorrector repairs known Chat tool calls and synchronizes their
// rename statistics.
type CodexToolCorrector struct {
	// mu protects stats during successful name recording and snapshot creation.
	// Payload repairs use caller input and static mappings independently of the
	// counters, which require initialized map storage before the first rename.
	stats ToolCorrectionStats // recorded successful rename counts
	mu    sync.RWMutex        // rename statistics synchronization mutex
}

// NewCodexToolCorrector creates a corrector with zero totals and initialized
// per-rename storage. Use this constructor before a mapped name is corrected;
// direct zero-value correctors lack the map required by rename recording.
func NewCodexToolCorrector() *CodexToolCorrector {
	return &CodexToolCorrector{
		stats: ToolCorrectionStats{
			CorrectionsByTool: make(map[string]int),
		},
	}
}

// CorrectToolCallsInSSEData repairs one JSON data payload and reports whether
// any supported field changed. Empty, newline-only, invalid, and unchanged
// inputs retain the supplied string; pass payload text after removing SSE
// framing.
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

// CorrectToolCallsInSSEBytes repairs supported Chat function locations and
// returns updated bytes with a change flag. Keep data immutable during the call
// and construct the corrector before name rewriting. Blank input, failed
// literal prechecks, invalid JSON, and unchanged payloads retain the input
// bytes.
//
// Top-level, delta, and choice message/delta call fields are visited in order.
// A successful name patch selects argument handling with the new tool name and
// records that rename. String arguments remain strings and object arguments
// remain objects. Patches are accepted individually, so a later optional repair
// failure keeps earlier successful changes; the routine does not validate the
// entire tool definition or parse arbitrary strings as calls.
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

// mayContainToolCallPayload checks literal tool-field markers before JSON
// parsing. It is a fast precheck with possible false positives and does not
// decode escaped key spellings; a positive result still needs validity and
// supported-path checks.
func mayContainToolCallPayload(data []byte) bool {
	// Ordinary text and token payloads need no tool JSON traversal. Literal
	// markers select candidates, while downstream validity and supported-path checks decide actual repair.
	return bytes.Contains(data, []byte(`"tool_calls"`)) ||
		bytes.Contains(data, []byte(`"function_call"`)) ||
		bytes.Contains(data, []byte(`"function":{"name"`))
}

// correctToolCallsArrayAtPath repairs function members of a nonempty array at
// toolCallsPath. Unusable or empty arrays retain data, and successful per-item
// patches update the working bytes while unchanged items remain intact.
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

// correctFunctionAtPath repairs a nonblank string name and compatible arguments
// at functionPath. Name lookup uses trimmed text; successful mapped-name
// patches record statistics before argument handling, while failed optional
// patches keep the available working payload.
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
		// A mapped parameter schema follows an accepted name patch, so a failed
		// rename cannot make argument correction target a tool identity absent from the payload.
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

// correctToolParametersAtPath repairs bash or edit arguments and reports
// accepted changes. Other tool names, absent arguments, invalid JSON, and
// nonobject argument values retain data. A configured corrector supplies the
// name mapping before this helper is called.
//
// JSON-string arguments are edited as decoded object text and written back as a
// string. Embedded objects use raw JSON replacement, retaining their wire
// representation and scalar values. A failed enclosing splice returns the
// supplied data even if an argument candidate was prepared; callers can retain
// separately completed name changes from their preceding phase.
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
		// Preserve string-encoded arguments when publishing the repaired object text;
		// failed splicing keeps the preceding payload so optional repair cannot discard response data.
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
		// Publish repaired object arguments as raw JSON, retaining their wire type;
		// failed splicing keeps the preceding payload instead of exposing a partial argument replacement.
		next, err := sjson.SetRawBytes(data, argumentsPath, []byte(nextArgsJSON))
		if err != nil {
			return data, false
		}
		return next, true
	default:
		return data, false
	}
}

// correctToolArgumentsJSON repairs a valid bash or edit argument object and
// reports change status. Invalid JSON, nonobjects, and unrelated tool names
// retain the supplied text. Canonical field presence takes precedence even when
// its value is null.
//
// Bash moves work_dir when workdir is absent and removes the duplicate alias
// when workdir already exists. Edit chooses file_path, then path, then file for
// a missing filePath, and separately moves old_string, new_string, and
// replace_all only when their destinations are absent. Field moves preserve raw
// values and successful independent repairs remain in the returned object.
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
		// Canonical workdir presence wins over the alias, even for null values.
		// Moving or removing work_dir keeps bash arguments on one recognized directory spelling.
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
		// Edit uses canonical camelCase fields. Choose the first available path alias
		// for missing filePath and retain existing destinations when moving the other recognized keys.
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

// moveJSONField moves a present raw value only when the destination is absent.
// Both setting and deletion must succeed before publishing the result; missing
// sources, existing destinations, and patch errors return the supplied text
// with false.
//
// Argument repair uses this operation to migrate canonical field aliases while
// retaining their raw JSON values. The destination is prepared privately, then
// source deletion completes the move. A failed deletion discards that prepared
// destination so this operation cannot report a copied field as a completed
// migration; other repairs already present in its input remain available.
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
	// Publish the move after source deletion succeeds, so a failed second patch
	// cannot leave callers with a reported change that duplicated the original field.
	next, err = sjson.Delete(next, from)
	if err != nil {
		return input, false
	}
	return next, true
}

// deleteJSONField removes one present path and returns its accepted JSON patch.
// Missing paths and failed deletion return the supplied text with false,
// allowing optional correction to retain the available response.
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

// recordCorrection increments total and original-to-final name counts under the
// statistics mutex. Use initialized map storage from NewCodexToolCorrector;
// logging runs under the same lock after both counters reflect the successful
// name patch.
func (c *CodexToolCorrector) recordCorrection(from, to string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.TotalCorrected++
	key := fmt.Sprintf("%s->%s", from, to)
	c.stats.CorrectionsByTool[key]++

	logger.LegacyPrintf("service.openai_tool_corrector", "[CodexToolCorrector] Corrected tool call: %s -> %s (total: %d)",
		from, to, c.stats.TotalCorrected)
}

// GetStats copies both rename counters under a read lock and returns
// independently mutable map storage. Counts describe successful name rewrites
// only, and subsequent corrections update later snapshots rather than this
// returned map.
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

// CorrectToolName returns the exact mapped OpenCode name and true for a
// registered alias. Unregistered names are returned unchanged with false; this
// direct lookup does not trim or case-fold input.
func CorrectToolName(name string) (string, bool) {
	if correctName, found := codexToolNameMapping[name]; found {
		return correctName, true
	}
	return name, false
}

// CorrectResponseBody applies optional corrector changes and normalizes
// Responses function-call arguments. Empty bodies retain their bytes, nil
// correctors skip name repair while protocol argument normalization still runs,
// and unchanged stages keep the available payload.
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
