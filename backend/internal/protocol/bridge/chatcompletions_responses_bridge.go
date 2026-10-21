package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// chatcompletions_responses_bridge.go converts Responses requests to Chat and
// restores Responses identities in Chat replies.
// A Responses call can carry a namespace, freeform custom input, or client
// tool-search identity that the Chat function shape does not retain directly.
// Request conversion collects effective declarations, lowers those call
// shapes, and stores the names needed for response restoration. Reasoning
// plaintext can accompany later tool calls, while tool-result images move to
// attributed user content after their correlated replies.
//
// Stream restoration waits for names when classification depends on
// declarations, then retains the chosen kind and namespace for the announced
// item. Argument fragments may arrive before names, so ordinary buffered
// prefixes replay at announcement and custom/search input waits for
// completion. The state keeps one reasoning and one message lifecycle plus
// indexed calls; final snapshots regroup content and regenerate some item IDs.
// History repair, number-preserving media rewriting, namespace collision
// checks, and caller-invoked argument validation each protect a narrower rule
// rather than establishing complete wire validity.

const (
	// toolOutputMediaMarker and toolOutputMediaAttribution connect text markers
	// to extracted images, retaining a call-ID label when normalization emits the
	// user media batch.
	toolOutputMediaMarker      = "[Tool output media moved to the following user message]"
	toolOutputMediaAttribution = "[Tool output media for call %s]"

	// customToolInputSchema supplies the string wrapper used by lowered custom
	// declarations and replayed input. Response restoration unwraps that field;
	// the emitted function shape has no original grammar metadata.
	customToolInputSchema = `{"type":"object","properties":{"input":{"type":"string","description":"The raw input for this tool, passed through verbatim."}},"required":["input"]}`

	// toolSearchProxyName and toolSearchProxySchema represent client discovery
	// through a named function with required query and optional limit. Response
	// conversion uses the reserved name to recover the tool_search_call kind.
	toolSearchProxyName = "tool_search"

	toolSearchProxySchema = `{"type":"object","properties":{"query":{"type":"string","description":"Search query for tools or connectors to load."},"limit":{"type":"integer","description":"Maximum number of tool groups to return."}},"required":["query"]}`

	// chatToolNameMaxLen bounds derived function names in bytes, including the
	// hash suffix reserved when the full namespace/name pair exceeds that budget.
	chatToolNameMaxLen = 64
)

// toolOutputMediaByCallID associates selected output images with call correlation.
// Later outputs replace each entry; normalization reads images for retained calls
// only, and supplied part slices may share image pointers during encoding.
type toolOutputMediaByCallID map[string][]ChatContentPart

// ResponsesToChatOptions provides a synchronous optional plaintext lookup for
// history whose selected reasoning text is absent.
type ResponsesToChatOptions struct {
	// ReasoningContentByID runs synchronously for absent selected plaintext and
	// nonempty ID. Nil disables lookup; an empty return leaves prior pending
	// reasoning unchanged. Conversion waits for the callback, and any panic propagates.
	ReasoningContentByID func(itemID string) string
	// toolHistory belongs to one conversion. Known calls share declaration names;
	// missing declarations receive replay names that cannot authorize selectors.
	toolHistory responsesToolHistoryNames
}

// NamespacedToolName identifies a child tool by namespace and unqualified name.
type NamespacedToolName struct {
	// Namespace and Name retain one original declaration pair independently of
	// its flat label. Call restoration reads both, since hashing or separators
	// in either component prevent recovery by splitting that upstream label.
	Namespace string // original declared tool namespace
	Name      string // original unqualified child name
}

// ChatCompletionsToResponsesStreamState tracks conversion of Chat SSE chunks
// into Responses SSE events.
type ChatCompletionsToResponsesStreamState struct {
	// ResponseID, Model, Created, and ServiceTier feed response snapshots.
	// Nonempty chunk ID/tier replaces retained values, chosen nonempty model
	// wins, and creation time stays at the constructor observation.
	ResponseID  string // current response identity spelling
	Model       string // selected client response model
	Created     int64  // Unix seconds
	ServiceTier string // latest nonempty observed tier
	// SequenceNumber increments for every constructed event. Callers serialize
	// access so all item, part, and terminal events share this emission counter.
	SequenceNumber int // next constructed sequence number
	// CreatedSent suppresses repeated created events but not metadata updates
	// in later chunks. Those updates can reach the final snapshot.
	CreatedSent bool // response creation already produced
	// CompletedSent suppresses finalization only. Chunk processing still mutates
	// state afterward, so callers end input before finalizing this stream.
	CompletedSent bool // terminal construction already selected

	// nextOutputIndex allocates positions in first-opening order across reasoning,
	// message, and calls. Lifecycle events reuse those positions, while terminal
	// output is regrouped separately rather than built by those indexes.
	nextOutputIndex int // next lifecycle output position

	// ReasoningItemID, ReasoningIndex, and ReasoningOpen identify the sole
	// reasoning lifecycle. Text or a new call closes it before its next item
	// opens; the full reasoning Builder remains available to terminal output.
	ReasoningItemID string // announced reasoning item identity
	ReasoningIndex  int    // reasoning lifecycle output position
	ReasoningOpen   bool   // reasoning lifecycle still active
	// ReasoningDone prevents another reasoning item after closure. Later
	// reasoning handling can still append/emit with the retained closed identity.
	ReasoningDone bool // reasoning lifecycle already closed

	// MessageItemID, MessageIndex, and TextPartOpen describe one message and
	// its single text part at content index zero. Message completion does not
	// clear them, and interleaved calls do not allocate another text message.
	MessageItemID string // announced message item identity
	MessageIndex  int    // message lifecycle output position
	TextPartOpen  bool   // text part already announced

	// Text and Reasoning retain independent fragment prefixes, with optional
	// reasoning-only text fallback at finalization. Keep this state by pointer;
	// copying a written Builder before later writes triggers its copy check.
	Text      strings.Builder // accumulated visible text bytes
	Reasoning strings.Builder // accumulated reasoning summary bytes

	// ToolCalls, ToolItemIDs, and ToolOutputIndex share exact source tool keys
	// across choices; absent index selects zero. Metadata can change after
	// announcement. Completion loops require contiguous integer keys from zero;
	// lifecycle item IDs differ from newly generated terminal tool IDs.
	ToolCalls       map[int]*ChatToolCall // accumulated source call records
	ToolItemIDs     map[int]string        // stable lifecycle item identities
	ToolOutputIndex map[int]int           // allocated lifecycle output positions

	// CustomTools, FunctionTools, ToolSearchDeclared, and NamespaceTools are
	// caller-supplied classification inputs. Set them before chunks and retain
	// shared maps unchanged through completion. Explicit functions win custom
	// recognition; custom recognition precedes search, then namespace restoration.
	// CustomTools direct lookup reads bool, while inferred aliases range all keys.
	CustomTools        map[string]bool               // declared client custom names
	FunctionTools      map[string]bool               // ordinary and default-scoped function names
	ToolSearchDeclared bool                          // client search proxy enabled
	NamespaceTools     map[string]NamespacedToolName // flat-label client identity pairs

	// toolIsCustom, toolIsToolSearch, and toolNamespace capture classification
	// at announcement, so completion reuses kind and namespace despite later
	// metadata updates. Custom names can still be rechecked through current maps.
	toolIsCustom     map[int]bool               // captured custom call classification
	toolIsToolSearch map[int]bool               // captured client-search classification
	toolNamespace    map[int]NamespacedToolName // captured namespace identity pair
	// toolAnnounced suppresses another added event. Declaration-dependent
	// classification waits for name unless forced, then replays ordinary buffered
	// arguments.
	toolAnnounced map[int]bool // call item already announced

	// FinishReason keeps the last nonempty choice observation. Exact length
	// changes nested terminal status/details, while event label stays
	// response.completed.
	FinishReason string // selected nonempty finish evidence
	// Usage is replaced by each present chunk usage, including zero values.
	// Terminal response shares this pointer; consumers preserve it during delivery.
	Usage *ResponsesUsage // latest converted usage observation
}

// ResponsesToChatCompletionsRequestWithOptions converts a request with an
// optional synchronous reasoning lookup. Callers that restore Responses output
// use ResponsesToChatCompletionsRequestWithMapping to retain the matching tool
// identities; this request-only entry discards that restoration metadata.
func ResponsesToChatCompletionsRequestWithOptions(req *ResponsesRequest, opts *ResponsesToChatOptions) (*ChatCompletionsRequest, error) {
	converted, _, err := ResponsesToChatCompletionsRequestWithMapping(req, opts)
	return converted, err
}

// ResponsesToChatCompletionsRequestWithMapping converts a Responses request
// and returns the tool identities required to restore its matching response.
// Effective declarations are resolved before input conversion; errors return
// no request, preventing partially prepared identities from being forwarded.
//
// The caller keeps the returned mapping with this request through response
// consumption. Known history, tool choice, and declaration lowering share
// aliases, including default-scope and discovered plain-function identities.
// Missing historical declarations use a separate request-local lookup, keeping
// synthesized replay names out of selectors and response restoration.
// Options are copied before attaching conversion state, leaving caller-owned
// lookup settings unchanged across independent conversions and retries.
func ResponsesToChatCompletionsRequestWithMapping(req *ResponsesRequest, opts *ResponsesToChatOptions) (*ChatCompletionsRequest, ResponsesClientToolMapping, error) {
	if req == nil {
		return nil, ResponsesClientToolMapping{}, fmt.Errorf("responses request is nil")
	}
	effectiveTools, err := EffectiveResponsesTools(req)
	if err != nil {
		return nil, ResponsesClientToolMapping{}, err
	}
	mapping, err := responsesToolIdentityMapping(effectiveTools, req.Input)
	if err != nil {
		return nil, ResponsesClientToolMapping{}, err
	}
	mapping.CustomTools = CustomToolNames(effectiveTools)
	mapping.ToolSearch = HasToolSearchTool(effectiveTools)
	historyNames, err := newResponsesToolHistoryNames(req.Input, mapping)
	if err != nil {
		return nil, ResponsesClientToolMapping{}, err
	}
	var conversionOptions ResponsesToChatOptions
	if opts != nil {
		conversionOptions = *opts
	}
	conversionOptions.toolHistory = historyNames

	messages, err := responsesInputToChatMessagesWithOptions(req.Instructions, req.Input, &conversionOptions)
	if err != nil {
		return nil, ResponsesClientToolMapping{}, err
	}

	out := &ChatCompletionsRequest{
		Model:               req.Model,
		Messages:            messages,
		MaxCompletionTokens: req.MaxOutputTokens,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		Stream:              req.Stream,
		ServiceTier:         req.ServiceTier,
		ParallelToolCalls:   req.ParallelToolCalls,
	}
	if req.Reasoning != nil {
		out.ReasoningEffort = req.Reasoning.Effort
	}
	if len(effectiveTools) > 0 {
		tools, err := responsesToolsToChatTools(effectiveTools)
		if err != nil {
			return nil, ResponsesClientToolMapping{}, err
		}
		out.Tools = tools
	}
	// A choice needs a retained target after declaration lowering. Omit it when
	// the list is empty and let the selector filter dropped names before
	// producing Chat selection.
	if len(out.Tools) > 0 && len(req.ToolChoice) > 0 {
		declared := make(map[string]bool, len(out.Tools))
		for _, tool := range out.Tools {
			if tool.Function != nil {
				declared[tool.Function.Name] = true
			}
			if strings.EqualFold(strings.TrimSpace(tool.Type), "x_search") {
				declared["x_search"] = true
			}
		}
		choice, err := responsesMappedToolChoice(req.ToolChoice, &mapping)
		if err != nil {
			return nil, ResponsesClientToolMapping{}, err
		}
		if tc := responsesToolChoiceToChatToolChoice(choice, declared); len(tc) > 0 {
			out.ToolChoice = tc
		}
	}
	if req.Text != nil {
		out.ResponseFormat = responsesTextFormatToChatResponseFormat(req.Text.Format)
	}

	return out, mapping, nil
}

