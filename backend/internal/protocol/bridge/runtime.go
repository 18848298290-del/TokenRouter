package bridge

import "time"

// Runtime supplies caller-selected clock and random sources for protocol conversion.
// Each conversion uses these functions instead of a global source.
// @project-doc docs/architecture/gateway_request_lifecycle.md#protocol_conversion_boundary
type Runtime struct {
	Now        func() time.Time
	ReadRandom func([]byte) (int, error)
}
