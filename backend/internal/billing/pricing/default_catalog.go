package pricing

// DefaultPriceValue 描述一个带明确单位的默认价格或倍率；nil 表示不适用。
type DefaultPriceValue struct {
	Key   string   `json:"key"`
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
}

// DefaultModelPrice 是管理员查询的模型基础价，分组和用户倍率在报价时另行应用。
type DefaultModelPrice struct {
	Source                        string                 `json:"source,omitempty"`
	PriceSources                  map[string]string      `json:"price_sources,omitempty"`
	ContextIntervals              []DefaultPriceInterval `json:"context_intervals,omitempty"`
	Model                         string                 `json:"model"`
	Platform                      string                 `json:"platform"`
	BillingMode                   string                 `json:"billing_mode"`
	PriceStatus                   string                 `json:"price_status"`
	Prices                        []DefaultPriceValue    `json:"prices"`
	LongContextThreshold          int                    `json:"long_context_threshold,omitempty"`
	LongContextThresholdInclusive bool                   `json:"long_context_threshold_inclusive,omitempty"`
}

// DefaultPriceInterval 让管理页展示任意数量的绝对上下文阶梯。
type DefaultPriceInterval struct {
	MinTokens int                 `json:"min_tokens"`
	MaxTokens *int                `json:"max_tokens"`
	Prices    []DefaultPriceValue `json:"prices"`
}

// DefaultBillingMode 从已发布报价确定管理目录的计费分类，缺少媒体价格时可使用 token 价格。
func DefaultBillingMode(model, mode string, raw *CatalogModelPricing) string {
	if raw != nil && len(raw.VideoPrices) > 0 {
		return "video"
	}
	imageModel := mode == "image" || HasExplicitImageGenerationPricing(raw) || LooksLikeImageModel(model)
	if raw != nil && len(raw.ImagePrices) > 0 || HasImageUnitPrice(raw) && (imageModel || raw.TokenPricingAbsent) {
		return "image"
	}
	if value, err := ResolveModelPricing(model, raw); err == nil && value != nil {
		return "token"
	}
	switch mode {
	case "image", "image_generation":
		return "image"
	case "video":
		return "video"
	default:
		return "token"
	}
}
