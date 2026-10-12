//go:build wireinject

package app

import (
	"github.com/google/wire"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaypg "github.com/TokenFlux/TokenRouter/internal/gateway/postgres"
	gatewayredis "github.com/TokenFlux/TokenRouter/internal/gateway/rediscache"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
)

var (
	// gatewayAssemblyProviders groups gateway HTTP and executor Wire providers.
	gatewayAssemblyProviders = wire.NewSet(
		gatewaysession.NewDigestSessionStore,
		provideQoderRuntime,
		provideBackendMode,
		provideMediaHTTP,
		provideMediaRuntime,
		provideAuxiliaryHTTP,
		provideLiveHTTP,
		provideLiveExecution,
		gatewaySearchProviders,
		provideMessageHTTPBindings,
		provideMessageAttemptRuntime,
		provideMessagesHTTP,
		provideGatewayRequestActivity,
		provideModelsHTTP,
		provideResponsesWSHTTP,
		provideOpenAITextHTTP,
		provideOpenAITextAttemptRuntime,
		provideOpenAIAttemptBindings,
		provideUnifiedTextExecutor,
		provideOpenAITokensHTTP,
		provideGeminiNativeHTTP,
		provideCompatibleTextHTTP,
		provideQoderCompatibleHTTP,
		provideCountTokensHTTP,
		provideGatewayPromptPolicy,
		ProvideGatewayCompletionRecorders,
		gatewayredis.NewGatewayCache,
		gatewayErrorRulesProviders,
		provideUsageRecordWorkerPool,
		provideQoderRequestActivity,
		provideQoderChat,
		provideExecutionProviderStore,
		provideFundingAdmission,
		gatewayExecutionProviders,
		provideOpenAIHTTPResources,
		provideCyberBlocks,
		provideCyberHTTP,
		provideGatewayRouteMiddleware,
		provideGatewayAdminRules,
		provideGatewaySettings,
		provideGatewayRuntimeReaders,
		gatewayhttp.NewRuntimeSettingsHandler,
	)

	gatewayErrorRulesProviders = wire.NewSet(provideGatewayErrorRules, gatewaypg.NewErrorPassthroughRepository, gatewayredis.NewErrorPassthroughCache, gatewayhttp.NewErrorPassthroughHandler)
)
