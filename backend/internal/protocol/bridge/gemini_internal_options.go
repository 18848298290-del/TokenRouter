package bridge

import (
	"fmt"
	"slices"
	"strings"
)

// gemini_internal_options.go builds internal Gemini tool declarations and
// generation settings from Messages requests.
// Internal Gemini tools distinguish client functions from built-in web search
// and code execution declarations. Legacy search names are recognized
// alongside search types, schemas are cleaned in caller-owned maps, and the
// request assembler uses mixed declaration detection to request server-side
// tool invocations when functions and built-ins coexist.
//
// Generation settings combine caller-selected model limits with Messages
// output and thinking requests. Positive thinking budgets can raise output
// capacity before the final output cap applies, while dynamic -1 passes
// through the budget limiter. ReasoningModel suppresses stop sequences and
// sampling together; individual options can suppress those fields
// independently. Callers provide consistent output limits and padding when
// they require capacity beyond the chosen thinking budget.

// InternalGeminiGenerationOptions supplies model limits and request rules to
// the internal Gemini generation converter. Values are caller-resolved, and
// StopSequences storage remains shared with the returned configuration.
type InternalGeminiGenerationOptions struct {
	// DefaultOutputTokens supplies the initial limit; positive req.MaxTokens
	// replaces it, and MaxOutputTokens caps the resulting limit after budget adjustment.
	DefaultOutputTokens int // initial output token allowance
	MaxOutputTokens     int // model output token ceiling
	// ReasoningModel suppresses stop sequences and all sampling controls; the
	// independent omission options still apply when this flag is false.
	ReasoningModel bool
	// OmitStopSequences suppresses default stops when ReasoningModel is false;
	// true ReasoningModel already suppresses those stops for the returned config.
	OmitStopSequences bool
	// DropTemperature, DropTopP, and DropTopK suppress their respective request
	// pointers when ReasoningModel is false. Enabled sampling values keep their
	// original pointers in the returned config, including explicitly supplied zero.
	DropTemperature bool // caller suppresses temperature sampling
	DropTopP        bool // caller suppresses nucleus sampling
	DropTopK        bool // caller suppresses ranked sampling
	// StopSequences supplies internal default stops, not req.StopSequences;
	// configuration reuses this slice when stop-sequence policy permits it.
	StopSequences []string
	// AdaptiveThinkingBudget replaces an adaptive request's budget when positive;
	// otherwise request budget selection or dynamic -1 remains in effect.
	AdaptiveThinkingBudget int
	// ThinkingBudgetLimit caps positive thinking token counts when positive;
	// zero and negative limits leave that budget selection uncapped here.
	ThinkingBudgetLimit int
	// BudgetPadding is added when output capacity is at most a positive budget;
	// callers choose positive padding to request capacity beyond that budget.
	BudgetPadding int
}

// InternalHasWebSearchTool reports whether any declaration matches the
// internal search-tool recognition rules.
func InternalHasWebSearchTool(tools []ClaudeTool) bool {
	return slices.ContainsFunc(tools, InternalIsWebSearchTool)
}

// InternalIsWebSearchTool reports whether a tool is internal web search by
// type or legacy name. Type prefix matching is untrimmed; recognized names are
// trimmed before exact comparison, so named client functions can be promoted
// here.
func InternalIsWebSearchTool(tool ClaudeTool) bool {
	if strings.HasPrefix(tool.Type, "web_search") || tool.Type == "google_search" {
		return true
	}

	name := strings.TrimSpace(tool.Name)
	switch name {
	case "web_search", "google_search", "web_search_20250305":
		return true
	default:
		return false
	}
}

// InternalIsCodeExecutionTool reports whether a trimmed tool type selects
// built-in code execution.
func InternalIsCodeExecutionTool(tool ClaudeTool) bool {
	return strings.TrimSpace(tool.Type) == "code_execution"
}

// InternalHasMixedToolInvocations reports whether client functions and
// built-in search or code declarations coexist. The declarations can contain
// these kinds together or in separate elements; presence in either arrangement
// counts.
//
// The request assembler sets includeServerSideToolInvocations from this result
// so mixed client/server tool use is represented in the internal request.
// Empty function lists do not establish function presence, while a nonnil
// built-in declaration establishes built-in presence. Either kind alone
// returns false.
func InternalHasMixedToolInvocations(declarations []GeminiToolDeclaration) bool {
	hasFunctions, hasBuiltin := false, false
	for _, declaration := range declarations {
		if len(declaration.FunctionDeclarations) > 0 {
			hasFunctions = true
		}
		if declaration.GoogleSearch != nil || declaration.CodeExecution != nil {
			hasBuiltin = true
		}
	}
	return hasFunctions && hasBuiltin
}

