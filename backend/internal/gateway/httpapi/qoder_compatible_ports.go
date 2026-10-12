package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// QoderCompatibleTarget supports execution, credential refresh, and completion-data capture.
type QoderCompatibleTarget interface {
	Snapshot() provider.ProviderSnapshot
	Forward(context.Context, *gin.Context, []byte, protocol.ProtocolID, string) (*forward.MessagesResult, error)
	Refresh(context.Context) (QoderCompatibleTarget, error)
	Completion(context.Context, QoderCompletionCapture) *completion.Input
}

// QoderCompatibleSelection exposes the selected target, acquired slot, and scheduling feedback.
type QoderCompatibleSelection interface {
	Target() QoderCompatibleTarget
	Acquired() bool
	ReleaseFunc() func()
	WaitPlan() *scheduler.ProviderWaitPlan
	Report(int64, bool, *forward.MessagesResult)
	Switched()
}

// QoderCompatibleExecution supplies provider selection, routing, and sticky-session operations.
type QoderCompatibleExecution interface {
	Select(context.Context, *int64, string, string, map[int64]struct{}, int64) (QoderCompatibleSelection, error)
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	BindStickySession(context.Context, *int64, string, int64) error
}

// QoderCompletionCapture carries request and funding data captured before queueing, independent of Gin.
type QoderCompletionCapture struct {
	Result                                                              *forward.MessagesResult
	Key                                                                 *apikey.APIKey
	Subscription                                                        *billing.UserSubscription
	InboundEndpoint, UpstreamEndpoint, UserAgent, ClientIP, PayloadHash string
	Body                                                                []byte
	Pricing                                                             routing.PricingUsageFields
}

// QoderCompatibleOptions supplies shared services and resources to the HTTP adapter.
type QoderCompatibleOptions struct {
	Execution QoderCompatibleExecution
	Funding   interface {
		CheckKey(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
	}
	Recorder interface {
		Record(context.Context, *completion.Input, bool) error
	}
	Pool              *completion.UsageRecordWorkerPool
	Slots             *ConcurrencyHelper
	Enter             func() (func(), error)
	ReadAccess        func(*gin.Context) (*apikey.APIKey, bool)
	Errors            QoderErrorPresenter
	Rules             *errorpolicy.ErrorPassthroughService
	PlatformAvailable bool
	MayRefresh        func(error) bool
	MaySwitch         func(error) bool
}

// QoderCompatibleRuntime adapts compatible HTTP requests and captures completion inputs.
type QoderCompatibleRuntime struct {
	options           QoderCompatibleOptions
	concurrencyHelper *ConcurrencyHelper
}

func NewQoderCompatibleRuntime(options QoderCompatibleOptions) *QoderCompatibleRuntime {
	return &QoderCompatibleRuntime{options: options, concurrencyHelper: options.Slots}
}
