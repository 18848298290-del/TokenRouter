// types.ts exposes shared Ops view state and the API-owned alert and settings
// types. Components reuse one chart display vocabulary while HTTP records remain
// defined in the admin Ops API module.

// ChartState selects a loading placeholder, empty message, or populated chart.
export type ChartState = 'loading' | 'empty' | 'ready'

// Re-export ops alert/settings types so view components can import from a single
// place while keeping the API contract centralized in `@/api/admin/ops`.
export type {
  AlertRule,
  AlertEvent,
  AlertSeverity,
  MetricType,
  Operator,
  EmailNotificationConfig,
  OpsDistributedLockSettings,
  OpsAlertRuntimeSettings,
  OpsMetricThresholds,
  OpsAdvancedSettings,
  OpsDataRetentionSettings,
  OpsRuntimeLogConfig,
  OpsSystemLog,
  OpsSystemLogSinkHealth,
} from '@/api/admin/ops'
