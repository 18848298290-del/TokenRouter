package backup

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Data-management health remains queryable after the agent RPC feature is
// retired. The service reports a disabled status and the configured socket path
// for diagnosis, while command entry points return the deprecation error before
// attempting a connection or applying database and storage settings.

// Legacy socket and reason values keep health and command responses consistent
// while distinguishing retirement from an HTTP handler with no configured service.
const (
	DefaultDataManagementAgentSocketPath = "/tmp/tokenrouter-datamanagement.sock" // default reported socket path
	DataManagementDeprecatedReason       = "DATA_MANAGEMENT_DEPRECATED"           // retired agent operation reason
	DataManagementAgentUnavailableReason = "DATA_MANAGEMENT_AGENT_UNAVAILABLE"    // unconfigured agent service reason
)

// ErrDataManagementDeprecated reports retirement of the agent command API as
// service unavailability; EnsureAgentEnabled adds the diagnostic socket path to its metadata.
var ErrDataManagementDeprecated = infraerrors.ServiceUnavailable(
	DataManagementDeprecatedReason,
	"data management feature is deprecated",
)

// DataManagementAgentHealth describes disabled agent availability for the compatibility health endpoint.
type DataManagementAgentHealth struct {
	Enabled    bool   // agent operation availability flag
	Reason     string // disabled service reason code
	SocketPath string // reported agent socket path
}

// DataManagementService retains the retired API and reports deprecation without contacting an agent.
type DataManagementService struct {
	socketPath string // normalized diagnostic socket path
}

// NewDataManagementService constructs the retired agent facade with its default
// diagnostic socket path. The facade answers local health queries and reports
// deprecation for command guards regardless of whether that socket exists.
func NewDataManagementService() *DataManagementService {
	return NewDataManagementServiceWithOptions(DefaultDataManagementAgentSocketPath, 500*time.Millisecond)
}

// NewDataManagementServiceWithOptions constructs the retired agent facade with
// a diagnostic socket path. Whitespace is trimmed and empty paths select the
// default; dialTimeout is retained in the signature for compatibility but no dial occurs.
func NewDataManagementServiceWithOptions(socketPath string, dialTimeout time.Duration) *DataManagementService {
	_ = dialTimeout
	path := strings.TrimSpace(socketPath)
	if path == "" {
		path = DefaultDataManagementAgentSocketPath
	}
	return &DataManagementService{
		socketPath: path,
	}
}

// SocketPath returns the diagnostic path supplied at construction. Nil receivers
// and empty stored paths return the default path, allowing health and error
// responses to include a usable location even without a constructed facade.
func (s *DataManagementService) SocketPath() string {
	if s == nil || strings.TrimSpace(s.socketPath) == "" {
		return DefaultDataManagementAgentSocketPath
	}
	return s.socketPath
}

// GetAgentHealth returns disabled availability with the deprecation reason and
// diagnostic socket path. It ignores ctx because health is constructed locally;
// no agent connection is attempted, including when that context is cancelled.
func (s *DataManagementService) GetAgentHealth(ctx context.Context) DataManagementAgentHealth {
	_ = ctx
	return DataManagementAgentHealth{
		Enabled:    false,
		Reason:     DataManagementDeprecatedReason,
		SocketPath: s.SocketPath(),
	}
}

// EnsureAgentEnabled returns the deprecation error with the diagnostic socket
// path attached. It ignores ctx and performs no dial, letting compatibility command
// handlers report retirement after their existing request validation has completed.
func (s *DataManagementService) EnsureAgentEnabled(ctx context.Context) error {
	_ = ctx
	return ErrDataManagementDeprecated.WithMetadata(map[string]string{"socket_path": s.SocketPath()})
}