// EffectiveResponsesTools returns top-level declarations followed by exact
// additional_tools declarations and admitted client-search discoveries. Nil
// request returns nil. Nonarray/absent/null input returns the shallow-copied
// top-level slice, while array/item/declaration decoding errors abort the
// result.
//
// Discovery admission uses the combined static list and the shared
// identity/conflict rules, requiring a search declaration and eligible status.
// The promotion path serializes typed tools then decodes generic JSON with
// float64 numbers; comparison encoding and promoted schema numbers can
// therefore change large values. Original static values retain their raw
// schema storage. Returned tools are not fully schema-validated, and discovery
// conflicts return no effective list.
func EffectiveResponsesTools(req *ResponsesRequest) ([]ResponsesTool, error) {
	if req == nil {
		return nil, nil
	}

	tools := append([]ResponsesTool(nil), req.Tools...)
	inputRaw := bytesTrimSpace(req.Input)
	if len(inputRaw) == 0 || string(inputRaw) == "null" || inputRaw[0] != '[' {
		return tools, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return nil, fmt.Errorf("parse responses input for additional tools: %w", err)
	}
	for _, raw := range items {
		raw = bytesTrimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var discriminator struct {
			Type string `json:"type"` // decoded input item kind
		}
		if err := json.Unmarshal(raw, &discriminator); err != nil {
			return nil, fmt.Errorf("parse responses additional tools item: %w", err)
		}
		if discriminator.Type != "additional_tools" {
			continue
		}
		var item struct {
			// Tools is decoded only for exact additional_tools items. Other input
			// objects can carry an unrelated tools value without entering this decoder.
			Tools []ResponsesTool `json:"tools"` // additional declared tool values
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("parse responses additional tools item: %w", err)
		}
		tools = append(tools, item.Tools...)
	}

	// Eligible search history can add declarations after the static list. Reuse
	// shared identity admission, including absent/completed status; generic
	// decoding here uses float64, so promoted numeric schemas can change
	// representation.
	toolsRaw, err := json.Marshal(tools)
	if err != nil {
		return nil, fmt.Errorf("encode responses tools for discovery promotion: %w", err)
	}
	var rawTools, rawInput []any
	if err := json.Unmarshal(toolsRaw, &rawTools); err != nil {
		return nil, fmt.Errorf("decode responses tools for discovery promotion: %w", err)
	}
	if err := json.Unmarshal(inputRaw, &rawInput); err != nil {
		return nil, fmt.Errorf("parse responses input for discovery promotion: %w", err)
	}
	promoted, err := promotedResponsesToolSearchDiscoveries(rawTools, rawInput)
	if err != nil {
		return nil, err
	}
	if len(promoted) > 0 {
		promotedRaw, err := json.Marshal(promoted)
		if err != nil {
			return nil, fmt.Errorf("encode promoted responses tools: %w", err)
		}
		var discovered []ResponsesTool
		if err := json.Unmarshal(promotedRaw, &discovered); err != nil {
			return nil, fmt.Errorf("decode promoted responses tools: %w", err)
		}
		tools = append(tools, discovered...)
	}
	return tools, nil
}

// CustomToolNames collects exact custom kinds with nonempty names into true-
// valued lookup entries. Names are not trimmed, and no matches return nil.
// Response classification uses these client names directly and can infer
// namespace aliases separately.
func CustomToolNames(tools []ResponsesTool) map[string]bool {
	var out map[string]bool
	for _, tool := range tools {
		if tool.Type == "custom" && tool.Name != "" {
			if out == nil {
				out = make(map[string]bool)
			}
			out[tool.Name] = true
		}
	}
	return out
}

// FunctionToolNames records exact top-level function kinds with nonempty names
// and returns nil when none qualify. Names retain source spelling. Classifiers
// use this map to give an explicitly declared function priority over custom-
// name recognition.
func FunctionToolNames(tools []ResponsesTool) map[string]bool {
	var out map[string]bool
	for _, tool := range tools {
		if tool.Type == "function" && tool.Name != "" {
			if out == nil {
				out = make(map[string]bool)
			}
			out[tool.Name] = true
		}
	}
	return out
}

// NamespaceToolNames maps derived flat labels to original namespace/child
// pairs. Exact namespace kinds and nonempty names qualify; nonempty Tools wins
// over Children, and only named exact function children contribute. No matches
// return nil and names are not trimmed.
//
// Derived labels can be shortened with hashes or contain separators in either
// source component, so splitting does not recover identity. Repeated flat keys
// overwrite this map; callers first run declaration collision validation if
// they rely on unique restoration. The returned pairs retain full names
// independently of the bounded upstream label.
func NamespaceToolNames(tools []ResponsesTool) map[string]NamespacedToolName {
	var out map[string]NamespacedToolName
	for _, tool := range tools {
		if tool.Type != "namespace" || tool.Name == "" {
			continue
		}
		// The primary list wins only when nonempty. Use the children alias when
		// Tools contributes nothing so either supported declaration layout can
		// produce identities.
		children := tool.Tools
		if len(children) == 0 {
			children = tool.Children
		}
		for _, child := range children {
			if child.Type != "function" || child.Name == "" {
				continue
			}
			if out == nil {
				out = make(map[string]NamespacedToolName)
			}
			out[flattenNamespaceToolName(tool.Name, child.Name)] = NamespacedToolName{
				Namespace: tool.Name,
				Name:      child.Name,
			}
			if tool.Name == responsesDefaultToolNamespace {
				out[child.Name] = NamespacedToolName{Name: child.Name}
			}
		}
	}
	return out
}

// customToolCallName selects direct or inferred custom identity while
// respecting declared function and namespace names. True function membership
// wins, then true exact custom membership, then a known namespace key keeps
// ordinary handling. Otherwise namespace/custom combinations are checked
// against the derived flat label.
//
// One matching custom name is returned; conflicting matches return false,
// avoiding an arbitrary alias winner. Alias inference ranges every custom key,
// including false-valued entries, whereas direct membership reads the bool.
// Callers use collector-style maps with true entries and stable declarations.
// Name strings are compared exactly without trimming, and the helper does not
// validate the underlying tools.
func customToolCallName(name string, customTools, functionTools map[string]bool, namespaceTools map[string]NamespacedToolName) (string, bool) {
	// Explicit function identity owns the label before custom recognition. This
	// keeps a declared function from being reinterpreted through a neighboring
	// namespace alias.
	if functionTools[name] {
		return "", false
	}
	if customTools[name] {
		return name, true
	}
	if _, ok := namespaceTools[name]; ok {
		return "", false
	}
	// Some calls use a neighboring namespace prefix for a top-level custom name.
	// Infer only one original name, declining different matches so restoration
	// cannot choose arbitrarily.
	match := ""
	for customName := range customTools {
		for _, namespaceTool := range namespaceTools {
			alias := customToolAlias(namespaceTool.Namespace, customName)
			if alias != name {
				continue
			}
			if match != "" && match != customName {
				return "", false
			}
			match = customName
		}
	}
	return match, match != ""
}

// customToolAlias returns a namespace-prefixed custom spelling recognized by
// Chat response restoration. History allocation reserves these same aliases
// so an undeclared function cannot be interpreted as a current custom tool.
func customToolAlias(namespace, name string) string {
	// Captured legacy default-scope mappings can accompany prefixed custom
	// aliases from older adapters. Current default declarations still dispatch
	// their own function names without that prefix.
	if namespace == responsesDefaultToolNamespace {
		return responsesDefaultToolNamespace + "__" + name
	}
	return flattenNamespaceToolName(namespace, name)
}

// customNameForStreamTool returns recognized custom identity or the supplied
// name when recognition fails. State must be nonnil. It rechecks current maps
// rather than freezing the name; callers keep mappings and announced metadata
// stable through completion when those names must agree.
func customNameForStreamTool(state *ChatCompletionsToResponsesStreamState, name string) string {
	// An unresolved alias has no supported original custom identity here. Retain
	// its supplied name instead of guessing a replacement for completion
	// metadata.
	if customName, ok := customToolCallName(name, state.CustomTools, state.FunctionTools, state.NamespaceTools); ok {
		return customName
	}
	return name
}

// HasToolSearchTool reports an exact tool_search declaration among the
// supplied tools.
func HasToolSearchTool(tools []ResponsesTool) bool {
	for _, tool := range tools {
		if tool.Type == "tool_search" {
			return true
		}
	}
	return false
}

