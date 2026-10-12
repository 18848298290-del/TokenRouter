package modelcatalog

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// Attributes 描述模型属性和可配置能力。指针区分未知、false 和空集合。
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_attributes
type Attributes struct {
	DisplayNameLocalization *locale.Update[string] `json:"display_name_localization,omitempty"`
	DisplayName             *string                `json:"display_name,omitempty"`
	Context                 *int                   `json:"context,omitempty"`
	InputLimit              *int                   `json:"input_limit,omitempty"`
	OutputLimit             *int                   `json:"output_limit,omitempty"`
	InputModalities         *[]string              `json:"input_modalities,omitempty"`
	OutputModalities        *[]string              `json:"output_modalities,omitempty"`
	Reasoning               *bool                  `json:"reasoning,omitempty"`
	ToolCall                *bool                  `json:"tool_call,omitempty"`
	StructuredOutput        *bool                  `json:"structured_output,omitempty"`
	Temperature             *bool                  `json:"temperature,omitempty"`
	Attachment              *bool                  `json:"attachment,omitempty"`
	TopP                    *bool                  `json:"top_p,omitempty"`
	ReasoningOptions        *[]ReasoningOption     `json:"reasoning_options,omitempty"`
	Interleaved             *Interleaved           `json:"interleaved,omitempty"`
	Conditions              *ParameterConditions   `json:"conditions,omitempty"`
	Media                   *MediaCapabilities     `json:"media,omitempty"`
}

// Presentation 是模型信息的展示值，不表达网关准入决策。
type Presentation struct {
	SearchTerms            []string           `json:"search_terms,omitempty"`
	LocalizationResolution *locale.Resolution `json:"localization_resolution,omitempty"`
	Attributes
	RouteDifferences bool `json:"route_differences,omitempty"`
}

// fields 通过值编码复制所有可选字段，避免展示消费者改写目录快照。
func (a Attributes) fields() map[string]json.RawMessage {
	body, _ := json.Marshal(a)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	return fields
}

// Merge 按字段覆盖，未填写的字段继续继承，数组整体替换。
func Merge(base, patch Attributes) Attributes {
	if patch.DisplayNameLocalization != nil {
		base.DisplayNameLocalization = patch.DisplayNameLocalization
	}
	if patch.DisplayName != nil {
		base.DisplayName = patch.DisplayName
	}
	if patch.Context != nil {
		base.Context = patch.Context
	}
	if patch.InputLimit != nil {
		base.InputLimit = patch.InputLimit
	}
	if patch.OutputLimit != nil {
		base.OutputLimit = patch.OutputLimit
	}
	if patch.InputModalities != nil {
		base.InputModalities = patch.InputModalities
	}
	if patch.OutputModalities != nil {
		base.OutputModalities = patch.OutputModalities
	}
	if patch.Reasoning != nil {
		base.Reasoning = patch.Reasoning
	}
	if patch.ToolCall != nil {
		base.ToolCall = patch.ToolCall
	}
	if patch.StructuredOutput != nil {
		base.StructuredOutput = patch.StructuredOutput
	}
	if patch.Temperature != nil {
		base.Temperature = patch.Temperature
	}
	if patch.Attachment != nil {
		base.Attachment = patch.Attachment
	}
	if patch.TopP != nil {
		base.TopP = patch.TopP
	}
	if patch.ReasoningOptions != nil {
		base.ReasoningOptions = patch.ReasoningOptions
	}
	if patch.Interleaved != nil {
		base.Interleaved = patch.Interleaved
	}
	if patch.Conditions != nil {
		base.Conditions = patch.Conditions
	}
	base.Media = mergeMedia(base.Media, patch.Media)
	return base.Clone()
}

// ApplyAttributeSupplement 从模型补充条目的 attributes 中填入缺失字段。
// 目录中的 false 和空数组优先，同名中继继续使用各自的属性。
func (c *Catalog) ApplyAttributeSupplement(model string, body json.RawMessage) error {
	var supplement struct {
		Provider   string      `json:"provider"`
		Attributes *Attributes `json:"attributes"`
	}
	if err := json.Unmarshal(body, &supplement); err != nil {
		return fmt.Errorf("invalid model supplement %s: %w", model, err)
	}
	if supplement.Attributes == nil {
		return nil
	}
	if err := supplement.Attributes.Validate(); err != nil {
		return fmt.Errorf("invalid model attributes %s: %w", model, err)
	}
	key := normalize(model)
	if public, exists := c.publicEntries[key]; exists && (supplement.Provider == "" || supplement.Provider == public.Provider) {
		public = fillAttributeSupplement(public, *supplement.Attributes)
		c.publicEntries[key] = public
	}
	entry, exists := c.Entries[key]
	if c.Ambiguous[key] || (exists && supplement.Provider != "" && entry.Provider != supplement.Provider) {
		return nil
	}
	if !exists {
		entry = Entry{Model: model, Provider: supplement.Provider, Source: "local_supplement"}
	}
	c.Entries[key] = fillAttributeSupplement(entry, *supplement.Attributes)
	return nil
}

