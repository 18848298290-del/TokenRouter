package app

import "github.com/TokenFlux/TokenRouter/internal/app/lifecycle"

// gatewayRequestActivity tracks request admission, upstream attempts, and completion enqueueing through one
// shutdown barrier.
// Upstream requests retain their own contexts; Qoder tail-usage collection has a separate time budget.
type gatewayRequestActivity struct{ *lifecycle.Operations }

func provideGatewayRequestActivity(manager *lifecycle.Manager) *gatewayRequestActivity {
	activity := &gatewayRequestActivity{Operations: lifecycle.NewOperations("GatewayRequestsAndAttempts")}
	manager.Register(lifecycle.Hook{Name: "GatewayRequestsAndAttempts", StopOrder: 15, Stop: activity.StopContext})
	return activity
}
