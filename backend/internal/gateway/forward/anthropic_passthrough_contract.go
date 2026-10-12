package forward

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// APIKeyInput contains passthrough bytes and the shared parsed request updated by platform rewrites.
type APIKeyInput struct {
	Body                        []byte
	Parsed                      *requeststate.ParsedRequest
	RequestModel, OriginalModel string
	RequestStream               bool
	StartTime                   time.Time
}

// PassthroughPorts supplies operations for one selected provider exchange.
type PassthroughPorts interface {
	Begin() (func(), error)
	Credential(context.Context) error
	TokenKind() string
	ResolveProxy()
	MarkPassthrough()
	FilterSearchHistory([]byte, string) []byte
	ReadErrorBody() ([]byte, error)
	ResetErrorBody([]byte)
	Health(context.Context, string, int, map[string][]string, []byte, string) ErrorDecision
	HandleError(context.Context, string, bool) (*Result, error)
	Observe(Notice)
	FailoverError(int, []byte, bool) error
	IsFailover(error) bool
	Log(string)
	Truncate(string, int) string
	ExecutePassthrough(context.Context, *APIKeyInput, MessageHooks) (upstream.AttemptResult, error)
	ServiceTier() string
}
