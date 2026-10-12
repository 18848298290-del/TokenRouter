package bridge

import (
	"fmt"
	"slices"
	"strings"
)

// InternalGeminiGenerationOptions contains generation limits resolved by the caller.
type InternalGeminiGenerationOptions struct {
	DefaultOutputTokens    int
	MaxOutputTokens        int
	ReasoningModel         bool
	OmitStopSequences      bool
	DropTemperature        bool
	DropTopP               bool
	DropTopK               bool
	StopSequences          []string
	AdaptiveThinkingBudget int
	ThinkingBudgetLimit    int
	BudgetPadding          int
}

func InternalHasWebSearchTool(tools []ClaudeTool) bool {
	return slices.ContainsFunc(tools, InternalIsWebSearchTool)
}

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

func InternalIsCodeExecutionTool(tool ClaudeTool) bool {
	return strings.TrimSpace(tool.Type) == "code_execution"
}

// InternalHasMixedToolInvocations reports whether functions and built-in search or code tools coexist.
// Mixed declarations require includeServerSideToolInvocations.
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

// BuildInternalGeminiTools converts Claude tools to v1internal declarations and returns diagnostics.
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
		if strings.TrimSpace(tool.Name) == "" {
			diagnostic("Warning: skipping tool with empty name")
			continue
		}

		var description string
		var inputSchema map[string]any

		// Custom tools store their description and schema in a nested specification.
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

		// Remove placeholder values before adapting the schema to v1internal.
		DeepCleanUndefined(inputSchema)
		params := CleanJSONSchema(inputSchema)
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
	if hasWebSearch {
		declarations = append(declarations, GeminiToolDeclaration{
			GoogleSearch: &GeminiGoogleSearch{
				EnhancedContent: &GeminiEnhancedContent{
					ImageSearch: &GeminiImageSearch{
						MaxResultCount: 5,
					},
				},
			},
		})
	}
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

		// A positive caller-selected adaptive budget overrides the requested budget.
		// Otherwise, an absent positive budget uses the upstream dynamic value (-1).
		budget := -1
		if req.Thinking.BudgetTokens > 0 {
			budget = req.Thinking.BudgetTokens
		}
		if req.Thinking.Type == "adaptive" && options.AdaptiveThinkingBudget > 0 {
			budget = options.AdaptiveThinkingBudget
		}

		// Apply limits to positive budgets; pass the dynamic budget (-1) through.
		if budget > 0 {
			if options.ThinkingBudgetLimit > 0 && budget > options.ThinkingBudgetLimit {
				budget = options.ThinkingBudgetLimit
			}

			// Claude requires output capacity beyond the thinking budget.
			if adjusted, ok := EnsureMaxTokensGreaterThanBudget(config.MaxOutputTokens, budget, options.BudgetPadding); ok {
				diagnostic("[Antigravity] Auto-adjusted max_tokens from %d to %d (must be > budget_tokens=%d)",
					config.MaxOutputTokens, adjusted, budget)
				config.MaxOutputTokens = adjusted
			}
		}
		config.ThinkingConfig.ThinkingBudget = budget
	}

	if config.MaxOutputTokens > maxLimit {
		config.MaxOutputTokens = maxLimit
	}

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

// EnsureMaxTokensGreaterThanBudget raises maxTokens to budgetTokens + padding when needed.
// Callers supply positive padding to satisfy the Claude thinking budget constraint.
// The boolean reports whether maxTokens changed.
func EnsureMaxTokensGreaterThanBudget(maxTokens, budgetTokens, padding int) (int, bool) {
	if budgetTokens > 0 && maxTokens <= budgetTokens {
		return budgetTokens + padding, true
	}
	return maxTokens, false
}
