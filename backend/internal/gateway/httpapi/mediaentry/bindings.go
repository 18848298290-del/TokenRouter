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

// bindings.go defines shared media execution bindings and constructs the
// runtime used by HTTP request adapters.
// Media HTTP factories share application-owned selection, funding,
// concurrency and completion services while creating separate state for each
// request. Platform callbacks perform one selected-provider operation, so the
// media attempt loops can retain their own retry and completion decisions
// across Images, video, Embeddings, search and audio entry points.

// Options supplies provider transition and nonstreaming image heartbeat
// settings to request adapters.
type Options struct {
	MaxSwitches int // allowed provider transition count
	// ImageKeepalive schedules nonstreaming JSON padding. Nonpositive
	// durations disable that heartbeat through
	// StartOpenAIImagesJSONKeepalive at the request entry.
	ImageKeepalive time.Duration
}

// PlatformPorts binds selected-provider execution and status-specific
// recovery for media requests.
type PlatformPorts struct {
	// selected-provider SystemOne request execution
	SystemOne func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, []byte) (upstream.AttemptResult, error)
	// Images capability provider selection
	SelectImages func(context.Context, *int64, string, string, map[int64]struct{}, provider.OpenAIImagesCapability) (*gatewayadapter.SelectionResult, scheduler.PlatformDecision, error)
	// selected-provider image request execution
	Images func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, []byte, *media.ImageRequest, string, ...egress.TLSFingerprintRouterMatchResult) (*forward.OpenAIResult, error)
	// selected-provider Grok media execution
	GrokMedia func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, grok.GrokMediaEndpoint, string, []byte, string) (*forward.OpenAIResult, error)
	// selected-provider Embeddings request execution
	Embeddings func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, []byte, string) (*forward.OpenAIResult, error)
	// selected-provider AlphaSearch request execution
	AlphaSearch func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, []byte, ...egress.TLSFingerprintRouterMatchResult) (*forward.OpenAIResult, error)
	// selected-provider Grok Voice execution
	Voice func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider, string, []byte, string) (*forward.OpenAIResult, error)
	// upstream Realtime connection opening OpenRealtime returns an upstream
	// frame connection after successful dialing. Its caller transfers that
	// connection into a Realtime lease and closes it before releasing
	// provider capacity.
	OpenRealtime func(context.Context, *gatewayadapter.ExecutionProvider, string, string) (upstream.FrameConn, error)
	// upstream Realtime handshake failure policy
	RealtimeError func(context.Context, *gatewayadapter.ExecutionProvider, int, []byte)
	// bidirectional Realtime frame exchange RelayRealtime reports audio
	// observation while exchanging frames. The HTTP owner retains client
	// and upstream connection cleanup, using accepted closure status before
	// capturing any completion.
	RelayRealtime func(context.Context, upstream.FrameConn, upstream.FrameConn) (bool, error)
	// selected-provider credential resolution callback
	Credential func(context.Context, *gin.Context, *gatewayadapter.ExecutionProvider) (string, string, error)
	// OAuth status-specific retry stopping
	Stop429      func(*gatewayadapter.ExecutionProvider, int, int, *failover.OAuth429State) bool
	ReportSwitch func() // AlphaSearch provider transition observation
}

