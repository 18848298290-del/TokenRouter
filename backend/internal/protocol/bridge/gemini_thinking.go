package bridge

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// GeminiThinkingInput 保存客户端明确指定的推理控制，转换中生成的默认值由调用方排除。
type GeminiThinkingInput struct {
	Enabled  *bool
	Effort   string
	Budget   *int
	Adaptive bool
}

// GeminiThinkingOptions 由目标能力解析器提供，转换器按可用字段选择 level 或 budget。
type GeminiThinkingOptions struct {
	// ControlsKnown distinguishes omitted options from an explicitly empty set.
	ControlsKnown        bool
	Reasoning            *bool
	Toggle               bool
	Efforts              []string
	Budget               bool
	BudgetMin, BudgetMax *int
	ForceBudget          bool
	AdaptiveBudget       int
	OutputLimit          *int
	BudgetPadding        int
}

func ParseGeminiThinkingInput(body []byte) GeminiThinkingInput {
	var input struct {
		Thinking *struct {
			Type   string `json:"type"`
			Budget *int   `json:"budget_tokens"`
		} `json:"thinking"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		ReasoningEffort string `json:"reasoning_effort"`
		OutputConfig    struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	if json.Unmarshal(body, &input) != nil {
		return GeminiThinkingInput{}
	}
	result := GeminiThinkingInput{Effort: strings.ToLower(strings.TrimSpace(input.OutputConfig.Effort))}
	if result.Effort == "" {
		result.Effort = strings.ToLower(strings.TrimSpace(input.Reasoning.Effort))
	}
	if result.Effort == "" {
		result.Effort = strings.ToLower(strings.TrimSpace(input.ReasoningEffort))
	}
	if input.Thinking != nil {
		result.Budget = input.Thinking.Budget
		switch input.Thinking.Type {
		case "disabled", "off", "none":
			value := false
			result.Enabled = &value
		case "enabled", "adaptive", "between_tools":
			value := true
			result.Enabled = &value
			result.Adaptive = input.Thinking.Type == "adaptive"
		}
	}
	if result.Effort == "none" || result.Budget != nil && *result.Budget == 0 {
		value := false
		result.Enabled = &value
	}
	return result
}

// ResolveGeminiThinking 生成互斥的 thinkingLevel 或 thinkingBudget，缺少控制时使用上游默认行为。
func ResolveGeminiThinking(input GeminiThinkingInput, options GeminiThinkingOptions) (*GeminiThinkingConfig, error) {
	if input.Enabled == nil && input.Effort == "" && input.Budget == nil {
		return nil, nil
	}
	disabled := input.Enabled != nil && !*input.Enabled
	if disabled {
		if options.Reasoning != nil && !*options.Reasoning {
			return nil, nil
		}
		if options.ControlsKnown && !options.Toggle {
			return nil, fmt.Errorf("selected model does not support disabling thinking")
		}
		return &GeminiThinkingConfig{ThinkingBudget: 0, BudgetSet: true}, nil
	}
	if options.Reasoning != nil && !*options.Reasoning {
		return nil, fmt.Errorf("selected model does not support thinking")
	}
	if options.ControlsKnown && !options.ForceBudget && !options.Budget && len(options.Efforts) == 0 && !options.Toggle {
		return nil, fmt.Errorf("selected model does not expose adjustable thinking")
	}
	result := &GeminiThinkingConfig{IncludeThoughts: true}
	useBudget := options.ForceBudget || options.Budget && (input.Budget != nil || len(options.Efforts) == 0) || !options.ControlsKnown && input.Budget != nil
	if useBudget {
		budget := -1
		switch {
		case input.Adaptive && options.AdaptiveBudget > 0 && input.Effort == "":
			budget = options.AdaptiveBudget
		case input.Budget != nil:
			budget = *input.Budget
		case input.Effort != "" && input.Effort != "default":
			effort, err := geminiCompatibleEffort(input.Effort, nil)
			if err != nil {
				return nil, err
			}
			if effort == "minimal" {
				effort = "low"
			}
			budget = defaultThinkingBudget(effort)
		case input.Adaptive && options.AdaptiveBudget > 0:
			budget = options.AdaptiveBudget
		}
		if budget < -1 {
			return nil, fmt.Errorf("thinking budget must be -1 or nonnegative")
		}
		if budget > 0 {
			if options.BudgetMin != nil {
				budget = max(budget, *options.BudgetMin)
			}
			maximum := options.BudgetMax
			if options.BudgetPadding > 0 && options.OutputLimit != nil {
				limit := *options.OutputLimit - options.BudgetPadding
				if maximum == nil || limit < *maximum {
					maximum = &limit
				}
			}
			if maximum != nil {
				if *maximum <= 0 || options.BudgetMin != nil && *maximum < *options.BudgetMin {
					return nil, fmt.Errorf("output limit leaves no valid thinking budget")
				}
				budget = min(budget, *maximum)
			}
		}
		result.ThinkingBudget, result.BudgetSet = budget, true
		return result, nil
	}
	effort := input.Effort
	if effort == "" && input.Budget != nil && *input.Budget > 0 {
		switch {
		case *input.Budget <= defaultThinkingBudget("low"):
			effort = "low"
		case *input.Budget <= defaultThinkingBudget("medium"):
			effort = "medium"
		default:
			effort = "high"
		}
	}
	if effort == "" || effort == "default" {
		return result, nil
	}
	if !options.ControlsKnown {
		result.ThinkingLevel = effort
		return result, nil
	}
	level, err := geminiCompatibleEffort(effort, options.Efforts)
	if err != nil {
		return nil, err
	}
	result.ThinkingLevel = level
	return result, nil
}

func geminiCompatibleEffort(effort string, allowed []string) (string, error) {
	order := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	if effort == "ultra" {
		effort = "max"
	}
	if effort == "extrahigh" {
		effort = "xhigh"
	}
	rank := slices.Index(order, effort)
	if rank < 0 {
		return "", fmt.Errorf("unsupported reasoning effort %q", effort)
	}
	if len(allowed) == 0 {
		return effort, nil
	}
	if slices.Contains(allowed, effort) {
		return effort, nil
	}
	selected := ""
	for index, candidate := range order {
		if !slices.Contains(allowed, candidate) {
			continue
		}
		if selected == "" || index <= rank {
			selected = candidate
		}
	}
	if selected == "" {
		return "", fmt.Errorf("selected model has no compatible reasoning effort")
	}
	return selected, nil
}
