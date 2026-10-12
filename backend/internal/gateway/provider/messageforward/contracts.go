package messageforward

import (
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

const (
	MessagesBody BodyKind = iota
	CountBody
)

// Options holds startup transport parameters; dynamic policies are read during requests.
type Options struct {
	Configured           bool
	InjectAPIKeyBeta     bool
	LogErrorBody         bool
	LogErrorBodyMaxBytes int
	FailoverOn400        bool
	GeminiDebugHeaders   bool
	MaxLineSize          int
	StreamInterval       time.Duration
	StreamKeepalive      time.Duration
	ResponseReadLimit    int64
	PreserveContentType  bool
	URLAllowlistEnabled  bool
	AllowInsecureHTTP    bool
	URLValidation        egress.ValidationOptions
}

// AttemptState holds request preparation and response conversion state for one attempt.
// BetaEvaluated distinguishes an empty filter result from a query that has not run.
type AttemptState struct {
	BetaEvaluated bool
	BetaFilters   map[string]struct{}
	ToolNames     *anthropic.ToolNameRewrite
	MimicDebug    string
}

// BodyKind selects the entry point's response-body limit error format.
type BodyKind uint8

// HTTPBoundary provides output and observation operations for the current HTTP request.
// The HTTP adapter implements it and owns the Gin context.
type HTTPBoundary interface {
	Present() bool
	RequestPresent() bool
	HasHeaderFilter() bool
	RequestHeaders() http.Header
	Sink() upstream.OutputSink
	SearchOutput() searchtools.Output
	ConversionOutput(responses bool, state *AttemptState) forward.Output
	ReadResponseBody(io.Reader, int64, BodyKind) ([]byte, error)
	Size() int
	Written() bool
	Commit()
	GenericError()
	MessageError(int, string, string)
	RawError(int, []byte)
	CountError(int, string, string)
	CountSuccess(int, map[string][]string, []byte, bool)
	MarkPassthrough()
	ServiceTier() string
	SetError(int, string, string)
	Observe(forward.Notice)
	MatchRule(string, int, []byte) *errorpolicy.ErrorPassthroughRule
	SkipMonitoring()
	WriteHeaders(http.Header, http.Header, bool)
}