// responsesInputToChatMessagesWithOptions prepends nonblank instructions as
// system content, retaining their supplied spacing. Absent/null input returns
// those messages; scalar strings append one user message, while invalid
// nonstring array decoding returns an error.
//
// Array items receive reasoning/call/media conversion and final call-reply
// normalization. Scalar input bypasses that normalization. The optional lookup
// is read through shared options, and returned array-derived content has
// converter-specific filtering rather than complete role/schema validation.
// Programmatic raw content can still be retained for later serialization or
// upstream validation.
func responsesInputToChatMessagesWithOptions(instructions string, inputRaw json.RawMessage, opts *ResponsesToChatOptions) ([]ChatMessage, error) {
	var messages []ChatMessage
	if strings.TrimSpace(instructions) != "" {
		// Instructions are a supplied string, not retained raw JSON. Encoding cannot
		// fail through unsupported values, so the discarded error loses no
		// serialization failure.
		content, _ := json.Marshal(instructions)
		messages = append(messages, ChatMessage{Role: "system", Content: content})
	}

	inputRaw = bytesTrimSpace(inputRaw)
	if len(inputRaw) == 0 || string(inputRaw) == "null" {
		return messages, nil
	}

	// Scalar input supplies one user turn without call-history repair. If that
	// decoding fails, the array path can preserve separate reasoning, calls,
	// results, and media.
	var inputText string
	if err := json.Unmarshal(inputRaw, &inputText); err == nil {
		// The accepted scalar is a decoded string. Its encoding cannot fail
		// through unsupported values or malformed raw fields before becoming user content.
		content, _ := json.Marshal(inputText)
		messages = append(messages, ChatMessage{Role: "user", Content: content})
		return messages, nil
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal(inputRaw, &rawItems); err != nil {
		return nil, fmt.Errorf("parse responses input: %w", err)
	}

	built, mediaByCallID, err := buildChatMessagesFromItems(messages, rawItems, opts)
	if err != nil {
		return nil, err
	}
	return normalizeChatMessagesWithToolOutputMedia(built, mediaByCallID), nil
}

// buildChatMessagesFromItems appends decoded history and returns extracted
// media indexed by correlation. It accepts object items or scalar strings;
// unsupported decode shapes return an error. Pending reasoning precedes last-
// turn reasoning, and a missing plaintext/cache observation can retain earlier
// pending text.
//
// Ordinary function arguments use json.Valid, with invalid nonempty IDs
// excluding later outputs and missing IDs counting subsequent empty-ID outputs
// to skip. Custom input is wrapped, search arguments use their raw/string
// representation, and results become scalar tool text plus optional media.
// Tool outputs clear pending reasoning but retain turn text; user/non-
// assistant content clears both. Shared message slices can change, while
// unknown item kinds are skipped and raw content validation remains partial.
func buildChatMessagesFromItems(messages []ChatMessage, rawItems []json.RawMessage, opts *ResponsesToChatOptions) ([]ChatMessage, toolOutputMediaByCallID, error) {
	// pendingReasoning supplies the next assistant/call before the turn value.
	// Calls/results clear this one-use selection, while lastTurnReasoning can
	// continue across tool replies until new user/non-assistant content clears it.
	var pendingReasoning string
	// lastTurnReasoning survives tool output for chained calls that omit a repeated
	// reasoning item. New user input starts a fresh turn and clears it.
	var lastTurnReasoning string
	invalidFunctionCallIDs := make(map[string]struct{})
	invalidEmptyFunctionCallOutputs := 0
	reasoningForAssistant := func() string {
		if pendingReasoning != "" {
			return pendingReasoning
		}
		return lastTurnReasoning
	}
	mediaByCallID := make(toolOutputMediaByCallID)

	for _, raw := range rawItems {
		raw = bytesTrimSpace(raw)
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}

		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			// A scalar array element can still be user text. Try that shape before
			// failing item conversion, and clear both reasoning selections for the
			// resulting user turn.
			var text string
			if textErr := json.Unmarshal(raw, &text); textErr == nil {
				// A decoded scalar string contains no raw JSON or unsupported values.
				// Its discarded encoder error is nil before this fresh user content is appended.
				content, _ := json.Marshal(text)
				messages = append(messages, ChatMessage{Role: "user", Content: content})
				pendingReasoning = ""
				lastTurnReasoning = ""
				continue
			}
			return nil, nil, fmt.Errorf("parse responses input item: %w", err)
		}

		role := chatCompletionsBridgeRole(rawString(item["role"]))
		itemType := rawString(item["type"])
		switch itemType {
		case "reasoning":
			if txt := extractResponsesReasoningText(item); txt != "" {
				pendingReasoning = txt
			} else if opts != nil && opts.ReasoningContentByID != nil {
				// Reasoning without selected text can consult the supplied ID lookup. A
				// miss leaves pending text unchanged, allowing an earlier reasoning item
				// to remain selected.
				if id := rawString(item["id"]); id != "" {
					if cached := opts.ReasoningContentByID(id); cached != "" {
						pendingReasoning = cached
					}
				}
			}
			if pendingReasoning != "" {
				lastTurnReasoning = pendingReasoning
			}
			continue
		case "function_call":
			// Ordinary calls retain argument text as JSON. An absent/blank string needs
			// an object placeholder before validity checking and history pairing can
			// proceed.
			arguments := rawString(item["arguments"])
			if strings.TrimSpace(arguments) == "" {
				arguments = "{}"
			}
			callID := rawString(item["call_id"])
			if !json.Valid([]byte(arguments)) {
				// Incomplete ordinary argument text cannot become retained valid history.
				// Mark its correlation, or count an empty-ID output, so later matching
				// results are dropped with it.
				if callID != "" {
					invalidFunctionCallIDs[callID] = struct{}{}
				} else {
					invalidEmptyFunctionCallOutputs++
				}
				pendingReasoning = ""
				continue
			}
			name := rawString(item["name"])
			// Known declarations retain their aliases, while missing declarations use
			// request-local replay names that cannot expand the current callable tools.
			if opts != nil && opts.toolHistory != nil {
				resolved, err := opts.toolHistory.resolve(rawString(item["namespace"]), name)
				if err != nil {
					return nil, nil, err
				}
				name = resolved
			} else if ns := rawString(item["namespace"]); ns != "" {
				name = flattenNamespaceToolName(ns, name)
			}
			toolCall := ChatToolCall{
				ID:   callID,
				Type: "function",
				Function: ChatFunctionCall{
					Name:      name,
					Arguments: arguments,
				},
			}
			messages = appendAssistantToolCall(messages, toolCall, reasoningForAssistant())
			pendingReasoning = ""
			continue
		case "tool_search_call":
			// Discovery history can store arguments as a JSON value or string text.
			// Prefer nonempty decoded string spelling, otherwise retain raw JSON before
			// the empty/null default.
			arguments := strings.TrimSpace(string(bytesTrimSpace(item["arguments"])))
			if s := rawString(item["arguments"]); s != "" {
				arguments = s
			}
			if arguments == "" || arguments == "null" {
				arguments = "{}"
			}
			toolCall := ChatToolCall{
				ID:   rawString(item["call_id"]),
				Type: "function",
				Function: ChatFunctionCall{
					Name:      toolSearchProxyName,
					Arguments: arguments,
				},
			}
			messages = appendAssistantToolCall(messages, toolCall, reasoningForAssistant())
			pendingReasoning = ""
			continue
		case "custom_tool_call":
			// Wrap historical freeform input in {"input": ...}, matching
			// customToolInputSchema. Using the same shape for history and declarations
			// lets the model relate past calls to the currently available function
			// tool.
			// The wrapper contains only a decoded string. Its supported map has no
			// invalid raw members or cycles, so the discarded encoding error remains nil.
			arguments, _ := json.Marshal(map[string]string{"input": rawString(item["input"])})
			toolCall := ChatToolCall{
				ID:   rawString(item["call_id"]),
				Type: "function",
				Function: ChatFunctionCall{
					Name:      rawString(item["name"]),
					Arguments: string(arguments),
				},
			}
			messages = appendAssistantToolCall(messages, toolCall, reasoningForAssistant())
			pendingReasoning = ""
			continue
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			outputRaw := bytesTrimSpace(item["output"])
			if itemType == "tool_search_output" && (len(outputRaw) == 0 || string(outputRaw) == "null") {
				// This Chat history route treats absent/null output as the tools-list form.
				// Select that alternative before media/text normalization; nonnull explicit
				// output remains preferred.
				outputRaw = bytesTrimSpace(item["tools"])
			}
			callID := rawString(item["call_id"])
			// Known invalid IDs suppress matching outputs. Uncorrelated malformed calls
			// can only consume subsequent empty-ID outputs by count, in encounter
			// order.
			if callID == "" && invalidEmptyFunctionCallOutputs > 0 {
				invalidEmptyFunctionCallOutputs--
				pendingReasoning = ""
				continue
			}
			if _, skipped := invalidFunctionCallIDs[callID]; skipped {
				pendingReasoning = ""
				continue
			}
			// Duplicate replies select the latest output. Clear earlier images first so
			// a later media-free reply cannot inherit the previous result's media
			// attribution.
			delete(mediaByCallID, callID)

			outputText, media, rewritten := extractToolOutputMedia(outputRaw)
			if rewritten {
				// Extracted media needs nonempty correlation for the later attributed
				// batch. ID-less rewritten output retains its text marker but queues no
				// image parts.
				if callID != "" {
					mediaByCallID[callID] = media
				}
			} else {
				outputText = rawString(outputRaw)
				if outputText == "" && len(outputRaw) > 0 && string(outputRaw) != "null" && string(outputRaw) != `""` {
					// Nonstring results still need scalar Chat tool content. Keep their
					// supplied JSON spelling as text so media-free structured fields remain
					// available in history.
					outputText = string(outputRaw)
				}
			}
			// Result normalization produced one string, so encoding cannot fail through
			// unsupported fields or malformed raw JSON retained inside the source
			// output.
			content, _ := json.Marshal(outputText)
			messages = append(messages, ChatMessage{
				Role:       "tool",
				ToolCallID: callID,
				Content:    content,
			})
			pendingReasoning = ""
			continue
		case "input_text", "text":
			// Direct text items become scalar user content. rawString selects a
			// supported string, so the discarded encoder error cannot suppress a
			// malformed raw member.
			content, _ := json.Marshal(rawString(item["text"]))
			messages = append(messages, ChatMessage{Role: "user", Content: content})
			pendingReasoning = ""
			lastTurnReasoning = ""
			continue
		case "input_image":
			content, err := chatContentFromSingleResponsesPart(itemType, item)
			if err != nil {
				return nil, nil, err
			}
			messages = append(messages, ChatMessage{Role: "user", Content: content})
			pendingReasoning = ""
			lastTurnReasoning = ""
			continue
		}

		// Remaining typed nonmessage items have no modeled Chat message here. Skip
		// them before role/content fallback so private item payloads do not become
		// extra conversation turns.
		if itemType != "" && itemType != "message" {
			pendingReasoning = ""
			continue
		}

		content := item["content"]
		if len(bytesTrimSpace(content)) == 0 {
			if text := rawString(item["text"]); text != "" {
				// Some messages carry top-level text instead of content. Encode that
				// selected string, whose supported value cannot hide a raw-JSON encoder failure.
				content, _ = json.Marshal(text)
			}
		}
		chatContent, err := responsesContentToChatContent(content, role)
		if err != nil {
			return nil, nil, err
		}
		message := ChatMessage{Role: role, Content: chatContent}
		if role == "assistant" {
			message.ReasoningContent = reasoningForAssistant()
			pendingReasoning = ""
		} else {
			pendingReasoning = ""
			lastTurnReasoning = ""
		}
		messages = append(messages, message)
	}

	return messages, mediaByCallID, nil
}

// extractToolOutputMedia replaces recognized output images with markers and
// returns their Chat parts with true. Absent/null, no match, or decode/rebuild
// failure returns empty companions and false so the caller chooses its
// ordinary output-text path.
//
// A scalar image data URL qualifies directly; otherwise scalar JSON text or
// generic JSON is decoded with UseNumber before rewriting arrays and content
// wrappers. Recognized image maps are replaced wholly, and unrelated siblings
// retain decoded values. Rebuilding can alter layout/escaping even while
// preserving number spelling. The function does not validate URLs/base64 or
// search arbitrary map fields, and false does not mean the entire request
// retains its original bytes.
func extractToolOutputMedia(outputRaw json.RawMessage) (string, []ChatContentPart, bool) {
	outputRaw = bytesTrimSpace(outputRaw)
	if len(outputRaw) == 0 || string(outputRaw) == "null" {
		return "", nil, false
	}

	// Scalar output can be a whole image URL or a JSON-text wrapper. Inspect that
	// representation first; structured JSON follows its own number-preserving
	// decode path.
	var outputString string
	if err := json.Unmarshal(outputRaw, &outputString); err == nil {
		if isToolOutputImageDataURL(outputString) {
			return toolOutputMediaMarker, []ChatContentPart{toolOutputImagePart(outputString)}, true
		}

		nested, ok := decodeToolOutputJSON([]byte(outputString))
		if !ok {
			return "", nil, false
		}
		rewritten, media, changed := rewriteToolOutputMediaValue(nested)
		if !changed {
			return "", nil, false
		}
		encoded, err := json.Marshal(rewritten)
		if err != nil {
			// Failed rebuilding supplies no complete marker/media representation.
			// Report no rewrite so the caller retains its ordinary result-text path and
			// drops extracted parts.
			return "", nil, false
		}
		return string(encoded), media, true
	}

	value, ok := decodeToolOutputJSON(outputRaw)
	if !ok {
		return "", nil, false
	}
	rewritten, media, changed := rewriteToolOutputMediaValue(value)
	if !changed {
		return "", nil, false
	}
	encoded, err := json.Marshal(rewritten)
	if err != nil {
		// Failed rebuilding supplies no complete marker/media representation. Report
		// no rewrite so the caller uses ordinary source text rather than partial
		// rewritten output.
		return "", nil, false
	}
	return string(encoded), media, true
}

