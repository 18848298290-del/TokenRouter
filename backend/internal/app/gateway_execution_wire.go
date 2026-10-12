//go:build wireinject

package app

import (
	"github.com/google/wire"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
)

// gatewayExecutionProviders binds executors, credentials, health feedback, and session state constructors.
var gatewayExecutionProviders = wire.NewSet(
	provideCodexTurnStateHeaders,
	provideGrokCredentialRecovery,
	provideRequestCredentials,
	provideGrokHealth,
	provideGrokExecutor,
	provideWSConnections,
	provideGrokVideoTasks,
	provideCompactExecutor,
	provideOpenAIResponseHealth,
	provideOpenAIResponseOutput,
	provideReasoningHistory,
	provideRequestCredentialExecutor,
	gatewayadapter.NewRoutePlanner,
	provideRetryCooldown,
	provideGatewayRequestDebug,
	provideMessagesExecution,
	provideGatewayModelAvailability,
	provideResponseHeaderFilter,
	provideOpenAIResponseState,
	provideExecutionAgentIdentity,
	provideAnthropicPromptCache,
	provideOpenAITextExecutor,
	provideOpenAIImageBridgePolicy,
	provideOpenAIEncryptedLineage,
	provideOpenAIWebSockets,
	provideResponsesWSRuntime,
	provideOpenAIResponses,
	provideOpenAIAuxiliary,
	provideSystemOneExecutor,
	provideOpenAIImages,
	provideGatewayBillingRates,
	provideCreativeExecutor,
	provideCreativeTargets,
	provideGeminiForward,
	provideGeminiExecutor,
	provideAntigravityForward,
	provideAntigravityExecutor,
	provideAntigravityErrorObserver,
)
