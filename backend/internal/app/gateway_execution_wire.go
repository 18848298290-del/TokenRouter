//go:build wireinject

package app

import (
	"github.com/google/wire"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
)

// Gateway text and media entry points need platform executors with request
// credentials and response-state adapters. Wire supplies those executors
// alongside provider route planning and the separate Forward and OpenAI rate
// resolvers used by completion recording.

// gatewayExecutionProviders supplies platform execution, credentials, response
// state, and rate-reader constructors used by the gateway graph.
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
