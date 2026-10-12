package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// RuntimeReaders 包含执行适配器使用的配置与策略读取接口。
// 各读取器共享应用实例；调用方仍在原请求位置读取动态值。
type RuntimeReaders struct {
	Attributes *routing.ModelAttributeService
	Gateway    *gateway.RuntimeSettings
	Provider   *provider.RuntimeSettings
	Quota      *provider.QuotaSettingsCache
	Routing    *routing.RuntimeSettings
	Moderation *moderation.RuntimeSettings
	Search     *search.ConfigService
	Scheduler  settings.Repository
}
