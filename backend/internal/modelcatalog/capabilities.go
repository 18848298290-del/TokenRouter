package modelcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

// ReasoningOption 对应目录的推理开关、档位或 token 预算。nil 档位表示供应商默认值。
type ReasoningOption struct {
	Type   string    `json:"type"`
	Values []*string `json:"values,omitempty"`
	Min    *int      `json:"min,omitempty"`
	Max    *int      `json:"max,omitempty"`
}

// Interleaved 保存目录的 true 或指定 reasoning 字段。
type Interleaved struct {
	Field string `json:"field,omitempty"`
}

// ParameterCondition 限定可调参数的请求状态；多个条件同时满足时参数可用。
type ParameterCondition struct {
	Efforts  []string `json:"efforts,omitempty"`
	Thinking *bool    `json:"thinking,omitempty"`
}

type ParameterConditions map[string]ParameterCondition

// MediaCapabilities 包含平台已经实现的媒体操作与细分参数。
type MediaCapabilities struct {
	VideoOperations    []string `json:"video_operations,omitempty"`
	VideoParameters    []string `json:"video_parameters,omitempty"`
	ImageSizes         []string `json:"image_sizes,omitempty"`
	AspectRatios       []string `json:"aspect_ratios,omitempty"`
	Qualities          []string `json:"qualities,omitempty"`
	BackgroundOptions  []string `json:"background_options,omitempty"`
	OutputFormats      []string `json:"output_formats,omitempty"`
	MaxReferenceImages *int     `json:"max_reference_images,omitempty"`
	MaxOutputCount     *int     `json:"max_output_count,omitempty"`
	InputFidelity      *bool    `json:"input_fidelity,omitempty"`
}

// MarshalJSON 区分未配置的列表与管理员清空的列表。
func (m MediaCapabilities) MarshalJSON() ([]byte, error) {
	type plain MediaCapabilities
	body, err := json.Marshal(plain(m))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	for name, values := range map[string][]string{
		"image_sizes": m.ImageSizes, "aspect_ratios": m.AspectRatios,
		"qualities": m.Qualities, "background_options": m.BackgroundOptions, "output_formats": m.OutputFormats, "video_operations": m.VideoOperations, "video_parameters": m.VideoParameters,
	} {
		if values != nil && len(values) == 0 {
			fields[name] = json.RawMessage(`[]`)
		}
	}
	return json.Marshal(fields)
}

func (v Interleaved) MarshalJSON() ([]byte, error) {
	if v.Field == "" {
		return []byte("true"), nil
	}
	return json.Marshal(struct {
		Field string `json:"field"`
	}{v.Field})
}

func (v *Interleaved) UnmarshalJSON(body []byte) error {
	if bytes.Equal(bytes.TrimSpace(body), []byte("true")) {
		v.Field = ""
		return nil
	}
	var value struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	if value.Field != "reasoning_content" && value.Field != "reasoning_details" {
		return fmt.Errorf("invalid interleaved reasoning field")
	}
	v.Field = value.Field
	return nil
}

// EffortSupport 返回指定档位是否被声明支持；nil 表示目录和配置均未声明。
func (a Attributes) EffortSupport(effort string) *bool {
	if a.Reasoning != nil && !*a.Reasoning {
		return valuePointer(false)
	}
	if a.ReasoningOptions == nil {
		return nil
	}
	for _, option := range *a.ReasoningOptions {
		if option.Type != "effort" {
			continue
		}
		for _, value := range option.Values {
			if value != nil && *value == effort {
				return valuePointer(true)
			}
		}
	}
	return valuePointer(false)
}

func (a Attributes) Efforts() []string {
	if a.Reasoning != nil && !*a.Reasoning {
		return nil
	}
	var result []string
	if a.ReasoningOptions != nil {
		for _, option := range *a.ReasoningOptions {
			if option.Type == "effort" {
				for _, value := range option.Values {
					if value != nil {
						result = append(result, *value)
					}
				}
			}
		}
	}
	return result
}

func (a Attributes) ReasoningBudget() (minimum, maximum *int) {
	if a.ReasoningOptions != nil {
		for _, option := range *a.ReasoningOptions {
			if option.Type == "budget_tokens" {
				return option.Min, option.Max
			}
		}
	}
	return nil, nil
}

func (a Attributes) HasReasoningOption(kind string) bool {
	return a.ReasoningOptions != nil && slices.ContainsFunc(*a.ReasoningOptions, func(option ReasoningOption) bool { return option.Type == kind })
}

