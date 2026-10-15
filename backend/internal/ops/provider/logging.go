package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// logging.go applies logging settings to the process logger and emits
// configuration audit events.
// Runtime log changes update level, caller reporting, stacktrace threshold, and
// sampling in the shared logging core. Configuration audit callbacks serialize
// old and new settings into structured events, while persistence ordering and
// rollback remain in the Ops service that invokes this adapter.

// LogControl applies Ops log settings and emits structured configuration-change
// diagnostics.
type LogControl struct{}

// Apply reconfigures the shared logging core from nonnil Ops runtime settings
// and returns its reconfiguration error. It normalizes level strings and copies
// caller/sampling options; retention days and settings persistence are handled
// by the owning Ops service.
func (LogControl) Apply(cfg *ops.OpsRuntimeLogConfig) error {
	if cfg == nil {
		return fmt.Errorf("nil runtime log config")
	}
	return logging.Reconfigure(func(opts *logging.InitOptions) error {
		opts.Level = strings.ToLower(strings.TrimSpace(cfg.Level))
		opts.Caller = cfg.Caller
		opts.StacktraceLevel = strings.ToLower(strings.TrimSpace(cfg.StacktraceLevel))
		opts.Sampling.Enabled = cfg.EnableSampling
		opts.Sampling.Initial = cfg.SamplingInitial
		opts.Sampling.Thereafter = cfg.SamplingNext
		return nil
	})
}

// Changed emits an info-level configuration audit event with operator identity,
// trimmed action, and serialized old/new settings. Nil settings serialize as
// null, and delivery follows the current logging configuration and its sink
// filters.
func (LogControl) Changed(operatorID int64, oldCfg *ops.OpsRuntimeLogConfig, newCfg *ops.OpsRuntimeLogConfig, action string) {
	// The serialized audit snapshots include retention and update metadata as well
	// as the live logger options. Current scalar fields are JSON-supported,
	// including null for nil settings.
	oldRaw, _ := json.Marshal(oldCfg)
	newRaw, _ := json.Marshal(newCfg)
	logging.With(
		zap.String("component", "audit.log_config_change"),
		zap.String("action", strings.TrimSpace(action)),
		zap.Int64("operator_id", operatorID),
		zap.String("old", string(oldRaw)),
		zap.String("new", string(newRaw)),
	).Info("runtime log config changed")
}

// Failed emits a warning-level configuration audit event with operator
// identity, trimmed reason, and serialized old/new settings. Nil settings
// serialize as null; this diagnostic callback neither returns delivery errors
// nor changes the settings result.
func (LogControl) Failed(operatorID int64, oldCfg *ops.OpsRuntimeLogConfig, newCfg *ops.OpsRuntimeLogConfig, reason string) {
	// The failure event records old and attempted configuration inputs. Their
	// JSON-supported scalar snapshots describe the attempt, while the service
	// separately handles application and rollback outcomes.
	oldRaw, _ := json.Marshal(oldCfg)
	newRaw, _ := json.Marshal(newCfg)
	logging.With(
		zap.String("component", "audit.log_config_change"),
		zap.String("action", "failed"),
		zap.Int64("operator_id", operatorID),
		zap.String("reason", strings.TrimSpace(reason)),
		zap.String("old", string(oldRaw)),
		zap.String("new", string(newRaw)),
	).Warn("runtime log config change failed")
}