// BuildInternalGeminiTools converts Messages tools into internal function,
// search, and code declarations plus diagnostics. The search recognizer
// accepts legacy names as well as types; search and code presence each produce
// one built-in declaration.
//
// Functions keep input ordering and supplied names, while blank names or
// missing custom schemas are skipped with diagnostics. Custom tools read their
// nested description and schema. Schema cleaning mutates supplied maps and
// removes undefined placeholders, so callers retain those changes through
// shared references. Nil schemas use an empty-object declaration, and an empty
// result returns nil with the accumulated diagnostics.
func BuildInternalGeminiTools(tools []ClaudeTool) ([]GeminiToolDeclaration, []string) {
	diagnostics := []string{}
	diagnostic := func(format string, args ...any) { diagnostics = append(diagnostics, fmt.Sprintf(format, args...)) }
	if len(tools) == 0 {
		return nil, diagnostics
	}

	hasWebSearch := InternalHasWebSearchTool(tools)
	hasCodeExecution := slices.ContainsFunc(tools, InternalIsCodeExecutionTool)

	var funcDecls []GeminiFunctionDecl
	for _, tool := range tools {
		if InternalIsWebSearchTool(tool) || InternalIsCodeExecutionTool(tool) {
			continue
		}
		// Blank client function names cannot identify an invocation; skip that
		// declaration with a diagnostic while leaving other valid tools available.
		if strings.TrimSpace(tool.Name) == "" {
			diagnostic("Warning: skipping tool with empty name")
			continue
		}

		var description string
		var inputSchema map[string]any

		// Custom Messages tools take their schema and description from the nested
		// specification; a missing schema produces a diagnostic and skips that
		// function.
		if tool.Type == "custom" {
			if tool.Custom == nil || tool.Custom.InputSchema == nil {
				diagnostic("[Warning] Skipping invalid custom tool '%s': missing custom spec or input_schema", tool.Name)
				continue
			}
			description = tool.Custom.Description
			inputSchema = tool.Custom.InputSchema
		} else {
			description = tool.Description
			inputSchema = tool.InputSchema
		}

		// Clean the shared schema before assigning Parameters, removing undefined
		// placeholders and unsupported constructs in place so the resulting
		// declaration references the adapted map.
		DeepCleanUndefined(inputSchema)
		params := CleanJSONSchema(inputSchema)
		// A nil ordinary schema still needs a function parameter object; use an
		// empty object after cleaning because there is no schema to mutate.
		if params == nil {
			params = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}

		funcDecls = append(funcDecls, GeminiFunctionDecl{
			Name:        tool.Name,
			Description: description,
			Parameters:  params,
		})
	}

	var declarations []GeminiToolDeclaration
	if len(funcDecls) > 0 {
		declarations = append(declarations, GeminiToolDeclaration{
			FunctionDeclarations: funcDecls,
		})
	}
	// Represent recognized search tools with one internal built-in declaration,
	// keeping them separate from named client functions and their parameter
	// schemas.
	if hasWebSearch {
		declarations = append(declarations, GeminiToolDeclaration{
			GoogleSearch: &GeminiGoogleSearch{
				EnhancedContent: &GeminiEnhancedContent{
					ImageSearch: &GeminiImageSearch{
						// Enhanced image search requests at most five results.
						MaxResultCount: 5,
					},
				},
			},
		})
	}
	// A code-execution type produces the built-in declaration once; its Messages
	// name and schema do not become an extra client function.
	if hasCodeExecution {
		declarations = append(declarations, GeminiToolDeclaration{
			CodeExecution: &GeminiCodeExecution{},
		})
	}
	if len(declarations) == 0 {
		return nil, diagnostics
	}

	return declarations, diagnostics
}