// Bindings retains application services used by media HTTP factories and
// request adapters.
type Bindings struct {
	// shared attempt and completion services Common, Platform and Resources
	// retain shared application services. Factories copy binding values
	// into request adapters while the referenced service owners remain
	// responsible for usable lifetime through requests and completion
	// recording.
	Common   openaiattempt.Bindings
	Platform PlatformPorts // bound media platform attempt operations
	// application-owned HTTP concurrency resources
	Resources *gatewayhttp.OpenAIHTTPResources
	// entry dependency availability checks
	Dependencies gatewayhttp.OpenAIDependencies
	Options      Options // retained media request execution settings
	// optional API Key quota updater Quota optionally updates Key usage
	// after recorded completion. Nil leaves the recorder without this
	// Key-specific updater while other accounting proceeds.
	Quota gatewayadapter.QuotaUpdater
	// authenticated funding and RPM checker CheckFunding expects the
	// authenticated Key and resolved subscription. Its final flag selects
	// funding-only recheck when true and funding plus RPM policy when
	// false.
	CheckFunding func(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
	// current group route planning callback
	PlanRoute func(context.Context, *apikey.APIKey, string) routing.RoutePlan
	// explicit session ownership check callback Isolate is needed when an
	// image request supplies an explicit session hash. It checks that hash
	// under the authenticated Key and user ownership.
	Isolate func(context.Context, *apikey.APIKey, int64, string, string) error
	// saved video task service accessor
	// VideoTasks supplies the shared saved-owner and deferred-video-billing
	// service. Video lookup and accepted creation completion require this
	// callback and a usable returned service.
	VideoTasks func() *media.VideoTasks
	// optional Grok billing eligibility probe EligibilityProber is optional
	// when cached policy observations determine eligibility. Missing
	// billing observations require the probe; absent binding then returns
	// an eligibility reason and error.
	EligibilityProber gatewayadapter.GrokMediaEligibilityProber
}

// Runtime retains shared media services and constructs separate per-request
// HTTP adapters.
type Runtime struct {
	bindings Bindings // retained shared application bindings
}

// New retains a value copy of media bindings for request adapter factories.
// Referenced services and callbacks remain application-owned and shared, so
// callers finish assembly before requests begin and keep those bindings
// usable.
func New(b Bindings) *Runtime { return &Runtime{bindings: b} }

// submitOpenAIUsageRecordTask submits completion using the result's observed
// image count. Nil results contribute zero; positive image counts require
// mandatory submission, while other outcomes use the configured ordinary
// worker submission policy.
func (h *Runtime) submitOpenAIUsageRecordTask(c *gin.Context, result *forward.OpenAIResult, task completion.UsageRecordTask) {
	images := 0
	if result != nil {
		images = result.ImageCount
	}
	h.bindings.Common.Support.Submission.SubmitImages(c, images, task)
}

// checkContentModeration runs the shared media content check with a copied
// Key view and supplied authenticated subject. Disabled, unavailable or
// failed moderation can return nil through RunContentModeration; callers
// interpret a returned decision's Blocked flag.
func (h *Runtime) checkContentModeration(c *gin.Context, log *zap.Logger, key *apikey.APIKey, subject authctx.AuthSubject, protocol, model string, body []byte) *moderation.Decision {
	return gatewayhttp.RunContentModeration(gatewayhttp.GatewayModerationEndpoints{}, c, log, h.bindings.Common.Support.Moderation, apikey.CopyAPIKey(key), subject, protocol, model, body)
}

// handleOpenAISessionIsolationError presents stream-aware errors for session
// ownership checks and reports whether entry should stop. Nil permits
// continuation; ownership conflict returns permission denial, while another
// failure receives a temporary-service error.
func (h *Runtime) handleOpenAISessionIsolationError(c *gin.Context, err error, started bool) bool {
	if err == nil {
		return false
	}
	// Ownership conflicts have a permission response; unavailable isolation
	// checks use a temporary-service response so explicit sessions do not
	// proceed unchecked.
	if errors.Is(err, session.ErrSessionIsolationConflict) {
		gatewayhttp.DefaultOpenAIErrorOutput().StreamError(c, http.StatusForbidden, "permission_error", session.SessionIsolationConflictMessage, started)
		return true
	}
	gatewayhttp.DefaultOpenAIErrorOutput().StreamError(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable", started)
	return true
}

// ensureGrokMediaProviderEligibility checks policy eligibility and uses the
// optional prober when billing observations are missing. A nil runtime
// supplies no prober, so an unobserved billing state returns the shared
// unavailable-probe reason and error.
func (h *Runtime) ensureGrokMediaProviderEligibility(ctx context.Context, value *gatewayadapter.ExecutionProvider) (bool, string, error) {
	var probe gatewayadapter.GrokMediaEligibilityProber
	if h != nil {
		probe = h.bindings.EligibilityProber
	}
	return gatewayadapter.CheckGrokMediaEligibility(ctx, value, probe)
}
