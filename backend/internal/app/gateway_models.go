package app

import (
	"context"

	"github.com/gin-gonic/gin"

	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// geminiModelReadTarget 保存本次选中的提供商，供模型资源查询使用。
type geminiModelReadTarget struct {
	reader *googleforward.Gemini
	value  *gatewayprovider.ExecutionProvider
}

// provideModelsHTTP 构造四个模型目录查询入口。
func provideModelsHTTP(catalog *catalogprovider.Service, catalogue *routing.RequestableCatalogue, reader *googleforward.Gemini, activity *gatewayRequestActivity, choices *selection.Gemini, attributes *routing.ModelAttributeService) *gatewayhttp.ModelsHandler {
	if catalog == nil {
		return modelsHTTP(nil, catalogue, reader, activity, choices, attributes)
	}
	return modelsHTTP(catalog, catalogue, reader, activity, choices, attributes)
}

// modelsHTTP 为目录查询绑定统一元数据读取和客户端权限上下文。
func modelsHTTP(catalog modelcatalog.Reader, catalogue *routing.RequestableCatalogue, reader *googleforward.Gemini, activity *gatewayRequestActivity, choices *selection.Gemini, attributes ...*routing.ModelAttributeService) *gatewayhttp.ModelsHandler {
	ports := gatewayhttp.ModelsPorts{
		ReadAccess: keyhttp.GetAPIKeyFromContext, ReadPlatform: keyhttp.GetForcePlatformFromContext,
		ReadBilling: func(c *gin.Context) (*billing.APIKeyBillingContext, bool) {
			return gatewayhttp.GetAPIKeyBillingContext(c)
		},
		SafeSegment: gemini.IsSafeGeminiModelPathSegment,
		SelectModel: func(ctx context.Context, id *int64) (gatewayhttp.GeminiModelReader, error) {
			value, err := choices.SelectProviderForAIStudioEndpoints(ctx, id)
			if err != nil {
				return nil, err
			}
			return geminiModelReadTarget{reader, value}, nil
		},
		CheckAntigravity: choices.HasAntigravityProviders,
	}
	if catalogue != nil {
		ports.Catalogue = catalogue
	}
	display := gatewayprovider.ModelDisplayCatalogue{}
	if catalog != nil {
		display.Source = catalog
	}
	result := gatewayhttp.NewModelsHandler(ports, display, attributes...)
	if activity != nil {
		result.BindRequestActivity(activity.Enter)
	}
	return result
}

func (p geminiModelReadTarget) Read(ctx context.Context, path string) (*gatewayhttp.ModelHTTPResponse, error) {
	value, err := p.reader.ForwardAIStudioGET(ctx, p.value, path)
	if value == nil {
		return nil, err
	}
	return &gatewayhttp.ModelHTTPResponse{StatusCode: value.StatusCode, Headers: value.Headers, Body: value.Body}, err
}

// resolveModelsListReadLimit 返回模型目录读取上限，缺省或非正值使用默认值。
func resolveModelsListReadLimit(cfg *config.Config) int64 {
	if cfg != nil && cfg.Gateway.ModelsListReadMaxBytes > 0 {
		return cfg.Gateway.ModelsListReadMaxBytes
	}
	return config.DefaultModelsListReadMaxBytes
}
