package backup

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	DefaultDataManagementAgentSocketPath = "/tmp/tokenrouter-datamanagement.sock"
	DataManagementDeprecatedReason       = "DATA_MANAGEMENT_DEPRECATED"
	DataManagementAgentUnavailableReason = "DATA_MANAGEMENT_AGENT_UNAVAILABLE"
)

// ErrDataManagementDeprecated identifies requests to the retired data-management RPC API.
var ErrDataManagementDeprecated = infraerrors.ServiceUnavailable(
	DataManagementDeprecatedReason,
	"data management feature is deprecated",
)

type DataManagementAgentHealth struct {
	Enabled    bool
	Reason     string
	SocketPath string
}

// DataManagementService retains the retired API and reports deprecation without contacting an agent.
type DataManagementService struct {
	socketPath string
}

func NewDataManagementService() *DataManagementService {
	return NewDataManagementServiceWithOptions(DefaultDataManagementAgentSocketPath, 500*time.Millisecond)
}

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

func (s *DataManagementService) SocketPath() string {
	if s == nil || strings.TrimSpace(s.socketPath) == "" {
		return DefaultDataManagementAgentSocketPath
	}
	return s.socketPath
}

func (s *DataManagementService) GetAgentHealth(ctx context.Context) DataManagementAgentHealth {
	_ = ctx
	return DataManagementAgentHealth{
		Enabled:    false,
		Reason:     DataManagementDeprecatedReason,
		SocketPath: s.SocketPath(),
	}
}

func (s *DataManagementService) EnsureAgentEnabled(ctx context.Context) error {
	_ = ctx
	return ErrDataManagementDeprecated.WithMetadata(map[string]string{"socket_path": s.SocketPath()})
}