// BuildInternalGeminiGenerationConfig combines a nonnil Messages request with
// caller-resolved output, sampling, and thinking rules. It returns generation
// config plus diagnostics when a positive budget raises the provisional output
// limit.
//
// Enabled or adaptive thinking uses a positive requested budget, a positive
// adaptive override, or dynamic -1. Positive budgets are capped before output
// is adjusted, and the model output cap is applied afterward; that final cap
// can remove the requested margin. ReasoningModel suppresses sampling and
// default stops, while independent omission flags narrow other models. Callers
// supply compatible limits and positive padding when the final result must
// exceed its thinking budget.
func BuildInternalGeminiGenerationConfig(req *ClaudeRequest, options InternalGeminiGenerationOptions) (*GeminiGenerationConfig, []string) {
	diagnostics := []string{}
	diagnostic := func(format string, args ...any) { diagnostics = append(diagnostics, fmt.Sprintf(format, args...)) }
	maxLimit := options.MaxOutputTokens
	config := &GeminiGenerationConfig{
		MaxOutputTokens: options.DefaultOutputTokens,
	}

	isReasoning := options.ReasoningModel
	if !isReasoning && !options.OmitStopSequences {
		config.StopSequences = options.StopSequences
	}

	if req.MaxTokens > 0 {
		config.MaxOutputTokens = req.MaxTokens
	}

	if req.Thinking != nil && (req.Thinking.Type == "enabled" || req.Thinking.Type == "adaptive") {
		config.ThinkingConfig = &GeminiThinkingConfig{
			IncludeThoughts: true,
		}

		// Adaptive thinking prefers the caller's positive override; otherwise a
		// positive request budget wins, and -1 retains the upstream dynamic-budget
		// request.
		budget := -1
		if req.Thinking.BudgetTokens > 0 {
			budget = req.Thinking.BudgetTokens
		}
		if req.Thinking.Type == "adaptive" && options.AdaptiveThinkingBudget > 0 {
			budget = options.AdaptiveThinkingBudget
		}

		// Cap positive requested or adaptive tokens by ThinkingBudgetLimit; dynamic
		// -1 retains its special wire meaning and bypasses this numeric limit.
		if budget > 0 {
			if options.ThinkingBudgetLimit > 0 && budget > options.ThinkingBudgetLimit {
				budget = options.ThinkingBudgetLimit
			}

			// Request output headroom when capacity is at most the chosen budget; the
			// later model output cap can still remove that margin.
			if adjusted, ok := EnsureMaxTokensGreaterThanBudget(config.MaxOutputTokens, budget, options.BudgetPadding); ok {
				diagnostic("[Antigravity] Auto-adjusted max_tokens from %d to %d (must be > budget_tokens=%d)",
					config.MaxOutputTokens, adjusted, budget)
				config.MaxOutputTokens = adjusted
			}
		}
		config.ThinkingConfig.ThinkingBudget = budget
	}

	// Apply the model ceiling after provisional budget headroom adjustment.
	if config.MaxOutputTokens > maxLimit {
		config.MaxOutputTokens = maxLimit
	}

	// ReasoningModel suppresses all sampling knobs together; other models retain
	// supplied values only when their individual caller omission flags permit
	// them.
	if !isReasoning {
		if req.Temperature != nil && !options.DropTemperature {
			config.Temperature = req.Temperature
		}
		if req.TopP != nil && !options.DropTopP {
			config.TopP = req.TopP
		}
		if req.TopK != nil && !options.DropTopK {
			config.TopK = req.TopK
		}
	}

	return config, diagnostics
}

// EnsureMaxTokensGreaterThanBudget returns budgetTokens plus padding when a
// positive budget is at least maxTokens, and reports whether that branch
// applied. Other inputs retain maxTokens and return false; callers choose
// positive padding when they need headroom.
//
// The helper does not impose the later model output ceiling or validate
// arithmetic range. BuildInternalGeminiGenerationConfig applies its ceiling
// after this provisional result, so a caller's inconsistent ceiling can
// remove the intended margin. The adjustment flag records the comparison and
// addition, not a guarantee about the final configuration sent upstream.
func EnsureMaxTokensGreaterThanBudget(maxTokens, budgetTokens, padding int) (int, bool) {
	// Add caller-selected headroom only for an exhausted positive budget.
	if budgetTokens > 0 && maxTokens <= budgetTokens {
		return budgetTokens + padding, true
	}
	return maxTokens, false
}