// decodeToolOutputJSON validates a complete JSON document and decodes it with
// UseNumber. Failed validation/decode returns nil and false. Accepted numeric
// values retain their lexical text for later media reconstruction instead of
// float64 conversion.
func decodeToolOutputJSON(raw []byte) (any, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Image edits must retain unrelated numeric values. Keep number text during
	// decoding so subsequent generic encoding does not round large integers
	// through float64.
	decoder.UseNumber()
	var value any
	// Decode failure contributes no structured candidate. The caller's false path
	// keeps ordinary output text and avoids publishing extracted media from
	// incomplete data.
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

// rewriteToolOutputMediaValue rewrites arrays and map.content recursively,
// returning extracted image parts in traversal order. Callers supply acyclic
// decoded JSON. Array slots and content wrappers can change through shared
// storage; recognized image maps receive fresh replacements. Other scalars
// and unrelated map fields remain unvisited.
//
// Recognized input_image/image_url maps become a two-field marker object,
// discarding that image object's extensions while preserving its URL in a
// returned Chat part. Maps with content keep their other fields. No match
// returns the original value with false, so the caller can avoid generic
// reencoding. This is supported-shape extraction rather than full media or URL
// validation.
func rewriteToolOutputMediaValue(value any) (any, []ChatContentPart, bool) {
	// Only array members and explicit content wrappers are traversed. Unrelated
	// object fields retain their values, preventing arbitrary metadata strings
	// from becoming media.
	switch typed := value.(type) {
	case []any:
		var media []ChatContentPart
		changed := false
		for i, item := range typed {
			rewritten, itemMedia, itemChanged := rewriteToolOutputMediaValue(item)
			if !itemChanged {
				continue
			}
			typed[i] = rewritten
			media = append(media, itemMedia...)
			changed = true
		}
		return typed, media, changed
	case map[string]any:
		// A recognized image object moves wholly out of tool text. Keep its URL in a
		// returned part before replacing the source object and discarding its other
		// image-specific fields.
		if imageURL, ok := recognizedToolOutputImageURL(typed); ok {
			return map[string]any{
				"type": "input_text",
				"text": toolOutputMediaMarker,
			}, []ChatContentPart{toolOutputImagePart(imageURL)}, true
		}

		content, ok := typed["content"]
		if !ok {
			return typed, nil, false
		}
		rewritten, media, changed := rewriteToolOutputMediaValue(content)
		if !changed {
			return typed, nil, false
		}
		typed["content"] = rewritten
		return typed, media, true
	default:
		return value, nil, false
	}
}

// recognizedToolOutputImageURL accepts exact input_image/image_url kind with a
// nonblank string URL or nested string url. It returns the supplied spelling,
// including surrounding spaces. Other kinds/shapes return false; URL scheme,
// payload encoding, and image validity are not checked.
func recognizedToolOutputImageURL(value map[string]any) (string, bool) {
	partType, _ := value["type"].(string)
	if partType != "input_image" && partType != "image_url" {
		return "", false
	}

	switch imageURL := value["image_url"].(type) {
	case string:
		return imageURL, strings.TrimSpace(imageURL) != ""
	case map[string]any:
		url, _ := imageURL["url"].(string)
		return url, strings.TrimSpace(url) != ""
	default:
		return "", false
	}
}

// isToolOutputImageDataURL reports exact data:image/ prefix, a nonempty
// subtype before ;base64,, and any following payload byte. Input is not
// trimmed and payload/MIME validity is unchecked, so whitespace or invalid
// base64 can still satisfy this syntax test.
func isToolOutputImageDataURL(value string) bool {
	// Whole image URL text qualifies, while embedded URLs remain ordinary text.
	// These literal markers select syntax alone and leave MIME/payload validation
	// outside extraction.
	const prefix = "data:image/"
	const separator = ";base64,"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	separatorIndex := strings.Index(value[len(prefix):], separator)
	if separatorIndex <= 0 {
		return false
	}
	payloadIndex := len(prefix) + separatorIndex + len(separator)
	return payloadIndex < len(value)
}

// toolOutputImagePart constructs a Chat image_url part containing the supplied
// URL alone.
func toolOutputImagePart(imageURL string) ChatContentPart {
	return ChatContentPart{
		Type:     "image_url",
		ImageURL: &ChatImageURL{URL: imageURL},
	}
}

// appendAssistantToolCall appends a call to a trailing exact assistant message
// or creates another assistant value. Existing nonempty reasoning wins;
// otherwise pendingReasoning fills that field. The supplied slice and reused
// call backing storage can change in place.
//
// Adjacent calls can therefore share one message before final history
// normalization selects answered calls and their replies. This helper does not
// identify user turns or check whether the trailing assistant belongs to the
// new call's context. The caller supplies the appropriate reasoning and owns
// the mutable message storage; existing text/content is left in that combined
// assistant message.
func appendAssistantToolCall(messages []ChatMessage, toolCall ChatToolCall, pendingReasoning string) []ChatMessage {
	if n := len(messages); n > 0 && messages[n-1].Role == "assistant" {
		messages[n-1].ToolCalls = append(messages[n-1].ToolCalls, toolCall)
		if messages[n-1].ReasoningContent == "" {
			messages[n-1].ReasoningContent = pendingReasoning
		}
		return messages
	}
	return append(messages, ChatMessage{
		Role:             "assistant",
		ToolCalls:        []ChatToolCall{toolCall},
		ReasoningContent: pendingReasoning,
	})
}

// normalizeChatMessages delegates answered-call adjacency repair with no extra
// media map. Replies with nonempty IDs are moved beside retained calls or
// dropped as orphans; ID-less tool messages remain. Returned messages can
// share original content and call storage.
func normalizeChatMessages(messages []ChatMessage) []ChatMessage {
	return normalizeChatMessagesWithToolOutputMedia(messages, nil)
}

// normalizeChatMessagesWithToolOutputMedia returns call messages followed by
// their selected replies in call order, then any attributed media batch. Last
// nonempty-ID reply wins; calls without selected replies are removed, and ID-
// less tool messages remain at their positions.
//
// A message losing all calls keeps nonempty extracted text or is dropped; when
// calls remain, their subset replaces ToolCalls. Other interleaved messages
// stay later in traversal, and duplicate calls can reuse a selected
// reply/media. Input content and kept call values can share storage. Media is
// looked up only for retained calls, so orphans/unanswered entries do not
// create user image messages.
func normalizeChatMessagesWithToolOutputMedia(messages []ChatMessage, mediaByCallID toolOutputMediaByCallID) []ChatMessage {
	// Duplicate reply IDs select the last message before calls are emitted.
	// Indexing first lets an earlier call retrieve that selected reply despite
	// intervening content.
	replies := make(map[string]ChatMessage)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			replies[m.ToolCallID] = m
		}
	}

	out := make([]ChatMessage, 0, len(messages))
	for _, m := range messages {
		switch {
		case m.Role == "tool":
			// Keep ID-less passthrough tool messages in place. Matching replies are emitted
			// beside their assistant call message; replies with an ID but no matching
			// call are discarded.
			if m.ToolCallID == "" {
				out = append(out, m)
			}
			continue
		case len(m.ToolCalls) > 0:
			kept := make([]ChatToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if _, ok := replies[tc.ID]; ok {
					kept = append(kept, tc)
				}
			}
			if len(kept) == 0 {
				// Removing unanswered calls can leave independent visible text. Retain
				// that ordinary content, while textless/image-only content follows the
				// blank-content predicate.
				if isBlankChatContent(m.Content) {
					continue
				}
				m.ToolCalls = nil
				out = append(out, m)
				continue
			}
			m.ToolCalls = kept
			out = append(out, m)
			for _, tc := range kept {
				out = append(out, replies[tc.ID])
			}

			// Each retained call gets a text attribution before its selected images.
			// Build the batch after all replies, keeping image order tied to the call
			// list rather than result arrival.
			var mediaParts []ChatContentPart
			for _, tc := range kept {
				media := mediaByCallID[tc.ID]
				if len(media) == 0 {
					continue
				}
				mediaParts = append(mediaParts, ChatContentPart{
					Type: "text",
					Text: fmt.Sprintf(toolOutputMediaAttribution, tc.ID),
				})
				mediaParts = append(mediaParts, media...)
			}
			if len(mediaParts) > 0 {
				// Chat parts contain modeled string fields and string-valued references.
				// Their encoder has no unsupported values or raw fragments, so the
				// discarded error is nil.
				content, _ := json.Marshal(mediaParts)
				out = append(out, ChatMessage{Role: "user", Content: content})
			}
		default:
			out = append(out, m)
		}
	}
	return out
}

// isBlankChatContent reports absent/null/empty or textless decoded content.
// Image-only and unsupported content shapes count as blank because the shared
// extractor selects scalar/text parts; this predicate is used when removing
// unanswered calls from a message.
func isBlankChatContent(raw json.RawMessage) bool {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return true
	}
	return chatMessageContentText(raw) == ""
}

// extractResponsesReasoningText selects nonempty string text from summary
// arrays or scalar summary, then consults content only when summary
// contributed no text. Array entries need no specific part kind, and selected
// segments join with newlines.
//
// Absent/null values contribute nothing. A failed map-array shape can still
// supply scalar string text; unsupported members or failed scalar decoding
// contribute no segment. This selection restores plaintext from the
// converter's summary representation while retaining a content alternative for
// other history shapes. Empty return can therefore mean missing, empty, or
// unrecognized text rather than a validated reasoning item.
func extractResponsesReasoningText(item map[string]json.RawMessage) string {
	var parts []string
	collect := func(raw json.RawMessage) {
		raw = bytesTrimSpace(raw)
		if len(raw) == 0 || string(raw) == "null" {
			return
		}
		var arr []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, p := range arr {
				if t := rawString(p["text"]); t != "" {
					parts = append(parts, t)
				}
			}
			return
		}
		// A failed typed-array decode can still be scalar reasoning text. Accept
		// that string alternative; other shapes contribute no segment for subsequent
		// content selection.
		if t := rawString(raw); t != "" {
			parts = append(parts, t)
		}
	}
	collect(item["summary"])
	// Summary wins when it contributed any text. An empty/unsupported summary
	// permits content lookup, avoiding duplication when both fields represent the
	// same reasoning.
	if len(parts) == 0 {
		collect(item["content"])
	}
	return strings.Join(parts, "\n")
}

// ExtractResponsesReasoningItem returns exact reasoning kind, decoded ID, and
// available selected plaintext. Absent/null, map decode failure, and other
// kinds return false. True allows empty ID/text and does not validate
// encrypted content or prove a usable history-cache entry.
func ExtractResponsesReasoningItem(raw json.RawMessage) (id string, text string, ok bool) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", false
	}
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil || rawString(item["type"]) != "reasoning" {
		return "", "", false
	}
	return rawString(item["id"]), extractResponsesReasoningText(item), true
}

// chatCompletionsBridgeRole maps blank trimmed roles to user and case-
// insensitive trimmed developer to system. Other roles retain original
// spelling, including whitespace/case; later exact assistant/user checks
// therefore do not normalize every source role.
func chatCompletionsBridgeRole(role string) string {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return "user"
	}
	// Developer instructions use the Chat system role on this route. Match its
	// normalized label while leaving every other nonblank role spelling for later
	// exact checks.
	if strings.EqualFold(trimmed, "developer") {
		return "system"
	}
	return role
}

// responsesContentToChatContent trims content and retains scalar-string bytes
// or converts array/single-part shapes. Absent/null selects encoded empty
// text. Array images survive only for exact user role, while single image
// objects follow direct conversion without that role filter.
//
// Unrecognized/undecodable shapes return the trimmed raw bytes with no error
// for later validation. Supported arrays can collapse to paragraph-joined
// scalar text and unsupported parts disappear. The returned content can share
// a retained raw slice, and conversion is not a general media, role, or JSON-
// schema validator.
func responsesContentToChatContent(raw json.RawMessage, role string) (json.RawMessage, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		// Absent content uses one scalar empty string. This literal can be
		// encoded with no unsupported value or raw-JSON error to discard.
		empty, _ := json.Marshal("")
		return empty, nil
	}

	// Scalar text already has the target wire shape. Try it before arrays and
	// objects so supported strings retain their trimmed raw encoding.
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return raw, nil
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err == nil {
		return responsesContentPartsToChatContent(rawParts, role)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		return chatContentFromSingleResponsesPart(rawString(obj["type"]), obj)
	}

	// No modeled shape decoded. Retain the supplied bytes for later validation
	// instead of manufacturing empty content and silently losing the caller's
	// representation.
	return raw, nil
}

// responsesContentPartsToChatContent converts decoded text/image parts in
// order, ignoring undecodable entries and unsupported kinds. Text accepts
// three labels or a type that decodes as empty; image URL can be scalar or
// nested. Empty extracted fields contribute nothing.
//
// Without images, text joins with blank lines as a scalar. With images, exact
// user keeps the multimodal array while other roles keep text alone. Image
// payload validity is not inspected and other metadata is outside copied
// fields. A fully filtered list becomes empty scalar text, allowing the owning
// request/history path to decide whether to retain that message.
func responsesContentPartsToChatContent(rawParts []json.RawMessage, role string) (json.RawMessage, error) {
	var textParts []string
	var chatParts []ChatContentPart
	hasNonText := false

	// Supported text and image labels use narrower Chat field shapes. Skip
	// undecodable entries and other kinds while retaining the accepted array order.
	for _, rawPart := range rawParts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(rawPart, &part); err != nil {
			continue
		}
		partType := rawString(part["type"])
		switch partType {
		case "input_text", "output_text", "text", "":
			text := rawString(part["text"])
			if text == "" {
				continue
			}
			textParts = append(textParts, text)
			chatParts = append(chatParts, ChatContentPart{Type: "text", Text: text})
		case "input_image", "image_url":
			imageURL := rawString(part["image_url"])
			// Some image parts use a nested URL object. Consult that alternative
			// only after scalar extraction supplied no value, retaining the chosen spelling.
			if imageURL == "" {
				imageURL = rawNestedString(part["image_url"], "url")
			}
			// Neither accepted URL shape supplied text. Skip this array part so
			// no empty image reference enters the retained multimodal user content.
			if imageURL == "" {
				continue
			}
			hasNonText = true
			chatParts = append(chatParts, ChatContentPart{
				Type:     "image_url",
				ImageURL: &ChatImageURL{URL: imageURL},
			})
		}
	}

	// Text-only arrays collapse to scalar paragraphs on this route. Roles other
	// than exact user also keep scalar text, so image parts do not become their
	// array content.
	if !hasNonText {
		// Joined text is a string with no retained raw fragments or unsupported
		// values. Its discarded encoder error is nil before publishing scalar content.
		joined, _ := json.Marshal(strings.Join(textParts, "\n\n"))
		return joined, nil
	}
	if role != "user" {
		// Joined text is a string with no retained raw fragments or unsupported
		// values. Its discarded encoder error is nil before publishing scalar content.
		joined, _ := json.Marshal(strings.Join(textParts, "\n\n"))
		return joined, nil
	}
	// An empty retained array uses scalar empty text as the modeled content.
	// Encoding this literal string has no unsupported or raw-JSON error to discard.
	if len(chatParts) == 0 {
		empty, _ := json.Marshal("")
		return empty, nil
	}
	return json.Marshal(chatParts)
}

// chatContentFromSingleResponsesPart maps image kinds to one Chat image array
// and other kinds to scalar text. A nested url is used when scalar URL is
// empty; image output can still carry an empty URL. Role filtering and
// validity checks are outside this helper.
func chatContentFromSingleResponsesPart(partType string, part map[string]json.RawMessage) (json.RawMessage, error) {
	switch partType {
	case "input_image", "image_url":
		imageURL := rawString(part["image_url"])
		// Nested URL is the alternate single-image shape. This direct path does not
		// apply the array role filter, so even an empty chosen URL remains modeled
		// here.
		if imageURL == "" {
			imageURL = rawNestedString(part["image_url"], "url")
		}
		return json.Marshal([]ChatContentPart{{
			Type:     "image_url",
			ImageURL: &ChatImageURL{URL: imageURL},
		}})
	default:
		return json.Marshal(rawString(part["text"]))
	}
}

