package bridge

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Thinking requests arrive in several client formats, while resolved Gemini
// models expose either budgets, effort levels, or no adjustable controls.
// Parsing keeps explicit disabling and zero budgets distinct from absent
// client input, and capability resolution distinguishes unknown controls from
// a known empty option set.
//
// The selected control is a budget or a level, so the request cannot send both
// fields. Positive budgets combine model minimums, maximums, and reserved
// output space; dynamic -1 and disabled zero retain their special meanings.
// Effort levels select a supported rank when capabilities are known, while
// unknown controls pass effort spelling through. These decisions use final-
// model capabilities assembled by provider.GeminiThinking.

// GeminiThinkingInput holds parsed client controls before model-specific
// budget or level selection. ParseGeminiThinkingInput supplies normalized
// effort spelling and keeps absent booleans and budgets distinguishable from
// explicit values.
type GeminiThinkingInput struct {
	// Enabled is nil when the client gives no recognized toggle; false takes
	// precedence over effort or budget selection in ResolveGeminiThinking.
	Enabled *bool
	Effort  string // normalized client effort label
	// Budget points to the client's token request; -1 selects dynamic budgeting
	// and explicit zero is interpreted by ParseGeminiThinkingInput as disabling.
	Budget *int
	// Adaptive identifies exact thinking.type=adaptive so a positive
	// AdaptiveBudget can replace client budget selection under the resolver rules.
	Adaptive bool
}

// GeminiThinkingOptions supplies final-model capabilities and request rules to
// ResolveGeminiThinking. Numeric limits are read during resolution, and
// caller-owned pointers and slices remain shared.
type GeminiThinkingOptions struct {
	// ControlsKnown makes Toggle, Efforts, and Budget authoritative when true;
	// false permits client controls whose support has not been enumerated.
	ControlsKnown bool
	// Reasoning rejects enabled input when false; disabling an already
	// nonreasoning model returns no configuration. Nil leaves support unspecified.
	Reasoning *bool
	Toggle    bool     // model accepts disabling control
	Efforts   []string // supported effort level labels
	Budget    bool     // model accepts token budgets
	// BudgetMin and BudgetMax constrain positive budgets together; nil leaves
	// the corresponding side unbounded. Dynamic -1 and disabled zero bypass
	// these limits so they keep their distinct wire meanings.
	BudgetMin, BudgetMax *int // BudgetMin minimum budget tokens; BudgetMax maximum budget tokens
	// ForceBudget chooses the budget representation even when supported efforts
	// exist, as required by provider.GeminiThinking for Antigravity Claude targets.
	ForceBudget bool
	// AdaptiveBudget is preferred for adaptive input with an empty Effort;
	// otherwise an explicit Budget or nondefault Effort is evaluated first.
	AdaptiveBudget int
	// OutputLimit and BudgetPadding bound positive budgets when padding is
	// positive. Their difference can further lower BudgetMax or leave no valid
	// value above BudgetMin, which ResolveGeminiThinking reports as an error.
	OutputLimit   *int // model output token limit
	BudgetPadding int  // reserved output token capacity
}

// ParseGeminiThinkingInput reads client toggles, effort labels and optional
// budgets from Messages, Responses or Chat request JSON. Malformed input
// returns absent controls, and recognized thinking.type values select an
// explicit enabled or disabled state.
//
// Effort precedence is output_config, reasoning, then reasoning_effort after
// trimming and lowercasing. A none effort or explicit zero budget overrides
// an enabled type with Enabled=false, while an omitted budget stays nil. This
// distinction lets ResolveGeminiThinking leave absent controls to upstream
// defaults and encode a supported disabled request as zero instead.
func ParseGeminiThinkingInput(body []byte) GeminiThinkingInput {
	var input struct {
		// Thinking retains an optional toggle and optional token budget; absence
		// remains distinguishable from an explicitly supplied zero budget.
		Thinking *struct {
			Type   string `json:"type"`          // client thinking mode label
			Budget *int   `json:"budget_tokens"` // optional client token budget
		} `json:"thinking"`
		// Reasoning supplies effort after OutputConfig; ReasoningEffort is used
		// when both nested effort fields are blank after normalization.
		Reasoning struct {
			Effort string `json:"effort"` // Responses client effort label
		} `json:"reasoning"`
		ReasoningEffort string `json:"reasoning_effort"` // Chat client effort label
		// OutputConfig is the first effort source checked, even when reasoning
		// and reasoning_effort also carry nonempty client values.
		OutputConfig struct {
			Effort string `json:"effort"` // Messages client effort label
		} `json:"output_config"`
	}
	// Treat undecodable input as absent controls here; request validation belongs
	// to the entrypoint, and resolution leaves upstream thinking defaults
	// untouched.
	if json.Unmarshal(body, &input) != nil {
		return GeminiThinkingInput{}
	}
	// Use Messages output_config ahead of Responses reasoning and Chat
	// reasoning_effort when several spellings are present, resolving them to one
	// normalized effort before capability selection.
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
	// A zero token budget or none effort disables thinking even when the thinking
	// object requested an enabled or adaptive mode.
	if result.Effort == "none" || result.Budget != nil && *result.Budget == 0 {
		value := false
		result.Enabled = &value
	}
	return result
}

