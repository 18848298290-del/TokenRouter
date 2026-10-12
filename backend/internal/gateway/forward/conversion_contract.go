package forward

import (
	"context"
	"encoding/json"
)

// ConversionInput identifies OAuth requests; credentials are acquired during execution.
type ConversionInput struct{ OAuth bool }

// ErrorDecision contains the provider health-policy outcome.
type ErrorDecision struct{ Generic, Failover, RetrySameProvider bool }

// ConversionPorts supplies request preparation, network exchange, health checks, and output for protocol conversion.
type ConversionPorts interface {
	NormalizeResponses([]byte) ([]byte, bool, error)
	ResolveModel(context.Context, string) string
	Effort([]byte, bool, ...string) *string
	ThinkingFallback(*string, []byte, string) *string
	ModelNotice(string, string, string, bool)
	Mimic(context.Context, []byte, json.RawMessage, string) []byte
	CacheLimit([]byte) []byte
	Credential(context.Context) error
	Build(context.Context, []byte, string, bool, bool) ([]byte, error)
	Send(context.Context) (Response, error)
	ReadErrorBody() ([]byte, error)
	ErrorMessage([]byte) string
	Health(context.Context, int, []byte, string) ErrorDecision
	FailoverNotice(int, string)
	FailoverError(int, []byte, bool) error
	Output() Output
}

// MapStatus maps statuses of 500 or higher to 502 and passes lower values through.
func MapStatus(status int) int {
	if status >= 500 {
		return 502
	}
	return status
}
