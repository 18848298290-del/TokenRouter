// scheduledTests.types.ts defines recurring provider test plans and their execution
// results. Plans select a model and cron expression alongside retention and recovery
// settings, while each result records response text, failure details, and the observed
// execution times.

// ScheduledTestPlan describes a recurring model test and its result retention settings.
export interface ScheduledTestPlan {
  id: number // Record numeric identifier.
  provider_id: number // Upstream provider identifier.
  model_id: string // Selected model identifier.
  cron_expression: string // Recurring test schedule.
  enabled: boolean // Feature activation switch.
  max_results: number // Retained test result count.
  auto_recover: boolean // Successful-test recovery activation.
  last_run_at: string | null // Last run timestamp.
  next_run_at: string | null // Next run timestamp.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
}

// ScheduledTestResult records the response and timing from one scheduled model test.
export interface ScheduledTestResult {
  id: number // Record numeric identifier.
  plan_id: number // Scheduled test plan identifier.
  status: string // Model test outcome.
  response_text: string // Model test response.
  error_message: string // Reported failure explanation.
  latency_ms: number // Measured latency milliseconds.
  started_at: string // Test execution start.
  finished_at: string // Test execution completion.
  created_at: string // Record creation timestamp.
}

// CreateScheduledTestPlanRequest supplies the provider, model, and schedule for a
// recurring test.
export interface CreateScheduledTestPlanRequest {
  provider_id: number // Upstream provider identifier.
  model_id: string // Selected model identifier.
  cron_expression: string // Recurring test schedule.
  enabled?: boolean // Feature activation switch.
  max_results?: number // Retained test result count.
  auto_recover?: boolean // Successful-test recovery activation.
}

// UpdateScheduledTestPlanRequest supplies optional changes to a model test and its
// schedule.
export interface UpdateScheduledTestPlanRequest {
  model_id?: string // Selected model identifier.
  cron_expression?: string // Recurring test schedule.
  enabled?: boolean // Feature activation switch.
  max_results?: number // Retained test result count.
  auto_recover?: boolean // Successful-test recovery activation.
}
