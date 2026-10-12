package mediaentry

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/openaiattempt"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Options configures provider switch limits and the non-streaming image heartbeat interval.
type Options struct {
	MaxSwitches    int
	ImageKeepalive time.Duration
}

// PlatformPorts supplies provider selection and single-attempt operations.
// Callers manage retries and completion.
type PlatformPorts struct {
	SystemOne     func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, []byte) (upstream.AttemptResult, error)
	SelectImages  func(context.Context, *int64, string, string, map[int64]struct{}, provider.OpenAIImagesCapability) (*gatewayadapter.SelectionResult, scheduler.PlatformDecision, error)
	Images        func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, []byte, *media.ImageRequest, string, ...egress.TLSFingerprintRouterMatchResult) (*forward.OpenAIResult, error)
	GrokMedia     func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, grok.GrokMediaEndpoint, string, []byte, string) (*forward.OpenAIResult, error)
	Embeddings    func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, []byte, string) (*forward.OpenAIResult, error)
	AlphaSearch   func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, []byte, ...egress.TLSFingerprintRouterMatchResult) (*forward.OpenAIResult, error)
	Voice         func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, string, []byte, string) (*forward.OpenAIResult, error)
	OpenRealtime  func(context.Context, *gatewayadapter.ExecutionProvider, string, string) (upstream.FrameConn, error)
	RealtimeError func(context.Context, *gatewayadapter.ExecutionProvider, int, []byte)
	RelayRealtime func(context.Context, upstream.FrameConn, upstream.FrameConn) (bool, error)
	Credential    func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider) (string, string, error)
	Stop429       func(*gatewayadapter.ExecutionProvider, int, int, *failover.OAuth429State) bool
	ReportSwitch  func()
}

// Bindings contains application-owned dependencies and an optional quota updater.
type Bindings struct {
	Common            openaiattempt.Bindings
	Platform          PlatformPorts
	Resources         *gatewayhttp.OpenAIHTTPResources
	Dependencies      gatewayhttp.OpenAIDependencies
	Options           Options
	Quota             gatewayadapter.QuotaUpdater
	CheckFunding      func(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
	PlanRoute         func(context.Context, *apikey.APIKey, string) routing.RoutePlan
	Isolate           func(context.Context, *apikey.APIKey, int64, string, string) error
	VideoTasks        func() *media.VideoTasks
	EligibilityProber gatewayadapter.GrokMediaEligibilityProber
}

// Runtime holds dependencies bound at construction and creates an adapter for each HTTP request.
type Runtime struct{ bindings Bindings }

func New(b Bindings) *Runtime { return &Runtime{bindings: b} }
func (h *Runtime) submitOpenAIUsageRecordTask(c *gin.Context, result *forward.OpenAIResult, task completion.UsageRecordTask) {
	images := 0
	if result != nil {
		images = result.ImageCount
	}
	h.bindings.Common.Support.Submission.SubmitImages(c, images, task)
}

func (h *Runtime) checkContentModeration(c *gin.Context, log *zap.Logger, key *apikey.APIKey, subject authctx.AuthSubject, protocol, model string, body []byte) *moderation.Decision {
	return gatewayhttp.RunContentModeration(gatewayhttp.GatewayModerationEndpoints{}, c, log, h.bindings.Common.Support.Moderation, apikey.CopyAPIKey(key), subject, protocol, model, body)
}

func (h *Runtime) handleOpenAISessionIsolationError(c *gin.Context, err error, started bool) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, session.ErrSessionIsolationConflict) {
		gatewayhttp.DefaultOpenAIErrorOutput().StreamError(c, http.StatusForbidden, "permission_error", session.SessionIsolationConflictMessage, started)
		return true
	}
	gatewayhttp.DefaultOpenAIErrorOutput().StreamError(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable", started)
	return true
}

func (h *Runtime) ensureGrokMediaProviderEligibility(ctx context.Context, value *gatewayadapter.ExecutionProvider) (bool, string, error) {
	var probe gatewayadapter.GrokMediaEligibilityProber
	if h != nil {
		probe = h.bindings.EligibilityProber
	}
	return gatewayadapter.CheckGrokMediaEligibility(ctx, value, probe)
}