// responsesToolsToChatTools lowers supported declarations in order and rejects
// duplicate nonempty top-level executable names before conversion. Function
// parameters/strictness remain supplied; custom uses the fixed input wrapper,
// client search becomes one proxy, namespaces use bounded child names, and
// x_search retains its modeled filters/options.
//
// Namespace validation rejects joined-label owners that differ or conflict
// with top-level names. Repeated identical namespace/name pairs keep the first
// declaration even if other child fields differ. Search rejects its reserved
// top-level name and deduplicates repeated declarations. Unsupported kinds
// disappear. Retained schemas, slices, and optional pointers can share source
// storage, and these name checks do not establish complete tool-schema
// validity.
func responsesToolsToChatTools(tools []ResponsesTool) ([]ChatTool, error) {
	// Reject name collisions after flattening namespace children.
	// Responses distinguishes namespace and name; Chat dispatches by name alone.
	topLevel := make(map[string]bool)
	for _, tool := range tools {
		if (tool.Type == "function" || tool.Type == "custom") && tool.Name != "" {
			if topLevel[tool.Name] {
				return nil, fmt.Errorf("duplicate top-level executable tool name %q; this upstream cannot disambiguate duplicate names, rename one of the tools", tool.Name)
			}
			topLevel[tool.Name] = true
		}
	}
	flatOwner := make(map[string]NamespacedToolName)
	toolSearchDeclared := false
	out := make([]ChatTool, 0, len(tools))
	for _, tool := range tools {
		switch tool.Type {
		case "function":
			out = append(out, ChatTool{
				Type: "function",
				Function: &ChatFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.Parameters,
					Strict:      tool.Strict,
				},
			})
		case "custom":
			// Custom declarations need the same wrapper as replayed freeform input. Use
			// its function parameter schema and omit the original grammar fields from
			// this shape.
			out = append(out, ChatTool{
				Type: "function",
				Function: &ChatFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  json.RawMessage(customToolInputSchema),
				},
			})
		case "tool_search":
			// Reject a normal tool named tool_search, which would be misclassified on return.
			// Repeated tool_search declarations share one proxy.
			if topLevel[toolSearchProxyName] {
				return nil, fmt.Errorf("built-in tool_search conflicts with a declared tool named %q; this upstream cannot disambiguate them, rename the tool", toolSearchProxyName)
			}
			if toolSearchDeclared {
				continue
			}
			toolSearchDeclared = true
			out = append(out, toolSearchProxyChatTool())
		case "namespace":
			flattened, err := namespaceChildrenToChatTools(tool, topLevel, flatOwner)
			if err != nil {
				return nil, err
			}
			out = append(out, flattened...)
		case "x_search":
			// Hosted X search retains its modeled filters and optional media flags.
			// Keep this tool kind outside function lowering so its selector can remain
			// hosted too.
			out = append(out, ChatTool{
				Type:                     "x_search",
				AllowedXHandles:          tool.AllowedXHandles,
				ExcludedXHandles:         tool.ExcludedXHandles,
				FromDate:                 tool.FromDate,
				ToDate:                   tool.ToDate,
				EnableImageUnderstanding: tool.EnableImageUnderstanding,
				EnableVideoUnderstanding: tool.EnableVideoUnderstanding,
			})
		}
		// Omit other types such as web_search and image_generation, for which this
		// Chat conversion has no tool representation.
	}
	return out, nil
}

// toolSearchProxyChatTool constructs the reserved function declaration for
// client discovery execution.
func toolSearchProxyChatTool() ChatTool {
	return ChatTool{
		Type: "function",
		Function: &ChatFunction{
			Name:        toolSearchProxyName,
			Description: "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task.",
			Parameters:  json.RawMessage(toolSearchProxySchema),
		},
	}
}

// namespaceChildrenToChatTools lowers named exact function children of a
// nonempty namespace. Nonempty Tools wins over Children; unsupported children
// are skipped. Top-level conflicts or differing owners at one flat label
// return an error, while repeated original pairs are omitted.
//
// Callers provide initialized flatOwner when accepted children are possible;
// earlier owner entries remain if a later child fails. Equality checks
// namespace/name only, so a repeated pair's later schema or description is not
// compared. Emitted parameter bytes and strict pointers remain shared.
// Namespace/name spelling is not trimmed, and the complete result requires the
// caller's overall declaration validation.
func namespaceChildrenToChatTools(tool ResponsesTool, topLevel map[string]bool, flatOwner map[string]NamespacedToolName) ([]ChatTool, error) {
	if tool.Name == "" {
		return nil, nil
	}
	// Empty primary children permits the alternate Children list. The same owner
	// checks then apply to either representation before emitted names can be
	// restored.
	children := tool.Tools
	if len(children) == 0 {
		children = tool.Children
	}
	var out []ChatTool
	for _, child := range children {
		if child.Type != "function" || child.Name == "" {
			continue
		}
		flat := flattenNamespaceToolName(tool.Name, child.Name)
		entry := NamespacedToolName{Namespace: tool.Name, Name: child.Name}
		if topLevel[flat] {
			return nil, fmt.Errorf("namespace tool %q/%q flattens to %q which conflicts with a top-level tool of the same name; this upstream cannot disambiguate them, rename one of the tools", tool.Name, child.Name, flat)
		}
		// A repeated original pair uses its first emitted declaration, even if later
		// schema/description differs. Different pairs at the same label would
		// restore to different identities.
		if prev, ok := flatOwner[flat]; ok {
			if prev == entry {
				continue
			}
			return nil, fmt.Errorf("namespace tools %q/%q and %q/%q both flatten to %q; this upstream cannot disambiguate them, rename one of the tools", prev.Namespace, prev.Name, tool.Name, child.Name, flat)
		}
		flatOwner[flat] = entry
		out = append(out, ChatTool{
			Type: "function",
			Function: &ChatFunction{
				Name:        flat,
				Description: child.Description,
				Parameters:  child.Parameters,
				Strict:      child.Strict,
			},
		})
	}
	return out, nil
}

// flattenNamespaceToolName keeps default-scope names bare and joins other
// namespaces and children with two underscores within the 64-byte budget.
// Longer values retain a rune-bounded prefix plus two underscores and eight
// hexadecimal digits from the first four SHA-256 bytes.
//
// The suffix derives from the entire joined name, keeping long-name shortening
// stable while original identities are held separately for restoration. Hash
// prefixes and joins can still collide, so declaration validation checks
// owners rather than treating this label as proof of unique identity. Inputs
// are not trimmed or validated, and a short result can contain supplied
// invalid name syntax.
func flattenNamespaceToolName(namespace, name string) string {
	if namespace == "" || namespace == responsesDefaultToolNamespace {
		// Codex default-scope calls use bare names, so declarations and historical
		// calls must share that spelling instead of acquiring an executable prefix.
		return name
	}
	full := namespace + "__" + name
	if len(full) <= chatToolNameMaxLen {
		return full
	}
	// Reserve ten bytes for two separators and the eight-digit hash prefix.
	// Truncate the readable prefix at rune boundaries, then retain full-name
	// differentiation through that suffix.
	sum := sha256.Sum256([]byte(full))
	suffix := "__" + hex.EncodeToString(sum[:4])
	prefixLen := chatToolNameMaxLen - len(suffix)
	var prefix strings.Builder
	for _, ch := range full {
		if prefix.Len()+len(string(ch)) > prefixLen {
			break
		}
		// This local Builder is not copied after writes. WriteRune returns a nil
		// error, so the discarded result retains the accepted complete rune without
		// suppressing failure.
		_, _ = prefix.WriteRune(ch)
	}
	return prefix.String() + suffix
}

// responsesToolChoiceToChatToolChoice maps retained executable/custom/search
// selections to nested Chat function choice and x_search to its hosted
// selector. Map-decode failures retain raw bytes, including scalar strings or
// malformed input. Literal null decodes as an empty map and is omitted.
//
// Function/custom names prefer top-level name then the legacy nested field; no
// usable name retains the supplied object. Unknown object kinds and undeclared
// selected names return nil, instructing callers to omit choice. Rebuild
// failures retain raw bytes. Exact names are not trimmed and returned
// passthrough values are not selector validation, so callers serialize and
// validate the request separately.
func responsesToolChoiceToChatToolChoice(raw json.RawMessage, declared map[string]bool) json.RawMessage {
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &choice); err != nil {
		// Map decoding also fails for valid scalar selectors. Retain the raw
		// selection for those forms and later validation, including when the
		// supplied bytes are malformed.
		return raw
	}
	var name string
	switch rawString(choice["type"]) {
	case "x_search":
		if !declared["x_search"] {
			return nil
		}
		out, err := json.Marshal(map[string]any{"type": "x_search"})
		if err != nil {
			// No rebuilt selector is available after encoding failure. Return source
			// bytes so caller validation receives the supplied selection instead of a
			// partial replacement.
			return raw
		}
		return out
	case "tool_search":
		// responsesToolsToChatTools adapts tool_search to a function proxy with the
		// same name. Adapt the forced choice too so a required search does not
		// become optional model selection.
		name = toolSearchProxyName
	case "function", "custom":
		// Custom tools use function definitions here, so their choices must use that
		// type too.
		name = rawString(choice["name"])
		// Legacy selection nests function name. Use that alternate only when the
		// preferred top-level field supplied no string, keeping one selected target
		// identity.
		if name == "" {
			name = rawNestedString(choice["function"], "name")
		}
		if name == "" {
			return raw
		}
	default:
		return nil
	}
	if !declared[name] {
		return nil
	}
	out, err := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]string{
			"name": name,
		},
	})
	if err != nil {
		// No rebuilt selector is available after encoding failure. Retain source
		// selection for later validation instead of publishing a partially encoded
		// target object.
		return raw
	}
	return out
}

// extractCustomToolCallInput trims adapted argument text and selects a decoded
// input string when present. Empty text, null/empty objects, or input:null
// select empty input. Map-decode failure or a nonstring input returns the
// trimmed source text for later client validation.
//
// An input string takes precedence over other object members, which are not
// returned. A nonempty object lacking input also remains raw text rather than
// becoming an empty custom call. This preserves malformed/nonwrapper argument
// information while allowing the fixed function wrapper to recover freeform
// content; it does not enforce a custom grammar or validate usable tool input.
func extractCustomToolCallInput(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return ""
	}
	var obj map[string]json.RawMessage
	// Nonwrapper or malformed input still carries useful client argument text.
	// Keep its trimmed form when object decoding fails, allowing later validation
	// to inspect that representation.
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return trimmed
	}
	if raw, ok := obj["input"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		// A present input field that is not string-decodable cannot supply
		// freeform text here. Keep the complete wrapper for the client's shape validation.
		return trimmed
	}
	if len(obj) == 0 {
		return ""
	}
	return trimmed
}

// ChatCompletionsResponseToResponses converts only the first buffered choice
// with supplied restoration maps. A nonempty caller model wins, otherwise
// source model is used. Missing response ID and nonpositive source creation
// time use runtime callbacks; nil source still produces completed empty-
// message output.
//
// Message conversion separates reasoning, visible text, and supported calls,
// restoring custom/search/namespace identities according to those maps.
// Ordinary invalid JSON calls are skipped, while custom/search text follows
// specialized handling. Exact length makes nested status incomplete with max-
// output details; other finish reasons leave completed. Usage/tier are copied
// through their converters, and generated item IDs require a random reader. No
// map/declaration or full argument/schema validation is performed here.
func ChatCompletionsResponseToResponses(runtime Runtime, resp *ChatCompletionsResponse, model string, customTools, functionTools map[string]bool, toolSearch bool, namespaceTools map[string]NamespacedToolName) *ResponsesResponse {
	id := ""
	if resp != nil {
		id = resp.ID
	}
	// Buffered replies can lack response identity. Generate its label through the
	// injected reader so the modeled response still has metadata available to its
	// consumer.
	if id == "" {
		id = generateResponsesID(runtime)
	}

	// Source creation time wins only when positive. Otherwise capture the
	// injected clock, while stream constructors separately retain one time for
	// their complete lifecycle.
	createdAt := int64(0)
	if resp != nil {
		createdAt = resp.Created
	}
	if createdAt <= 0 {
		createdAt = runtime.Now().Unix()
	}

	out := &ResponsesResponse{
		ID:          id,
		Object:      "response",
		CreatedAt:   createdAt,
		Model:       model,
		Status:      "completed",
		ServiceTier: chatServiceTier(resp),
	}
	if resp == nil {
		out.Output = []ResponsesOutput{emptyResponsesMessageOutput(runtime)}
		return out
	}
	if out.Model == "" {
		out.Model = resp.Model
	}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		out.Output = chatMessageToResponsesOutput(runtime, choice.Message, customTools, functionTools, toolSearch, namespaceTools)
		if choice.FinishReason == "length" {
			out.Status = "incomplete"
			out.IncompleteDetails = &ResponsesIncompleteDetails{Reason: "max_output_tokens"}
		}
	}
	// Fully filtered or choice-free output still needs a modeled reply shape. Add
	// an empty message without reintroducing ordinary calls rejected for invalid
	// argument JSON.
	if len(out.Output) == 0 {
		out.Output = []ResponsesOutput{emptyResponsesMessageOutput(runtime)}
	}
	if resp.Usage != nil {
		out.Usage = ChatUsageToResponsesUsage(resp.Usage)
	}
	return out
}

