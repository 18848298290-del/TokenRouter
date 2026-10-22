package app

import "github.com/TokenFlux/TokenRouter/internal/app/lifecycle"

// gateway_activity.go registers the shared gateway operation barrier with
// application shutdown.
// Gateway requests and attempts can still enqueue completion work while HTTP
// shutdown is underway. Registering their operation barrier before completion
// and storage shutdown makes the lifecycle manager wait for that work before
// stopping its downstream dependencies.

// gatewayRequestActivity is the operation barrier shared by gateway request and
// attempt consumers. Its stop callback waits for operation releases; upstream
// requests retain their own contexts and cancellation behavior.
type gatewayRequestActivity struct {
	*lifecycle.Operations /* gateway operation shutdown barrier */
}

// provideGatewayRequestActivity creates the gateway operation barrier and
// registers its shutdown hook with manager. Consumers release every operation
// entered through the barrier; its stop order precedes completion and shared
// storage shutdown. Call before manager startup or shutdown, while hook
// registration remains available.
func provideGatewayRequestActivity(manager *lifecycle.Manager) *gatewayRequestActivity {
	activity := &gatewayRequestActivity{Operations: lifecycle.NewOperations("GatewayRequestsAndAttempts")}
	manager.Register(lifecycle.Hook{Name: "GatewayRequestsAndAttempts", StopOrder: shutdownStageRequests, Stop: activity.StopContext})
	return activity
}