// ResolveGeminiThinking selects one Gemini thinking configuration from client
// input and final-model capabilities. Absent input returns nil, while explicit
// disabling produces an encoded zero budget when supported or no config for a
// model known to lack reasoning.
//
// Enabled requests use budgets when forced, for unknown controls with an
// explicit budget, or when budget-capable models have an explicit budget or no
// efforts. Positive budgets are raised to the minimum then capped by model and
// output-space limits; incompatible limits return an error. Dynamic -1
// bypasses positive-budget limits. Level selection maps numeric budgets to
// fixed effort thresholds, maps recognized efforts to supported ranks, and
// passes levels through when controls are unknown.
func ResolveGeminiThinking(input GeminiThinkingInput, options GeminiThinkingOptions) (*GeminiThinkingConfig, error) {
	if input.Enabled == nil && input.Effort == "" && input.Budget == nil {
		return nil, nil
	}
	disabled := input.Enabled != nil && !*input.Enabled
	// Handle disabling before enabled-control admission so a model already marked
	// nonreasoning accepts the request while known toggles still govern
	// adjustable models.
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
	// Choose one wire representation from capability knowledge and client input;
	// mixed level and budget fields would otherwise encode competing thinking
	// controls.
	useBudget := options.ForceBudget || options.Budget && (input.Budget != nil || len(options.Efforts) == 0) || !options.ControlsKnown && input.Budget != nil
	if useBudget {
		budget := -1
		switch {
		// For adaptive requests with no effort, the caller's positive adaptive
		// budget takes precedence even when the client supplied its own token count.
		case input.Adaptive && options.AdaptiveBudget > 0 && input.Effort == "":
			budget = options.AdaptiveBudget
		case input.Budget != nil:
			budget = *input.Budget
		case input.Effort != "" && input.Effort != "default":
			effort, err := geminiCompatibleEffort(input.Effort, nil)
			if err != nil {
				return nil, err
			}
			// The fixed budget table has no minimal entry; map it to low before using
			// the common effort-to-token allowances.
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
		// Limit positive token counts while leaving -1 available for dynamic
		// budgeting; output reservation can make the model minimum impossible to
		// satisfy.
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
	// A level-only model maps positive requested tokens to low, medium, or high
	// using the same fixed allowances that budget-based conversion uses.
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
	// Unknown option metadata permits the supplied level spelling to reach the
	// upstream; known metadata instead limits selection to supported effort
	// ranks.
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

// geminiCompatibleEffort resolves ultra/extrahigh aliases and chooses a
// recognized effort from the supplied allowed set. An empty set accepts any
// recognized label; unsupported labels or sets containing no recognized rank
// return errors.
//
// Exact membership wins. Otherwise selection uses the greatest allowed rank no
// higher than the request, or the lowest allowed rank when every allowed value
// is higher. This ranking keeps adaptation consistent across models exposing
// different effort subsets, and discards unrelated allowed strings.
func geminiCompatibleEffort(effort string, allowed []string) (string, error) {
	order := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	// Normalize the two supported alias spellings before rank lookup so both use
	// the same fixed ordering as their canonical labels.
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
	// Seed the lowest recognized allowed rank, then advance only through ranks no
	// higher than the request to handle gaps in supported levels.
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