// chatServiceTier returns the source service tier or empty text for a nil
// reply. Buffered conversion uses this result before its nil-source branch;
// tier spelling is retained without normalization or supported-value checks.
func chatServiceTier(resp *ChatCompletionsResponse) string {
	if resp == nil {
		return ""
	}
	return resp.ServiceTier
}

// chatMessageToResponsesOutput emits selected reasoning, visible message text,
// then calls in source order. Without any calls, textless nonblank reasoning
// is also exposed as visible text; otherwise an empty ordinary reply still
// receives a message. Runtime.ReadRandom supplies generated item IDs.
//
// Custom recognition runs before enabled search recognition, then ordinary
// calls require json.Valid and can regain a recorded namespace. JSON validity
// accepts scalar values too. Empty/blank arguments select an object;
// specialized custom/search cases can retain malformed text. Returned item
// kinds and identity pairs rely on stable caller maps, while raw content
// parts, signatures, and unknown message fields are outside the modeled
// output.
func chatMessageToResponsesOutput(runtime Runtime, message ChatMessage, customTools, functionTools map[string]bool, toolSearch bool, namespaceTools map[string]NamespacedToolName) []ResponsesOutput {
	var outputs []ResponsesOutput
	reasoning := message.ReasoningText()
	if reasoning != "" {
		outputs = append(outputs, ResponsesOutput{
			Type: "reasoning",
			ID:   generateItemID(runtime),
			Summary: []ResponsesSummary{{
				Type: "summary_text",
				Text: reasoning,
			}},
		})
	}

	text := chatMessageContentText(message.Content)
	// Reasoning-only ordinary output would otherwise have no visible message.
	// Reuse nonblank reasoning as text when there are no calls, keeping its
	// separate reasoning item too.
	if text == "" && strings.TrimSpace(reasoning) != "" && len(message.ToolCalls) == 0 {
		text = reasoning
	}
	if text != "" || len(message.ToolCalls) == 0 {
		outputs = append(outputs, ResponsesOutput{
			Type: "message",
			ID:   generateItemID(runtime),
			Role: "assistant",
			Content: []ResponsesContentPart{{
				Type: "output_text",
				Text: text,
			}},
			Status: "completed",
		})
	}

	for _, toolCall := range message.ToolCalls {
		arguments := toolCall.Function.Arguments
		// An argument-free call needs an object spelling in the target
		// representation. Keep nonblank source text for specialized restoration or
		// ordinary JSON validity checking.
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		// Resolve custom identity before search and ordinary namespaces. Specialized
		// calls retain their input representation even when ordinary JSON validity
		// would reject it.
		if customName, ok := customToolCallName(toolCall.Function.Name, customTools, functionTools, namespaceTools); ok {
			outputs = append(outputs, ResponsesOutput{
				Type:   "custom_tool_call",
				ID:     generateItemID(runtime),
				CallID: toolCall.ID,
				Name:   customName,
				Input:  extractCustomToolCallInput(arguments),
				Status: "completed",
			})
			continue
		}
		if toolSearch && toolCall.Function.Name == toolSearchProxyName {
			outputs = append(outputs, ResponsesOutput{
				Type:      "tool_search_call",
				ID:        generateItemID(runtime),
				CallID:    toolCall.ID,
				Arguments: arguments,
				Status:    "completed",
			})
			continue
		}
		// Skip function calls with invalid JSON arguments so truncated non-streaming
		// calls do not enter the next turn's history as completed calls.
		if !json.Valid([]byte(arguments)) {
			continue
		}
		if ns, ok := namespaceTools[toolCall.Function.Name]; ok {
			outputs = append(outputs, ResponsesOutput{
				Type:      "function_call",
				ID:        generateItemID(runtime),
				CallID:    toolCall.ID,
				Name:      ns.Name,
				Namespace: ns.Namespace,
				Arguments: arguments,
				Status:    "completed",
			})
			continue
		}
		outputs = append(outputs, ResponsesOutput{
			Type:      "function_call",
			ID:        generateItemID(runtime),
			CallID:    toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: arguments,
			Status:    "completed",
		})
	}

	return outputs
}

// emptyResponsesMessageOutput constructs one completed assistant item with
// empty output text and generated identity. Runtime.ReadRandom is required for
// that label; the item supplies the modeled fallback for nil, empty, or fully
// filtered buffered replies.
func emptyResponsesMessageOutput(runtime Runtime) ResponsesOutput {
	return ResponsesOutput{
		Type:    "message",
		ID:      generateItemID(runtime),
		Role:    "assistant",
		Content: []ResponsesContentPart{{Type: "output_text", Text: ""}},
		Status:  "completed",
	}
}

// chatMessageContentText returns scalar text or paragraph-joined exact text
// parts. Absent/null, failed array decoding, and unsupported media/part kinds
// contribute empty text. It performs no full content validation and is used by
// visible-text fallback and blank-history selection.
func chatMessageContentText(raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []ChatContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var texts []string
		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, "\n\n")
	}
	// Unsupported or undecodable content supplies no selected visible text.
	// Return empty so the owning fallback/blank predicate can choose its modeled
	// reply or history behavior.
	return ""
}

// ChatUsageToResponsesUsage copies prompt/completion counts and supplied
// total, returning nil for nil usage. An exact zero total is recomputed;
// nonzero totals remain even when inconsistent. Input details are emitted only
// when cached/creation/write has a positive value.
//
// Within that selected object, supplied read and both write aliases remain,
// including any negative sibling values. Positive write wins canonical cache
// creation, otherwise creation is selected. Audio-only prompt details and
// output detail counters are not copied on this route. Counts are not
// validated, and the aliases overlap rather than being added into canonical
// creation.
func ChatUsageToResponsesUsage(usage *ChatUsage) *ResponsesUsage {
	if usage == nil {
		return nil
	}
	out := &ResponsesUsage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
	// An omitted or zero aggregate uses the copied components for total usage. A
	// nonzero supplied aggregate stays selected, even when the individual
	// counters differ.
	if out.TotalTokens == 0 {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	// A positive cached/creation/write count creates input details here. This
	// route omits audio-only/output details; positive write wins canonical
	// creation, otherwise the supplied creation alias is selected.
	if usage.PromptTokensDetails != nil && (usage.PromptTokensDetails.CachedTokens > 0 ||
		usage.PromptTokensDetails.CacheCreationTokens > 0 || usage.PromptTokensDetails.CacheWriteTokens > 0) {
		out.InputTokensDetails = &ResponsesInputTokensDetails{
			CachedTokens:        usage.PromptTokensDetails.CachedTokens,
			CacheCreationTokens: usage.PromptTokensDetails.CacheCreationTokens,
			CacheWriteTokens:    usage.PromptTokensDetails.CacheWriteTokens,
		}
		if usage.PromptTokensDetails.CacheWriteTokens > 0 {
			out.CacheCreationInputTokens = usage.PromptTokensDetails.CacheWriteTokens
		} else {
			out.CacheCreationInputTokens = usage.PromptTokensDetails.CacheCreationTokens
		}
	}
	return out
}

// NewChatCompletionsToResponsesStreamState generates response identity,
// captures time, and initializes all call/classification maps. Runtime
// callbacks are required. Caller model wins when nonempty; declarations are
// supplied afterward before chunks, and used Builder state stays pointer-owned
// and serialized.
func NewChatCompletionsToResponsesStreamState(runtime Runtime, model string) *ChatCompletionsToResponsesStreamState {
	return &ChatCompletionsToResponsesStreamState{
		ResponseID:       generateResponsesID(runtime),
		Model:            model,
		Created:          runtime.Now().Unix(),
		ToolCalls:        make(map[int]*ChatToolCall),
		ToolItemIDs:      make(map[int]string),
		ToolOutputIndex:  make(map[int]int),
		toolIsCustom:     make(map[int]bool),
		toolIsToolSearch: make(map[int]bool),
		toolNamespace:    make(map[int]NamespacedToolName),
		toolAnnounced:    make(map[int]bool),
	}
}

// ValidateToolCallArguments rejects nonblank invalid JSON in ordinary recorded
// calls and returns nil for nil state. Nil records, blank arguments, and
// captured custom/search classifications are skipped. The first error follows
// map iteration order.
//
// Classification comes from announcement, so an unannounced deferred call has
// not acquired its specialized skip flag yet. json.Valid accepts any JSON
// value, not only an object, and metadata/identity is outside this check. The
// public finalizer does not invoke validation; callers run it before relying
// on a completed history snapshot or ordinary argument-done events.
func (state *ChatCompletionsToResponsesStreamState) ValidateToolCallArguments() error {
	if state == nil {
		return nil
	}
	for idx, toolCall := range state.ToolCalls {
		if toolCall == nil {
			continue
		}
		if state.toolIsCustom[idx] || state.toolIsToolSearch[idx] {
			continue
		}
		arguments := strings.TrimSpace(toolCall.Function.Arguments)
		if arguments == "" {
			continue
		}
		if !json.Valid([]byte(arguments)) {
			return fmt.Errorf("tool call %q (%s) arguments are invalid JSON", toolCall.ID, toolCall.Function.Name)
		}
	}
	return nil
}

// allocOutputIndex returns the next lifecycle position and advances its stream
// counter.
func (state *ChatCompletionsToResponsesStreamState) allocOutputIndex() int {
	idx := state.nextOutputIndex
	state.nextOutputIndex++
	return idx
}

// ChatCompletionsChunkToResponsesEvents converts all choices through one
// constructed pointer state. Nil chunk/state returns nothing. Nonempty ID/tier
// replaces metadata, an empty chosen model can take source model, and each
// present usage replaces prior usage before created is emitted once.
//
// Reasoning/text fragments accumulate independently; text or a new call closes
// the sole reasoning lifecycle. New call records allocate item identity/output
// position, defer classification if names are needed, then append first/later
// argument text through one path. Ordinary announced calls stream fragments;
// custom/search waits for completion. Equal tool indexes across choices share
// a record, and later nonempty ID/name can change metadata after announcement.
// CompletedSent is not checked here; input order and declaration stability
// remain caller obligations.
func ChatCompletionsChunkToResponsesEvents(runtime Runtime,
	chunk *ChatCompletionsChunk,
	state *ChatCompletionsToResponsesStreamState,
) []ResponsesStreamEvent {
	if chunk == nil || state == nil {
		return nil
	}
	if chunk.ID != "" {
		state.ResponseID = chunk.ID
	}
	if state.Model == "" && chunk.Model != "" {
		state.Model = chunk.Model
	}
	if chunk.ServiceTier != "" {
		state.ServiceTier = chunk.ServiceTier
	}
	if chunk.Usage != nil {
		state.Usage = ChatUsageToResponsesUsage(chunk.Usage)
	}

	var events []ResponsesStreamEvent
	events = append(events, ensureChatToResponsesCreated(state)...)

	for _, choice := range chunk.Choices {
		// Open the reasoning item and summary part before its first delta.
		// Ignore empty initial deltas commonly emitted by upstreams.
		reasoning := choice.Delta.ReasoningText()
		if reasoning != nil && *reasoning != "" {
			events = append(events, ensureChatReasoningItem(runtime, state)...)
			// Append the selected reasoning prefix before emitting its fragment.
			// Builder errors are nil, but a copied used state can still panic through
			// copy checking.
			_, _ = state.Reasoning.WriteString(*reasoning)
			events = append(events, chatToResponsesEvent(state, "response.reasoning_summary_text.delta", &ResponsesStreamEvent{
				OutputIndex:  state.ReasoningIndex,
				SummaryIndex: 0,
				Delta:        *reasoning,
				ItemID:       state.ReasoningItemID,
			}))
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			// Close reasoning before opening the message and output_text part
			// for the first text delta.
			events = append(events, closeChatReasoningItem(state)...)
			events = append(events, ensureChatToResponsesMessageItem(runtime, state)...)
			events = append(events, ensureChatToResponsesTextPart(state)...)
			// Text completion reads this exact accumulated prefix. Builder errors are
			// nil; preserve pointer ownership because later writes check whether the
			// used Builder was copied.
			_, _ = state.Text.WriteString(*choice.Delta.Content)
			events = append(events, chatToResponsesEvent(state, "response.output_text.delta", &ResponsesStreamEvent{
				OutputIndex:  state.MessageIndex,
				ContentIndex: 0,
				Delta:        *choice.Delta.Content,
				ItemID:       state.MessageItemID,
			}))
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			// Missing indexes join the zero-key call across all choices. Capture the
			// source key now; later completion scans assume contiguous keys from zero.
			idx := 0
			if toolCall.Index != nil {
				idx = *toolCall.Index
			}
			stored, ok := state.ToolCalls[idx]
			if !ok {
				// Close any open reasoning item before starting a tool call.
				events = append(events, closeChatReasoningItem(state)...)
				copyCall := toolCall
				// Call metadata can precede upstream correlation. Generate a temporary
				// label for its record, permitting later nonempty ID to replace it before
				// completion.
				if copyCall.ID == "" {
					copyCall.ID = generateItemID(runtime)
				}
				copyCall.Type = "function"
				// Initial metadata can include arguments in the same observation. Clear
				// copied arguments before the common append path so that first fragment
				// contributes once.
				copyCall.Function.Arguments = ""
				state.ToolCalls[idx] = &copyCall
				stored = &copyCall
				state.ToolItemIDs[idx] = generateItemID(runtime)
				state.ToolOutputIndex[idx] = state.allocOutputIndex()
			} else {
				if toolCall.ID != "" {
					stored.ID = toolCall.ID
				}
				if toolCall.Function.Name != "" {
					stored.Function.Name = toolCall.Function.Name
				}
			}
			events = append(events, announceChatToolItem(state, idx, stored, false)...)
			if toolCall.Function.Arguments != "" {
				stored.Function.Arguments += toolCall.Function.Arguments
				// Buffer until announcement when the name has not arrived. Custom-call JSON
				// wraps freeform input, so decode the complete arguments at finalization in
				// closeChatToolItems. Tool-search arguments are also delivered whole with
				// output_item.done.
				if state.toolAnnounced[idx] && !state.toolIsCustom[idx] && !state.toolIsToolSearch[idx] {
					events = append(events, chatToResponsesEvent(state, "response.function_call_arguments.delta", &ResponsesStreamEvent{
						OutputIndex: state.ToolOutputIndex[idx],
						ItemID:      state.ToolItemIDs[idx],
						Delta:       toolCall.Function.Arguments,
						CallID:      stored.ID,
						Name:        stored.Function.Name,
					}))
				}
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			// Later nonempty finish observations replace earlier ones across choices.
			// Finalization applies its exact length rule to that selected value rather
			// than inferring from fragments.
			state.FinishReason = *choice.FinishReason
		}
	}

	return events
}

// FinalizeChatCompletionsResponsesStream closes modeled lifecycles and emits
// one terminal snapshot for nonnil unfinished state. It creates a missing
// response start, closes reasoning, optionally synthesizes reasoning-only
// visible text, completes the message, then completes contiguous indexed
// calls. Argument validation is a separate caller operation.
//
// Exact length sets nested incomplete status/details, while the terminal event
// label remains response.completed. State is marked completed before final
// snapshot construction, so callback failure can leave that guard set.
// Terminal output is rebuilt with some new item IDs rather than preserving all
// lifecycle identities. Usage remains shared, and late chunk processing is not
// globally blocked by the finalizer guard.
func FinalizeChatCompletionsResponsesStream(runtime Runtime, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state == nil || state.CompletedSent {
		return nil
	}
	var events []ResponsesStreamEvent
	events = append(events, ensureChatToResponsesCreated(state)...)

	// Close reasoning even when no text followed it.
	events = append(events, closeChatReasoningItem(state)...)
	events = append(events, synthesizeChatReasoningFallbackMessage(runtime, state)...)

	if state.MessageItemID != "" {
		if state.TextPartOpen {
			events = append(events, chatToResponsesEvent(state, "response.output_text.done", &ResponsesStreamEvent{
				OutputIndex:  state.MessageIndex,
				ContentIndex: 0,
				Text:         state.Text.String(),
				ItemID:       state.MessageItemID,
			}))
			events = append(events, chatToResponsesEvent(state, "response.content_part.done", &ResponsesStreamEvent{
				OutputIndex:  state.MessageIndex,
				ContentIndex: 0,
				ItemID:       state.MessageItemID,
				Part:         &ResponsesContentPart{Type: "output_text", Text: state.Text.String()},
			}))
		}
		events = append(events, chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
			OutputIndex: state.MessageIndex,
			Item: &ResponsesOutput{
				Type:    "message",
				ID:      state.MessageItemID,
				Role:    "assistant",
				Content: []ResponsesContentPart{{Type: "output_text", Text: state.Text.String()}},
				Status:  "completed",
			},
		}))
	}

	// Complete each modeled call through its captured kind. Ordinary calls need
	// full argument/item done events; custom input and search use their separate
	// completion representations.
	events = append(events, closeChatToolItems(state)...)

	status := "completed"
	var incompleteDetails *ResponsesIncompleteDetails
	if state.FinishReason == "length" {
		status = "incomplete"
		incompleteDetails = &ResponsesIncompleteDetails{Reason: "max_output_tokens"}
	}

	// Mark termination before constructing its rebuilt snapshot. The event label
	// stays completed even when nested status is incomplete, and callback failure
	// can leave this guard set.
	state.CompletedSent = true
	events = append(events, chatToResponsesEvent(state, "response.completed", &ResponsesStreamEvent{
		Response: &ResponsesResponse{
			ID:                state.ResponseID,
			Object:            "response",
			CreatedAt:         state.Created,
			Model:             state.Model,
			Status:            status,
			ServiceTier:       state.ServiceTier,
			Output:            state.chatOutput(runtime),
			Usage:             state.Usage,
			IncompleteDetails: incompleteDetails,
		},
	}))
	return events
}

