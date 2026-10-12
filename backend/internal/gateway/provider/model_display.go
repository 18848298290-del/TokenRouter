package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeldisplay"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// ModelDisplayCatalogue 将统一目录条目转换为客户端展示字段。
type ModelDisplayCatalogue struct{ Source modelcatalog.Reader }

func (c ModelDisplayCatalogue) ModelAttributes(model string) modelcatalog.Attributes {
	if c.Source == nil {
		return modelcatalog.Attributes{}
	}
	return c.Source.ModelEntry(model).Attributes
}

// Model 保留请求 ID，名称和厂商按完整身份查询统一目录。
func (c ModelDisplayCatalogue) Model(id string) modeldisplay.OpenAIModel {
	model := modeldisplay.OpenAIModel{ID: id, Object: "model", Type: "model", OwnedBy: "tokenrouter", DisplayName: id}
	if c.Source != nil {
		entry := c.Source.ModelEntry(id)
		if entry.Provider != "" {
			model.OwnedBy = entry.Provider
		}
		if entry.Attributes.DisplayName != nil && strings.TrimSpace(*entry.Attributes.DisplayName) != "" {
			model.DisplayName = *entry.Attributes.DisplayName
		}
	}
	return model
}

// GeminiModel 生成已通过协议检查的 Gemini 模型资源。
func (c ModelDisplayCatalogue) GeminiModel(id string, _ bool) modeldisplay.GeminiModel {
	id = strings.TrimPrefix(id, "models/")
	return modeldisplay.GeminiModel{Name: "models/" + id, DisplayName: c.Model(id).DisplayName, SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"}}
}