func validateCapabilities(a Attributes) error {
	if a.ReasoningOptions != nil {
		seen := map[string]bool{}
		for _, option := range *a.ReasoningOptions {
			if seen[option.Type] {
				return fmt.Errorf("duplicate reasoning option %q", option.Type)
			}
			seen[option.Type] = true
			switch option.Type {
			case "toggle":
				if option.Values != nil || option.Min != nil || option.Max != nil {
					return fmt.Errorf("toggle cannot declare effort values or budgets")
				}
			case "effort":
				if option.Min != nil || option.Max != nil {
					return fmt.Errorf("effort cannot declare a token budget")
				}
				values := map[string]bool{}
				for _, value := range option.Values {
					key := "null"
					if value != nil {
						key = *value
					}
					if !slices.Contains([]string{"null", "default", "none", "minimal", "low", "medium", "high", "xhigh", "max"}, key) || values[key] {
						return fmt.Errorf("invalid or duplicate reasoning effort %q", key)
					}
					values[key] = true
				}
			case "budget_tokens":
				if option.Values != nil || option.Min != nil && *option.Min < -1 || option.Max != nil && *option.Max < 0 || option.Min != nil && option.Max != nil && *option.Min > *option.Max {
					return fmt.Errorf("invalid reasoning token budget")
				}
			default:
				return fmt.Errorf("unknown reasoning option %q", option.Type)
			}
		}
	}
	if a.Conditions != nil {
		for key, condition := range *a.Conditions {
			if !slices.Contains([]string{"temperature", "top_p", "tool_call"}, key) {
				return fmt.Errorf("unsupported conditional capability %q", key)
			}
			for _, effort := range condition.Efforts {
				if !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "default"}, effort) {
					return fmt.Errorf("invalid conditional reasoning effort %q", effort)
				}
			}
		}
	}
	if a.Media != nil {
		for _, limit := range []*int{a.Media.MaxReferenceImages, a.Media.MaxOutputCount} {
			if limit != nil && *limit < 0 {
				return fmt.Errorf("media limits must be nonnegative")
			}
		}
	}
	return nil
}

func commonReasoningOptions(a, b *[]ReasoningOption) *[]ReasoningOption {
	if a == nil || b == nil {
		return nil
	}
	result := []ReasoningOption{}
	for _, left := range *a {
		for _, right := range *b {
			if left.Type != right.Type {
				continue
			}
			option := ReasoningOption{Type: left.Type}
			switch left.Type {
			case "effort":
				option.Values = []*string{}
				for _, value := range left.Values {
					if slices.ContainsFunc(right.Values, func(candidate *string) bool { return reflect.DeepEqual(value, candidate) }) {
						option.Values = append(option.Values, value)
					}
				}
			case "budget_tokens":
				option.Min = maximum(left.Min, right.Min)
				option.Max = minimum(left.Max, right.Max)
				if option.Min != nil && option.Max != nil && *option.Min > *option.Max {
					continue
				}
			}
			result = append(result, option)
		}
	}
	return &result
}

func commonConditions(a, b *ParameterConditions) (*ParameterConditions, []string) {
	result := ParameterConditions{}
	if a != nil {
		maps.Copy(result, *a)
	}
	var unavailable []string
	if b != nil {
		for key, condition := range *b {
			prior, exists := result[key]
			if !exists {
				result[key] = condition
				continue
			}
			if prior.Thinking != nil && condition.Thinking != nil && *prior.Thinking != *condition.Thinking {
				unavailable = append(unavailable, key)
				delete(result, key)
				continue
			}
			if prior.Thinking != nil {
				condition.Thinking = prior.Thinking
			}
			if len(prior.Efforts) > 0 {
				if len(condition.Efforts) == 0 {
					condition.Efforts = slices.Clone(prior.Efforts)
				} else {
					condition.Efforts = commonStrings(prior.Efforts, condition.Efforts)
					if len(condition.Efforts) == 0 {
						unavailable = append(unavailable, key)
						delete(result, key)
						continue
					}
				}
			}
			result[key] = condition
		}
	}
	if a == nil && b == nil {
		return nil, nil
	}
	return &result, unavailable
}

func commonMedia(a, b *MediaCapabilities) *MediaCapabilities {
	if a == nil || b == nil {
		return nil
	}
	return &MediaCapabilities{
		VideoOperations: commonStrings(a.VideoOperations, b.VideoOperations), VideoParameters: commonStrings(a.VideoParameters, b.VideoParameters),
		ImageSizes: commonStrings(a.ImageSizes, b.ImageSizes), AspectRatios: commonStrings(a.AspectRatios, b.AspectRatios),
		Qualities: commonStrings(a.Qualities, b.Qualities), BackgroundOptions: commonStrings(a.BackgroundOptions, b.BackgroundOptions),
		OutputFormats: commonStrings(a.OutputFormats, b.OutputFormats), MaxReferenceImages: minimum(a.MaxReferenceImages, b.MaxReferenceImages),
		MaxOutputCount: minimum(a.MaxOutputCount, b.MaxOutputCount), InputFidelity: commonBool(a.InputFidelity, b.InputFidelity),
	}
}

func commonStrings(a, b []string) []string {
	if a == nil || b == nil {
		return nil
	}
	return *intersection(&a, &b)
}

func maximum(a, b *int) *int {
	if a == nil || b != nil && *b > *a {
		return b
	}
	return a
}

func valuePointer[T any](value T) *T { return &value }