// ensureChatToResponsesCreated emits an in-progress empty-output snapshot once
// using current state metadata. Nonnil state is required and no fallback
// metadata is generated here; the created flag is set before event
// construction consumes a sequence number.
func ensureChatToResponsesCreated(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.CreatedSent {
		return nil
	}
	state.CreatedSent = true
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.created", &ResponsesStreamEvent{
		Response: &ResponsesResponse{
			ID:          state.ResponseID,
			Object:      "response",
			CreatedAt:   state.Created,
			Model:       state.Model,
			Status:      "in_progress",
			ServiceTier: state.ServiceTier,
			Output:      []ResponsesOutput{},
		},
	})}
}

// ensureChatReasoningItem allocates the sole reasoning identity/index and
// emits item/summary-part starts before a first delta. Open or already-done
// reasoning returns nothing. Nonnil state and runtime random callback are
// required for an initial opening.
//
// Closing makes ReasoningDone permanent, so this helper does not open a second
// reasoning series after text or tools. The chunk handler can nevertheless
// append/emit later reasoning against the retained identity, requiring ordered
// source reasoning for a balanced lifecycle. Summary index stays zero, while
// full text remains in the shared Builder for completion and terminal
// reconstruction.
func ensureChatReasoningItem(runtime Runtime, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.ReasoningOpen || state.ReasoningDone {
		return nil
	}
	state.ReasoningOpen = true
	state.ReasoningItemID = generateItemID(runtime)
	state.ReasoningIndex = state.allocOutputIndex()
	return []ResponsesStreamEvent{
		chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
			OutputIndex: state.ReasoningIndex,
			Item:        &ResponsesOutput{Type: "reasoning", ID: state.ReasoningItemID, Status: "in_progress"},
		}),
		chatToResponsesEvent(state, "response.reasoning_summary_part.added", &ResponsesStreamEvent{
			OutputIndex:  state.ReasoningIndex,
			SummaryIndex: 0,
			ItemID:       state.ReasoningItemID,
			Part:         &ResponsesContentPart{Type: "summary_text"},
		}),
	}
}

// closeChatReasoningItem marks reasoning closed/done and snapshots its prefix
// into text-done, part-done, and item-done events. Nonnil state is required;
// closed state returns no events.
//
// All three completions reuse its announced identity/index and summary index
// zero, while the Builder is retained. Subsequent terminal reconstruction can
// use a different item ID and can include later appended text. This closer
// supplies lifecycle completion alone and does not clear history or reopen
// reasoning; callers finish that series before other source content continues.
func closeChatReasoningItem(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if !state.ReasoningOpen {
		return nil
	}
	state.ReasoningOpen = false
	state.ReasoningDone = true
	reasoning := state.Reasoning.String()
	return []ResponsesStreamEvent{
		chatToResponsesEvent(state, "response.reasoning_summary_text.done", &ResponsesStreamEvent{
			OutputIndex:  state.ReasoningIndex,
			SummaryIndex: 0,
			Text:         reasoning,
			ItemID:       state.ReasoningItemID,
		}),
		chatToResponsesEvent(state, "response.reasoning_summary_part.done", &ResponsesStreamEvent{
			OutputIndex:  state.ReasoningIndex,
			SummaryIndex: 0,
			ItemID:       state.ReasoningItemID,
			Part:         &ResponsesContentPart{Type: "summary_text", Text: reasoning},
		}),
		chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
			OutputIndex: state.ReasoningIndex,
			Item: &ResponsesOutput{
				Type:    "reasoning",
				ID:      state.ReasoningItemID,
				Status:  "completed",
				Summary: []ResponsesSummary{{Type: "summary_text", Text: reasoning}},
			},
		}),
	}
}

// synthesizeChatReasoningFallbackMessage creates visible text only for
// nonblank reasoning when no message, text, or tool record exists. Nil state
// or any disqualifying observation produces nothing, including whitespace-only
// reasoning or even a nil stored tool entry.
//
// The fallback opens message and text-part lifecycles before writing the
// retained reasoning into Text and emitting its whole delta. It supplements an
// otherwise reasoning-only answer while leaving reasoning output separate.
// Constructor-owned pointer state and runtime random callback are required
// when opening; later message completion uses that copied text prefix, and
// repeated synthesis is suppressed by the now-present message identity.
func synthesizeChatReasoningFallbackMessage(runtime Runtime, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state == nil ||
		state.MessageItemID != "" ||
		state.Text.Len() > 0 ||
		state.Reasoning.Len() == 0 ||
		len(state.ToolCalls) > 0 {
		return nil
	}

	text := state.Reasoning.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}

	var events []ResponsesStreamEvent
	events = append(events, ensureChatToResponsesMessageItem(runtime, state)...)
	events = append(events, ensureChatToResponsesTextPart(state)...)
	// Copy the reasoning prefix only after message/part announcement so its
	// visible delta has an opened target. Builder's returned error is nil for
	// this write.
	_, _ = state.Text.WriteString(text)
	events = append(events, chatToResponsesEvent(state, "response.output_text.delta", &ResponsesStreamEvent{
		OutputIndex:  state.MessageIndex,
		ContentIndex: 0,
		Delta:        text,
		ItemID:       state.MessageItemID,
	}))
	return events
}

// ensureChatToResponsesMessageItem opens the sole assistant message when
// MessageItemID is empty. Nonnil state/runtime random callback are required.
// Existing identity suppresses another start, so later text uses its retained
// output index even when tool items interleave.
//
// Opening announces an in-progress assistant item with an empty output-text
// value. The text-part helper then supplies its own start before deltas.
// Message identity and index remain after completion, so this helper does not
// establish a second message lifecycle or guard late input; callers finish
// source text before finalizing the state.
func ensureChatToResponsesMessageItem(runtime Runtime, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.MessageItemID != "" {
		return nil
	}
	state.MessageItemID = generateItemID(runtime)
	state.MessageIndex = state.allocOutputIndex()
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
		OutputIndex: state.MessageIndex,
		Item: &ResponsesOutput{
			Type:    "message",
			ID:      state.MessageItemID,
			Role:    "assistant",
			Status:  "in_progress",
			Content: []ResponsesContentPart{{Type: "output_text"}},
		},
	})}
}

// ensureChatToResponsesTextPart announces one empty output-text part at
// content index zero. Nonnil state and an already-opened message are required.
// TextPartOpen suppresses another start so all subsequent visible fragments
// address the same message part.
//
// The owning converter invokes this before the first delta and retains full
// Text for done payloads. The flag is not reset during finalization, so
// repeated calls after completion do not reopen that part. Source content-part
// distinctions are collapsed into this single lifecycle; callers stop feeding
// the state when the stream terminates.
func ensureChatToResponsesTextPart(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.TextPartOpen {
		return nil
	}
	state.TextPartOpen = true
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.content_part.added", &ResponsesStreamEvent{
		OutputIndex:  state.MessageIndex,
		ContentIndex: 0,
		ItemID:       state.MessageItemID,
		Part:         &ResponsesContentPart{Type: "output_text", Text: ""},
	})}
}

