package grok

import (
	"strings"
	"sync/atomic"
)

const (
	// DefaultResponsesModel 是 Grok Responses 请求未指定模型时使用的默认模型。
	DefaultResponsesModel = "grok-4.5"

	// DefaultTextModel 是允许省略模型的文本请求使用的默认型号。
	DefaultTextModel = "grok-4.6"

	// 以下为官方 Imagine 模型 ID。
	DefaultImagineImageQualityModel = "grok-imagine-image-quality"
	DefaultImagineImageFastModel    = "grok-imagine-image"
	DefaultImagineImage20Model      = "grok-imagine-image-2.0"
	DefaultImagineVideoModel        = "grok-imagine-video"
	DefaultImagineVideo15Model      = "grok-imagine-video-1.5"
)

var runtimeDefaultTextModel atomic.Pointer[string]

// SetRuntimeDefaultTextModel 发布允许省略模型的请求所用的默认文本型号。
func SetRuntimeDefaultTextModel(model string) {
	runtimeDefaultTextModel.Store(&model)
}

// RuntimeDefaultTextModel 返回配置的默认文本型号，空值交由调用方选择默认值。
func RuntimeDefaultTextModel() string {
	if model := runtimeDefaultTextModel.Load(); model != nil {
		return *model
	}
	return ""
}

// NormalizeModelID 保留完整模型 ID，允许省略模型的入口使用协议默认值。
func NormalizeModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return DefaultResponsesModel
	}
	return model
}

// ResolveGrokTextResponsesModelID 仅为空模型选用默认值，非空 ID 原样保留。
func ResolveGrokTextResponsesModelID(model string, defaultText ...string) string {
	return ResolveDefaultTextModel(model, defaultText...)
}

// ResolveDefaultTextModel 在模型为空时返回 defaultText，未提供则返回 DefaultTextModel。
func ResolveDefaultTextModel(model string, defaultText ...string) string {
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		return trimmed
	}
	if len(defaultText) > 0 && strings.TrimSpace(defaultText[0]) != "" {
		return strings.TrimSpace(defaultText[0])
	}
	return DefaultTextModel
}

// CanonicalImagineVideoModel 使用完整视频型号查询价格，缺省请求使用默认型号。
func CanonicalImagineVideoModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return DefaultImagineVideoModel
	}
	return model
}