func fillAttributeSupplement(entry Entry, attributes Attributes) Entry {
	known := entry.Attributes.fields()
	entry.AttributeSources = maps.Clone(entry.AttributeSources)
	if entry.AttributeSources == nil {
		entry.AttributeSources = map[string]string{}
	}
	for field := range attributes.fields() {
		if _, found := known[field]; !found {
			entry.AttributeSources[field] = "local_supplement"
		}
	}
	entry.Attributes = Merge(attributes, entry.Attributes)
	return entry
}

// Validate 校验管理员编辑值；目录中的零长度由解析器转换为未知。
func (a Attributes) Validate() error {
	if err := validateCapabilities(a); err != nil {
		return err
	}
	for key, value := range map[string]*int{"context": a.Context, "input_limit": a.InputLimit, "output_limit": a.OutputLimit} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("%s must be positive", key)
		}
	}
	for _, values := range []*[]string{a.InputModalities, a.OutputModalities} {
		if values == nil {
			continue
		}
		seen := map[string]bool{}
		for _, value := range *values {
			if !slices.Contains([]string{"text", "image", "audio", "video", "pdf"}, value) || seen[value] {
				return fmt.Errorf("invalid or duplicate modality: %s", value)
			}
			seen[value] = true
		}
	}
	return nil
}

// Common 取多条可请求路线的共同能力和已知最小上限，只供展示和导出。
func Common(values []Attributes) (Attributes, bool) {
	if len(values) == 0 {
		return Attributes{}, false
	}
	result := Merge(Attributes{}, values[0])
	first := values[0]
	different := false
	for _, value := range values[1:] {
		different = different || !reflect.DeepEqual(value, first)
		result.Context = minimum(result.Context, value.Context)
		result.InputLimit = minimum(result.InputLimit, value.InputLimit)
		result.OutputLimit = minimum(result.OutputLimit, value.OutputLimit)
		result.Reasoning = commonBool(result.Reasoning, value.Reasoning)
		result.ToolCall = commonBool(result.ToolCall, value.ToolCall)
		result.StructuredOutput = commonBool(result.StructuredOutput, value.StructuredOutput)
		result.Temperature = commonBool(result.Temperature, value.Temperature)
		result.Attachment = commonBool(result.Attachment, value.Attachment)
		result.TopP = commonBool(result.TopP, value.TopP)
		result.ReasoningOptions = commonReasoningOptions(result.ReasoningOptions, value.ReasoningOptions)
		var unavailable []string
		result.Conditions, unavailable = commonConditions(result.Conditions, value.Conditions)
		for _, field := range unavailable {
			switch field {
			case "temperature":
				result.Temperature = valuePointer(false)
			case "top_p":
				result.TopP = valuePointer(false)
			case "tool_call":
				result.ToolCall = valuePointer(false)
			}
		}
		result.Media = commonMedia(result.Media, value.Media)
		if !equalInterleaved(result.Interleaved, value.Interleaved) {
			result.Interleaved = nil
		}
		result.InputModalities = intersection(result.InputModalities, value.InputModalities)
		result.OutputModalities = intersection(result.OutputModalities, value.OutputModalities)
		if result.DisplayName == nil || value.DisplayName == nil || *result.DisplayName != *value.DisplayName {
			result.DisplayName = nil
		}
	}
	return result.Clone(), different
}

func minimum(a, b *int) *int {
	if a == nil {
		return b
	}
	if b != nil && *b < *a {
		return b
	}
	return a
}

func commonBool(a, b *bool) *bool {
	if a != nil && !*a || b != nil && !*b {
		value := false
		return &value
	}
	if a == nil || b == nil {
		return nil
	}
	return a
}

func intersection(a, b *[]string) *[]string {
	if a == nil || b == nil {
		return nil
	}
	result := []string{}
	for _, value := range *a {
		if slices.Contains(*b, value) {
			result = append(result, value)
		}
	}
	return &result
}