// announceChatToolItem captures and emits call classification once. Nonnil
// constructed state/stored record is required. A missing name waits when any
// declaration map/search signal exists unless force is true; forced calls
// still use current classification rules rather than bypassing them.
//
// Custom selection precedes enabled search, then ordinary namespace identity
// is captured. The kind flags and namespace pair persist, while later stored
// ID/name and rechecked custom names can change completion metadata. Ordinary
// accumulated arguments replay immediately after added so earlier fragments
// survive late naming; custom wrappers/search text wait for completion. Caller
// maps and source identity remain stable when matching added/done metadata is
// required.
func announceChatToolItem(
	state *ChatCompletionsToResponsesStreamState,
	idx int,
	stored *ChatToolCall,
	force bool,
) []ResponsesStreamEvent {
	if state.toolAnnounced[idx] {
		return nil
	}
	if !force && stored.Function.Name == "" && (len(state.CustomTools) > 0 || len(state.FunctionTools) > 0 || state.ToolSearchDeclared || len(state.NamespaceTools) > 0) {
		return nil
	}
	// Capture kind once before publishing added. Subsequent fragments reuse that
	// classification, while ordinary accumulated prefixes can be replayed after
	// this delayed announcement.
	state.toolAnnounced[idx] = true
	customName, isCustom := customToolCallName(stored.Function.Name, state.CustomTools, state.FunctionTools, state.NamespaceTools)
	isToolSearch := !isCustom && state.ToolSearchDeclared && stored.Function.Name == toolSearchProxyName
	state.toolIsCustom[idx] = isCustom
	state.toolIsToolSearch[idx] = isToolSearch
	itemType := "function_call"
	if isCustom {
		itemType = "custom_tool_call"
	}
	if isToolSearch {
		itemType = "tool_search_call"
	}
	// Namespace children use the function-call lifecycle but need their unqualified
	// name and namespace on added/done items for Codex dispatch.
	itemName, itemNamespace := stored.Function.Name, ""
	if isCustom {
		itemName = customName
	}
	if ns, ok := state.NamespaceTools[stored.Function.Name]; ok && !isCustom && !isToolSearch {
		state.toolNamespace[idx] = ns
		itemName, itemNamespace = ns.Name, ns.Namespace
	}
	events := []ResponsesStreamEvent{chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
		OutputIndex: state.ToolOutputIndex[idx],
		Item: &ResponsesOutput{
			Type:      itemType,
			ID:        state.ToolItemIDs[idx],
			CallID:    stored.ID,
			Name:      itemName,
			Namespace: itemNamespace,
			Status:    "in_progress",
		},
	})}
	// Earlier ordinary fragments waited for selected identity. Replay that prefix
	// after added so its target exists; custom wrappers/search arguments still
	// wait for whole-input completion.
	if !isCustom && !isToolSearch && stored.Function.Arguments != "" {
		events = append(events, chatToResponsesEvent(state, "response.function_call_arguments.delta", &ResponsesStreamEvent{
			OutputIndex: state.ToolOutputIndex[idx],
			ItemID:      state.ToolItemIDs[idx],
			Delta:       stored.Function.Arguments,
			CallID:      stored.ID,
			Name:        stored.Function.Name,
		}))
	}
	return events
}

// closeChatToolItems emits completion for existing nonnil calls whose integer
// keys fall from zero through len(ToolCalls)-1 and whose item-ID keys exist.
// Sparse/negative keys outside that scan can be omitted. Nonnil constructed
// state is required; pending calls are forced through announcement first.
//
// Blank arguments become an object. Custom emits complete unwrapped input,
// search emits item-done alone, and ordinary functions emit full arguments-
// done plus item-done using captured namespace. No JSON validation occurs.
// Records and flags remain, so direct repeated calls can emit again; public
// finalization owns repetition control. Lifecycle IDs are reused here,
// distinct from regenerated terminal snapshot IDs.
func closeChatToolItems(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if len(state.ToolCalls) == 0 {
		return nil
	}
	var events []ResponsesStreamEvent
	// Completion scans the assumed contiguous source keys, not arbitrary map
	// keys. Missing/nil records or item-ID keys have no modeled lifecycle to
	// close on this path.
	for i := range len(state.ToolCalls) {
		toolCall, ok := state.ToolCalls[i]
		if !ok || toolCall == nil {
			continue
		}
		itemID, opened := state.ToolItemIDs[i]
		if !opened {
			continue
		}
		// Pending calls must be announced before their completion events. Force
		// removes the name wait while retaining current classification rules for the
		// emitted kind.
		events = append(events, announceChatToolItem(state, i, toolCall, true)...)
		arguments := toolCall.Function.Arguments
		// A blank argument buffer still needs an object representation on closure.
		// Nonblank content is retained unchanged; caller validation determines
		// whether ordinary JSON is usable.
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		outputIndex := state.ToolOutputIndex[i]
		if state.toolIsCustom[i] {
			// Decode and emit the complete custom input here, after all argument fragments
			// have arrived in ChatCompletionsChunkToResponsesEvents.
			input := extractCustomToolCallInput(arguments)
			if input != "" {
				events = append(events, chatToResponsesEvent(state, "response.custom_tool_call_input.delta", &ResponsesStreamEvent{
					OutputIndex: outputIndex,
					ItemID:      itemID,
					Delta:       input,
				}))
			}
			events = append(events,
				chatToResponsesEvent(state, "response.custom_tool_call_input.done", &ResponsesStreamEvent{
					OutputIndex: outputIndex,
					ItemID:      itemID,
					CallID:      toolCall.ID,
					Name:        customNameForStreamTool(state, toolCall.Function.Name),
					Input:       input,
				}),
				chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
					OutputIndex: outputIndex,
					Item: &ResponsesOutput{
						Type:   "custom_tool_call",
						ID:     itemID,
						CallID: toolCall.ID,
						Name:   customNameForStreamTool(state, toolCall.Function.Name),
						Input:  input,
						Status: "completed",
					},
				}),
			)
			continue
		}
		if state.toolIsToolSearch[i] {
			// Client search retains one complete argument representation on item.done.
			// Its specialized item codec supplies execution/argument shape, so no
			// separate argument deltas are produced.
			events = append(events, chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
				OutputIndex: outputIndex,
				Item: &ResponsesOutput{
					Type:      "tool_search_call",
					ID:        itemID,
					CallID:    toolCall.ID,
					Arguments: arguments,
					Status:    "completed",
				},
			}))
			continue
		}
		// The captured namespace pair supplies the same restored identity at done.
		// Use it instead of reinterpreting a later raw name that may have changed
		// after announcement.
		name, namespace := toolCall.Function.Name, ""
		if ns, ok := state.toolNamespace[i]; ok {
			name, namespace = ns.Name, ns.Namespace
		}
		events = append(events,
			chatToResponsesEvent(state, "response.function_call_arguments.done", &ResponsesStreamEvent{
				OutputIndex: outputIndex,
				ItemID:      itemID,
				CallID:      toolCall.ID,
				Name:        name,
				Arguments:   arguments,
			}),
			chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
				OutputIndex: outputIndex,
				Item: &ResponsesOutput{
					Type:      "function_call",
					ID:        itemID,
					CallID:    toolCall.ID,
					Name:      name,
					Namespace: namespace,
					Arguments: arguments,
					Status:    "completed",
				},
			}),
		)
	}
	return events
}

// chatOutput rebuilds terminal reasoning, message, then calls from current
// state. Nonnil state/runtime random callback are required. Reasoning/tool
// items get new IDs, while a present message ID is reused; its generated
// fallback is evaluated eagerly even when that ID exists.
//
// The grouped snapshot does not follow lifecycle output indexes and scans
// calls only from zero through len(ToolCalls)-1, omitting sparse/negative
// keys. It can contain empty message output with no text lifecycle when no
// tools exist. Captured kinds/namespaces select specialized payloads, but
// current metadata and buffers supply values; argument validity is not
// checked. Building retains state and can produce different generated IDs on
// another call.
func (state *ChatCompletionsToResponsesStreamState) chatOutput(runtime Runtime) []ResponsesOutput {
	// The terminal snapshot groups current buffers rather than reusing closed
	// event items. Reasoning/tool IDs are generated again, so lifecycle
	// identities are not preserved for those entries.
	var outputs []ResponsesOutput
	if state.Reasoning.Len() > 0 {
		outputs = append(outputs, ResponsesOutput{
			Type: "reasoning",
			ID:   generateItemID(runtime),
			Summary: []ResponsesSummary{{
				Type: "summary_text",
				Text: state.Reasoning.String(),
			}},
		})
	}
	// The message ID can be retained, but Go evaluates its generated alternative
	// before nonEmpty chooses. A random callback is therefore consumed even with
	// an existing message identity.
	if state.MessageItemID != "" || len(state.ToolCalls) == 0 {
		outputs = append(outputs, ResponsesOutput{
			Type: "message",
			ID:   nonEmpty(state.MessageItemID, generateItemID(runtime)),
			Role: "assistant",
			Content: []ResponsesContentPart{{
				Type: "output_text",
				Text: state.Text.String(),
			}},
			Status: "completed",
		})
	}
	// Match the closer's contiguous-key assumption. Sparse or negative map
	// entries outside this integer range do not contribute to the reconstructed
	// terminal output.
	for i := range len(state.ToolCalls) {
		toolCall, ok := state.ToolCalls[i]
		if !ok || toolCall == nil {
			continue
		}
		arguments := toolCall.Function.Arguments
		// Empty call buffers retain the same object placeholder as lifecycle
		// completion. Other text is kept for its captured kind, without validating
		// JSON on reconstruction.
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		if state.toolIsCustom[i] {
			outputs = append(outputs, ResponsesOutput{
				Type:   "custom_tool_call",
				ID:     generateItemID(runtime),
				CallID: toolCall.ID,
				Name:   customNameForStreamTool(state, toolCall.Function.Name),
				Input:  extractCustomToolCallInput(arguments),
				Status: "completed",
			})
			continue
		}
		if state.toolIsToolSearch[i] {
			outputs = append(outputs, ResponsesOutput{
				Type:      "tool_search_call",
				ID:        generateItemID(runtime),
				CallID:    toolCall.ID,
				Arguments: arguments,
				Status:    "completed",
			})
			continue
		}
		name, namespace := toolCall.Function.Name, ""
		if ns, ok := state.toolNamespace[i]; ok {
			name, namespace = ns.Name, ns.Namespace
		}
		outputs = append(outputs, ResponsesOutput{
			Type:      "function_call",
			ID:        generateItemID(runtime),
			CallID:    toolCall.ID,
			Name:      name,
			Namespace: namespace,
			Arguments: arguments,
			Status:    "completed",
		})
	}
	return outputs
}

// chatToResponsesEvent copies a nonnil template, installs supplied
// type/current sequence, and advances nonnil state. Nested pointers/slices
// remain shared with the template; callers serialize event construction and
// preserve emitted payload storage.
func chatToResponsesEvent(
	state *ChatCompletionsToResponsesStreamState,
	eventType string,
	template *ResponsesStreamEvent,
) ResponsesStreamEvent {
	seq := state.SequenceNumber
	state.SequenceNumber++
	evt := *template
	evt.Type = eventType
	evt.SequenceNumber = seq
	return evt
}

// rawString trims raw bytes and returns only string-decodable content, with
// absent/null/failure becoming empty text. Nonstring valid JSON also yields
// empty text, so consumers cannot distinguish it from an absent or empty
// string through this helper.
func rawString(raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Nonstring or malformed fields supply no accepted text through this decoder.
	// Return the same empty sentinel used by missing fields so consumers can
	// apply their stated defaults.
	return ""
}

// rawNestedString decodes an object and selects one member through rawString.
// Map-decode failure returns empty text; missing/nonstring members likewise
// yield empty, and this helper does not validate other object fields.
func rawNestedString(raw json.RawMessage, key string) string {
	var obj map[string]json.RawMessage
	// A failed object decode supplies no selected nested string. Return empty so
	// callers can retain their primary representation or choose their declared
	// missing-field behavior.
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	return rawString(obj[key])
}

// bytesTrimSpace trims surrounding Unicode whitespace through string
// conversion and returns raw bytes.
func bytesTrimSpace(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

// nonEmpty returns a nonempty supplied value or the already-evaluated fallback
// string. Callers compute fallback before invoking this helper, so selecting
// value cannot defer or suppress a callback used to build that alternative.
func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
